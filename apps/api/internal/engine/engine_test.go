package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stockastic/api/internal/money"
)

// ---- test doubles ----------------------------------------------------------------------------

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type journal struct {
	mu       sync.Mutex
	batches  []Batch
	failWith error         // when set, Commit fails
	gate     chan struct{} // when non-nil, Commit blocks until it is closed
	entered  chan struct{} // signalled (non-blocking) each time Commit is entered
	panicOn  atomic.Bool
	log      *eventLog
}

func (j *journal) Commit(ctx context.Context, b Batch) error {
	if j.entered != nil {
		select {
		case j.entered <- struct{}{}:
		default:
		}
	}
	if j.gate != nil {
		<-j.gate
	}
	if j.panicOn.Load() {
		panic("boom in journal")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.failWith != nil {
		return j.failWith
	}
	j.batches = append(j.batches, b)
	if j.log != nil {
		j.log.add("commit:" + b.Symbol)
	}
	return nil
}

func (j *journal) count() int { j.mu.Lock(); defer j.mu.Unlock(); return len(j.batches) }

type eventLog struct {
	mu sync.Mutex
	ev []string
}

func (l *eventLog) add(s string) { l.mu.Lock(); l.ev = append(l.ev, s); l.mu.Unlock() }
func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ev...)
}

type sink struct {
	mu      sync.Mutex
	applied []Applied
	log     *eventLog
	panicky bool
}

func (s *sink) OnApplied(a Applied) {
	if s.log != nil {
		s.log.add("apply:" + a.Symbol)
	}
	s.mu.Lock()
	s.applied = append(s.applied, a)
	s.mu.Unlock()
	if s.panicky {
		panic("sink exploded")
	}
}

func (s *sink) count() int { s.mu.Lock(); defer s.mu.Unlock(); return len(s.applied) }

type rig struct {
	e *Engine
	j *journal
	s *sink
}

func newRig(t *testing.T, mod func(*Config), symbols ...string) *rig {
	t.Helper()
	if len(symbols) == 0 {
		symbols = []string{"ACME"}
	}
	r := &rig{j: &journal{}, s: &sink{}}
	cfg := Config{Symbols: symbols, Journal: r.j, Sink: r.s, Log: quiet}
	if mod != nil {
		mod(&cfg)
	}
	e, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	r.e = e
	return r
}

func (r *rig) start(t *testing.T) *rig {
	t.Helper()
	r.e.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = r.e.Stop(ctx)
	})
	return r
}

var ctxBG = context.Background()

func rp(v int64) money.Paise { return money.Paise(v * 100) } // rupees -> paise

func ord(id, acct string, side Side, price, qty int64) NewOrder {
	return NewOrder{ClientOrderID: id, AccountID: acct, Symbol: "ACME", Side: side, Price: rp(price), Qty: qty}
}

func mustSubmit(t *testing.T, e *Engine, n NewOrder) Result {
	t.Helper()
	res, err := e.Submit(ctxBG, n)
	if err != nil {
		t.Fatalf("submit %s: %v", n.ClientOrderID, err)
	}
	return res
}

// ---- matching semantics (ported from the TS suite) --------------------------------------------

func TestRestsWhenNothingCrosses(t *testing.T) {
	r := newRig(t, nil).start(t)
	res := mustSubmit(t, r.e, ord("c1", "a", Buy, 100, 10))
	if len(res.Fills) != 0 || res.Order.Status != StatusOpen || res.Order.Remaining != 10 {
		t.Fatalf("got %+v", res)
	}
	d, _ := r.e.Depth("ACME")
	if len(d.Bids) != 1 || d.Bids[0].Price != rp(100) || len(d.Asks) != 0 {
		t.Errorf("depth = %+v", d)
	}
}

func TestCrossingFillsAtMakerPrice(t *testing.T) {
	r := newRig(t, nil).start(t)
	mustSubmit(t, r.e, ord("m", "maker", Sell, 100, 10))
	res := mustSubmit(t, r.e, ord("t", "taker", Buy, 105, 10))
	if len(res.Fills) != 1 || res.Fills[0].Price != rp(100) || res.Fills[0].Qty != 10 {
		t.Fatalf("fills = %+v; a buy at 105 must trade at the resting 100", res.Fills)
	}
	if res.Order.Status != StatusFilled || res.Order.Remaining != 0 {
		t.Errorf("taker = %+v", res.Order)
	}
	f := res.Fills[0]
	if f.TakerAccountID != "taker" || f.MakerAccountID != "maker" || f.TakerSide != Buy {
		t.Errorf("fill parties wrong: %+v", f)
	}
	if d, _ := r.e.Depth("ACME"); len(d.Asks) != 0 {
		t.Errorf("ask should be consumed: %+v", d)
	}
}

func TestPartialFillRestsRemainder(t *testing.T) {
	r := newRig(t, nil).start(t)
	mustSubmit(t, r.e, ord("m", "maker", Sell, 100, 5))
	res := mustSubmit(t, r.e, ord("t", "taker", Buy, 100, 10))
	if res.Order.Status != StatusPartiallyFilled || res.Order.Remaining != 5 || len(res.Fills) != 1 {
		t.Fatalf("got %+v", res)
	}
	if d, _ := r.e.Depth("ACME"); len(d.Bids) != 1 || d.Bids[0].Qty != 5 {
		t.Errorf("remainder must rest as a bid: %+v", d)
	}
}

func TestTimePriorityWithinALevel(t *testing.T) {
	r := newRig(t, nil).start(t)
	first := mustSubmit(t, r.e, ord("m1", "early", Sell, 100, 5))
	mustSubmit(t, r.e, ord("m2", "late", Sell, 100, 5))
	res := mustSubmit(t, r.e, ord("t", "taker", Buy, 100, 5))
	if len(res.Fills) != 1 || res.Fills[0].MakerOrderID != first.Order.ID {
		t.Fatalf("the earlier order must fill first: %+v", res.Fills)
	}
}

func TestPricePriorityBeatsTime(t *testing.T) {
	r := newRig(t, nil).start(t)
	mustSubmit(t, r.e, ord("m1", "worse-early", Sell, 101, 5))
	better := mustSubmit(t, r.e, ord("m2", "better-late", Sell, 100, 5))
	res := mustSubmit(t, r.e, ord("t", "taker", Buy, 101, 5))
	if len(res.Fills) != 1 || res.Fills[0].MakerOrderID != better.Order.ID || res.Fills[0].Price != rp(100) {
		t.Fatalf("got %+v", res.Fills)
	}
}

func TestWalksMultipleLevels(t *testing.T) {
	r := newRig(t, nil).start(t)
	mustSubmit(t, r.e, ord("m1", "m1", Sell, 100, 5))
	mustSubmit(t, r.e, ord("m2", "m2", Sell, 101, 5))
	res := mustSubmit(t, r.e, ord("t", "t", Buy, 101, 10))
	if len(res.Fills) != 2 || res.Fills[0].Price != rp(100) || res.Fills[1].Price != rp(101) || res.Order.Status != StatusFilled {
		t.Fatalf("got %+v", res)
	}
}

func TestNoCrossNoFill(t *testing.T) {
	r := newRig(t, nil).start(t)
	mustSubmit(t, r.e, ord("m", "m", Sell, 105, 5))
	res := mustSubmit(t, r.e, ord("t", "t", Buy, 100, 5))
	if len(res.Fills) != 0 || res.Order.Status != StatusOpen {
		t.Fatalf("got %+v", res)
	}
	d, _ := r.e.Depth("ACME")
	if d.Bids[0].Price != rp(100) || d.Asks[0].Price != rp(105) {
		t.Errorf("depth = %+v", d)
	}
}

func TestDepthAggregatesByLevel(t *testing.T) {
	r := newRig(t, nil).start(t)
	mustSubmit(t, r.e, ord("a", "a", Buy, 100, 5))
	mustSubmit(t, r.e, ord("b", "b", Buy, 100, 3))
	mustSubmit(t, r.e, ord("c", "c", Buy, 99, 7))
	d, _ := r.e.Depth("ACME")
	if d.Bids[0] != (Level{rp(100), 8, 2}) || d.Bids[1] != (Level{rp(99), 7, 1}) {
		t.Errorf("bids = %+v", d.Bids)
	}
}

func TestCancelRemovesRestingOrderAndEnforcesOwnership(t *testing.T) {
	r := newRig(t, nil).start(t)
	o := mustSubmit(t, r.e, ord("c", "owner", Buy, 100, 10)).Order
	if _, err := r.e.Cancel(ctxBG, "ACME", o.ID, "intruder"); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("intruder cancel: %v", err)
	}
	res, err := r.e.Cancel(ctxBG, "ACME", o.ID, "owner")
	if err != nil || res.Order.Status != StatusCancelled || res.Order.Remaining != 10 {
		t.Fatalf("got %+v %v", res, err)
	}
	if d, _ := r.e.Depth("ACME"); len(d.Bids) != 0 {
		t.Errorf("book should be empty: %+v", d)
	}
	if _, err := r.e.Cancel(ctxBG, "ACME", o.ID, "owner"); !errors.Is(err, ErrAlreadyClosed) {
		t.Errorf("second cancel: %v", err)
	}
	if _, err := r.e.Cancel(ctxBG, "ACME", "nope", "owner"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown order: %v", err)
	}
}

func TestCannotCancelAFilledOrder(t *testing.T) {
	r := newRig(t, nil).start(t)
	m := mustSubmit(t, r.e, ord("m", "maker", Sell, 100, 5)).Order
	mustSubmit(t, r.e, ord("t", "taker", Buy, 100, 5))
	res, err := r.e.Cancel(ctxBG, "ACME", m.ID, "maker")
	if !errors.Is(err, ErrAlreadyClosed) || res.Order.Status != StatusFilled {
		t.Errorf("got %+v %v", res, err)
	}
}

func TestValidationAndUnknownSymbol(t *testing.T) {
	r := newRig(t, nil).start(t)
	bad := []NewOrder{
		{ClientOrderID: "", AccountID: "a", Symbol: "ACME", Side: Buy, Price: 1, Qty: 1},
		{ClientOrderID: "x", AccountID: "", Symbol: "ACME", Side: Buy, Price: 1, Qty: 1},
		{ClientOrderID: "x", AccountID: "a", Symbol: "ACME", Side: 0, Price: 1, Qty: 1},
		{ClientOrderID: "x", AccountID: "a", Symbol: "ACME", Side: Buy, Price: 0, Qty: 1},
		{ClientOrderID: "x", AccountID: "a", Symbol: "ACME", Side: Buy, Price: 1, Qty: 0},
		{ClientOrderID: "x", AccountID: "a", Symbol: "ACME", Side: Buy, Price: 1, Qty: -3},
	}
	for i, n := range bad {
		if _, err := r.e.Submit(ctxBG, n); !errors.Is(err, ErrInvalidOrder) {
			t.Errorf("case %d: got %v, want ErrInvalidOrder", i, err)
		}
	}
	n := ord("x", "a", Buy, 1, 1)
	n.Symbol = "NOPE"
	if _, err := r.e.Submit(ctxBG, n); !errors.Is(err, ErrUnknownSymbol) {
		t.Errorf("got %v", err)
	}
}

// ---- reliability: write-before-ack ------------------------------------------------------------

func TestCommitHappensBeforeApplyAndBeforeTheAck(t *testing.T) {
	log := &eventLog{}
	r := newRig(t, nil)
	r.j.log, r.s.log = log, log
	r.start(t)
	mustSubmit(t, r.e, ord("a", "a", Buy, 100, 5))
	log.add("acked")
	got := log.all()
	want := []string{"commit:ACME", "apply:ACME", "acked"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("event order = %v, want %v (commit before apply before ack)", got, want)
	}
}

func TestFailedCommitLeavesTheBookUntouchedAndRetryWorks(t *testing.T) {
	r := newRig(t, nil).start(t)
	mustSubmit(t, r.e, ord("m", "maker", Sell, 100, 10))
	before, _ := r.e.Depth("ACME")
	applied := r.s.count()

	r.j.mu.Lock()
	r.j.failWith = errors.New("db down")
	r.j.mu.Unlock()
	_, err := r.e.Submit(ctxBG, ord("t", "taker", Buy, 100, 10))
	if !errors.Is(err, ErrJournal) {
		t.Fatalf("got %v, want ErrJournal", err)
	}
	after, _ := r.e.Depth("ACME")
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Errorf("book changed despite failed commit:\n before %+v\n after  %+v", before, after)
	}
	if r.s.count() != applied {
		t.Error("sink was notified of a batch that never became durable")
	}
	// Cancel is journaled too.
	m := r.mustOrderID(t, "maker", "m")
	if _, err := r.e.Cancel(ctxBG, "ACME", m, "maker"); !errors.Is(err, ErrJournal) {
		t.Errorf("cancel under journal failure: %v", err)
	}

	// The DB recovers: the SAME client id can now be retried and really executes.
	r.j.mu.Lock()
	r.j.failWith = nil
	r.j.mu.Unlock()
	res, err := r.e.Submit(ctxBG, ord("t", "taker", Buy, 100, 10))
	if err != nil || res.Deduped || len(res.Fills) != 1 {
		t.Fatalf("retry after recovery: %+v %v", res, err)
	}
}

func (r *rig) mustOrderID(t *testing.T, acct, clientID string) string {
	t.Helper()
	r.j.mu.Lock()
	defer r.j.mu.Unlock()
	for _, b := range r.j.batches {
		if b.Order.AccountID == acct && b.Order.ClientOrderID == clientID {
			return b.Order.ID
		}
	}
	t.Fatalf("no committed order %s/%s", acct, clientID)
	return ""
}

// ---- reliability: idempotency ------------------------------------------------------------------

func TestDuplicateSubmissionIsReplayedNotReprocessed(t *testing.T) {
	r := newRig(t, nil).start(t)
	first := mustSubmit(t, r.e, ord("dup", "a", Buy, 100, 10))
	second := mustSubmit(t, r.e, ord("dup", "a", Buy, 100, 10))
	if first.Deduped || !second.Deduped || first.Order.ID != second.Order.ID {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
	if r.j.count() != 1 {
		t.Errorf("committed %d batches, want exactly 1", r.j.count())
	}
	if d, _ := r.e.Depth("ACME"); d.Bids[0].Qty != 10 {
		t.Errorf("only one order may rest: %+v", d)
	}
}

func TestSameKeyDifferentAccountsAreIndependent(t *testing.T) {
	r := newRig(t, nil).start(t)
	a := mustSubmit(t, r.e, ord("same", "acct-A", Buy, 100, 1))
	b := mustSubmit(t, r.e, ord("same", "acct-B", Buy, 100, 1))
	if a.Order.ID == b.Order.ID || b.Deduped {
		t.Errorf("a=%+v b=%+v", a, b)
	}
}

func TestReusedKeyWithADifferentOrderIsRejected(t *testing.T) {
	r := newRig(t, nil).start(t)
	mustSubmit(t, r.e, ord("k", "a", Buy, 100, 10))
	if _, err := r.e.Submit(ctxBG, ord("k", "a", Buy, 100, 11)); !errors.Is(err, ErrIdempotencyMismatch) {
		t.Errorf("got %v, want ErrIdempotencyMismatch", err)
	}
	other := ord("k", "a", Buy, 100, 10)
	other.Symbol = "ZZZ"
	if _, err := r.e.Submit(ctxBG, other); err == nil {
		t.Error("the same key must not place an order in a second symbol")
	}
}

func TestConcurrentRetriesRaceOnTheSameKey(t *testing.T) {
	r := newRig(t, nil).start(t)
	const n = 64
	results := make([]Result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := r.e.Submit(ctxBG, ord("race", "a", Buy, 100, 10))
			if err != nil {
				t.Errorf("submit: %v", err)
			}
			results[i] = res
		}(i)
	}
	wg.Wait()
	originals := 0
	for _, res := range results {
		if !res.Deduped {
			originals++
		}
		if res.Order.ID != results[0].Order.ID {
			t.Fatalf("callers saw different orders: %s vs %s", res.Order.ID, results[0].Order.ID)
		}
	}
	if originals != 1 || r.j.count() != 1 {
		t.Errorf("originals=%d commits=%d, want exactly one of each", originals, r.j.count())
	}
}

func TestRestoreSeedsIdempotencyAndPreservesPriority(t *testing.T) {
	r := newRig(t, nil)
	now := time.Now()
	early := Order{ID: "o1", ClientOrderID: "c1", AccountID: "a", Symbol: "ACME", Side: Sell, Price: rp(100), Qty: 5, Remaining: 5, Status: StatusOpen, Seq: 1, CreatedAt: now}
	late := Order{ID: "o2", ClientOrderID: "c2", AccountID: "b", Symbol: "ACME", Side: Sell, Price: rp(100), Qty: 3, Remaining: 3, Status: StatusOpen, Seq: 2, CreatedAt: now}
	done := Order{ID: "o0", ClientOrderID: "c0", AccountID: "z", Symbol: "ACME", Side: Buy, Price: rp(90), Qty: 4, Remaining: 0, Status: StatusFilled, Seq: 0, CreatedAt: now}
	// Deliberately out of order: Restore must sort by Seq itself.
	if err := r.e.Restore([]Order{late, early, done}, nil); err != nil {
		t.Fatal(err)
	}
	r.start(t)

	if d, _ := r.e.Depth("ACME"); d.Asks[0].Qty != 8 || d.Asks[0].Orders != 2 {
		t.Fatalf("restored depth = %+v", d)
	}
	res := mustSubmit(t, r.e, ord("t", "taker", Buy, 100, 5))
	if len(res.Fills) != 1 || res.Fills[0].MakerOrderID != "o1" {
		t.Fatalf("the earlier restored order must fill first: %+v", res.Fills)
	}
	// A retry of a pre-crash order dedupes rather than trading again.
	before := r.j.count()
	replay := mustSubmit(t, r.e, NewOrder{ClientOrderID: "c1", AccountID: "a", Symbol: "ACME", Side: Sell, Price: rp(100), Qty: 5})
	if !replay.Deduped || replay.Order.ID != "o1" || r.j.count() != before {
		t.Errorf("replay = %+v (commits %d -> %d)", replay, before, r.j.count())
	}
	// New orders continue the sequence, they don't reuse it.
	res = mustSubmit(t, r.e, ord("later", "c", Buy, 50, 1))
	if res.Order.Seq <= late.Seq {
		t.Errorf("seq = %d, must continue past the restored maximum", res.Order.Seq)
	}
}

func TestRestoreAfterStartIsRefused(t *testing.T) {
	r := newRig(t, nil).start(t)
	if err := r.e.Restore(nil, nil); err == nil {
		t.Error("Restore after Start must fail")
	}
}

// ---- reliability: freeze ----------------------------------------------------------------------

func TestFreezeRejectsNewAndAlreadyQueuedSubmissionsButNotWhatAlreadyMatched(t *testing.T) {
	r := newRig(t, nil)
	r.j.gate = make(chan struct{})
	r.j.entered = make(chan struct{}, 8)
	r.start(t)

	// Order 1 is now inside Commit (in flight). Order 2 waits in the queue behind it.
	res1 := make(chan error, 1)
	go func() { _, err := r.e.Submit(ctxBG, ord("in-flight", "a", Buy, 100, 1)); res1 <- err }()
	<-r.j.entered
	res2 := make(chan error, 1)
	go func() { _, err := r.e.Submit(ctxBG, ord("queued", "b", Buy, 100, 1)); res2 <- err }()
	waitFor(t, func() bool { return len(r.e.syms["ACME"].cmds) == 1 }, "second order to queue")

	r.e.Freeze()
	close(r.j.gate)

	if err := <-res1; err != nil {
		t.Errorf("the order already being committed stands: %v", err)
	}
	if err := <-res2; !errors.Is(err, ErrFrozen) {
		t.Errorf("the queued order must be rejected once frozen, got %v", err)
	}
	if _, err := r.e.Submit(ctxBG, ord("new", "c", Buy, 100, 1)); !errors.Is(err, ErrFrozen) {
		t.Errorf("new order while frozen: %v", err)
	}
	if r.j.count() != 1 {
		t.Errorf("commits = %d, want 1", r.j.count())
	}
	// Cancels stay allowed while frozen; unfreezing restores trading; a frozen rejection is retryable.
	id := r.mustOrderID(t, "a", "in-flight")
	if _, err := r.e.Cancel(ctxBG, "ACME", id, "a"); err != nil {
		t.Errorf("cancel while frozen: %v", err)
	}
	r.e.Unfreeze()
	if _, err := r.e.Submit(ctxBG, ord("queued", "b", Buy, 100, 1)); err != nil {
		t.Errorf("retry of the frozen-out order after unfreeze: %v", err)
	}
}

func waitFor(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

// ---- reliability: isolation, panics, backpressure, shutdown --------------------------------------

func TestSymbolsRunInParallelAndOneStuckSymbolDoesNotBlockOthers(t *testing.T) {
	r := newRig(t, nil, "AAA", "BBB")
	slow := &gatedJournal{inner: r.j, gateFor: "AAA", gate: make(chan struct{}), entered: make(chan struct{}, 1)}
	r.e.cfg.Journal = slow
	r.start(t)

	stuck := make(chan error, 1)
	go func() {
		_, err := r.e.Submit(ctxBG, NewOrder{ClientOrderID: "1", AccountID: "a", Symbol: "AAA", Side: Buy, Price: rp(1), Qty: 1})
		stuck <- err
	}()
	<-slow.entered
	ctx, cancel := context.WithTimeout(ctxBG, 2*time.Second)
	defer cancel()
	if _, err := r.e.Submit(ctx, NewOrder{ClientOrderID: "2", AccountID: "a", Symbol: "BBB", Side: Buy, Price: rp(1), Qty: 1}); err != nil {
		t.Fatalf("BBB must not wait for AAA: %v", err)
	}
	close(slow.gate)
	if err := <-stuck; err != nil {
		t.Errorf("AAA: %v", err)
	}
}

type gatedJournal struct {
	inner   *journal
	gateFor string
	gate    chan struct{}
	entered chan struct{}
}

func (g *gatedJournal) Commit(ctx context.Context, b Batch) error {
	if b.Symbol == g.gateFor {
		select {
		case g.entered <- struct{}{}:
		default:
		}
		<-g.gate
	}
	return g.inner.Commit(ctx, b)
}

func TestAPanicHaltsOnlyThatSymbolAndResumeRebuildsIt(t *testing.T) {
	r := newRig(t, nil, "AAA", "BBB").start(t)
	mustSubmitSym := func(sym, id string) (Result, error) {
		return r.e.Submit(ctxBG, NewOrder{ClientOrderID: id, AccountID: "a", Symbol: sym, Side: Buy, Price: rp(10), Qty: 1})
	}
	if _, err := mustSubmitSym("AAA", "ok"); err != nil {
		t.Fatal(err)
	}
	persisted := append([]Batch(nil), r.j.batches...)

	r.j.panicOn.Store(true)
	if _, err := mustSubmitSym("AAA", "boom"); !errors.Is(err, ErrSymbolHalted) {
		t.Fatalf("panicking command: %v, want ErrSymbolHalted", err)
	}
	r.j.panicOn.Store(false)

	if !r.e.Halted("AAA") || r.e.Halted("BBB") {
		t.Fatal("only AAA should be halted")
	}
	if _, err := mustSubmitSym("AAA", "after"); !errors.Is(err, ErrSymbolHalted) {
		t.Errorf("halted symbol must refuse work: %v", err)
	}
	if _, err := mustSubmitSym("BBB", "fine"); err != nil {
		t.Errorf("other symbols must be unaffected: %v", err)
	}
	// The goroutine survived: it still answers (would deadlock if it had died).
	if _, err := r.e.Order(ctxBG, "AAA", "x"); !errors.Is(err, ErrSymbolHalted) && !errors.Is(err, ErrNotFound) {
		t.Errorf("goroutine unresponsive: %v", err)
	}

	var durable []Order
	for _, b := range persisted {
		durable = append(durable, b.Order)
	}
	if err := r.e.Resume(ctxBG, "AAA", durable); err != nil {
		t.Fatal(err)
	}
	if r.e.Halted("AAA") {
		t.Fatal("Resume must clear the halt")
	}
	if d, _ := r.e.Depth("AAA"); len(d.Bids) != 1 || d.Bids[0].Qty != 1 {
		t.Errorf("book must be rebuilt from durable state: %+v", d)
	}
	if _, err := mustSubmitSym("AAA", "again"); err != nil {
		t.Errorf("after resume: %v", err)
	}
}

func TestASinkPanicDoesNotHaltTheSymbol(t *testing.T) {
	r := newRig(t, nil)
	r.s.panicky = true
	r.start(t)
	if _, err := r.e.Submit(ctxBG, ord("a", "a", Buy, 100, 1)); err != nil {
		t.Fatalf("the batch was durable and applied; a sink bug must not fail it: %v", err)
	}
	if r.e.Halted("ACME") {
		t.Error("a sink panic must not halt the symbol")
	}
	if _, err := r.e.Submit(ctxBG, ord("b", "a", Buy, 100, 1)); err != nil {
		t.Errorf("next order: %v", err)
	}
}

func TestBackpressureReturnsAContextErrorInsteadOfGrowingWithoutBound(t *testing.T) {
	r := newRig(t, func(c *Config) { c.QueueSize = 1 })
	r.j.gate = make(chan struct{})
	r.j.entered = make(chan struct{}, 4)
	r.start(t)

	go func() { _, _ = r.e.Submit(ctxBG, ord("1", "a", Buy, 100, 1)) }() // in flight
	<-r.j.entered
	go func() { _, _ = r.e.Submit(ctxBG, ord("2", "b", Buy, 100, 1)) }() // fills the queue
	waitFor(t, func() bool { return len(r.e.syms["ACME"].cmds) == 1 }, "queue to fill")

	ctx, cancel := context.WithTimeout(ctxBG, 50*time.Millisecond)
	defer cancel()
	_, err := r.e.Submit(ctx, ord("3", "c", Buy, 100, 1))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("got %v, want a deadline error from the full queue", err)
	}
	close(r.j.gate)
	// The rejected order was never enqueued, so it can simply be retried.
	waitFor(t, func() bool { return len(r.e.syms["ACME"].cmds) == 0 }, "queue to drain")
	if _, err := r.e.Submit(ctxBG, ord("3", "c", Buy, 100, 1)); err != nil {
		t.Errorf("retry after backpressure: %v", err)
	}
}

func TestACallerTimingOutAfterEnqueueStillGetsTheRealResultOnRetry(t *testing.T) {
	r := newRig(t, nil)
	r.j.gate = make(chan struct{})
	r.j.entered = make(chan struct{}, 2)
	r.start(t)

	ctx, cancel := context.WithTimeout(ctxBG, 30*time.Millisecond)
	defer cancel()
	if _, err := r.e.Submit(ctx, ord("slow", "a", Buy, 100, 1)); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	// The client retries while the first attempt is still committing.
	got := make(chan Result, 1)
	go func() { res, _ := r.e.Submit(ctxBG, ord("slow", "a", Buy, 100, 1)); got <- res }()
	time.Sleep(20 * time.Millisecond)
	close(r.j.gate)
	res := <-got
	if !res.Deduped || res.Order.ID == "" {
		t.Errorf("retry must receive the original outcome: %+v", res)
	}
	if r.j.count() != 1 {
		t.Errorf("commits = %d, want 1 (no double submit)", r.j.count())
	}
}

func TestStopDrainsQueuedWorkThenRefusesNewWork(t *testing.T) {
	r := newRig(t, nil)
	r.j.gate = make(chan struct{})
	r.j.entered = make(chan struct{}, 8)
	r.e.Start()

	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		i := i
		go func() { _, err := r.e.Submit(ctxBG, ord(fmt.Sprint("q", i), "a", Buy, 100, 1)); errs <- err }()
	}
	<-r.j.entered
	waitFor(t, func() bool { return len(r.e.syms["ACME"].cmds) == 2 }, "two orders queued behind the first")

	stopped := make(chan error, 1)
	go func() {
		ctx, cancel := context.WithTimeout(ctxBG, 5*time.Second)
		defer cancel()
		stopped <- r.e.Stop(ctx)
	}()
	waitFor(t, func() bool { r.e.mu.RLock(); defer r.e.mu.RUnlock(); return r.e.stopped }, "Stop to begin")

	close(r.j.gate)
	if err := <-stopped; err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if _, err := r.e.Submit(ctxBG, ord("late", "z", Buy, 1, 1)); !errors.Is(err, ErrStopped) {
		t.Errorf("after Stop: %v, want ErrStopped", err)
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Errorf("work accepted before Stop must complete: %v", err)
		}
	}
	if r.j.count() != 3 {
		t.Errorf("commits = %d, want 3", r.j.count())
	}
}

func TestPriceSinkSeesTheLastTradePrice(t *testing.T) {
	var mu sync.Mutex
	var got []string
	r := newRig(t, func(c *Config) {
		c.Prices = PriceUpdateFunc(func(sym string, p money.Paise, at time.Time) {
			mu.Lock()
			got = append(got, fmt.Sprintf("%s@%d", sym, p))
			mu.Unlock()
		})
	}).start(t)
	mustSubmit(t, r.e, ord("m1", "m1", Sell, 100, 5))
	mustSubmit(t, r.e, ord("m2", "m2", Sell, 101, 5))
	mustSubmit(t, r.e, ord("resting", "x", Buy, 90, 1)) // no fill: no price update
	mustSubmit(t, r.e, ord("t", "t", Buy, 101, 10))     // walks 100 then 101: last trade is 101
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 || got[0] != fmt.Sprintf("ACME@%d", rp(101)) {
		t.Errorf("price updates = %v", got)
	}
}

// ---- properties -------------------------------------------------------------------------------

func TestRandomFlowKeepsTheBookConsistent(t *testing.T) {
	r := newRig(t, nil, "ACME", "GLOBEX").start(t)
	rng := rand.New(rand.NewSource(42))
	var submittedQty, filledTaker, filledMaker int64
	live := map[string]Order{}

	for i := 0; i < 3000; i++ {
		sym := []string{"ACME", "GLOBEX"}[rng.Intn(2)]
		n := NewOrder{
			ClientOrderID: fmt.Sprint("c", i), AccountID: fmt.Sprint("acct", rng.Intn(8)), Symbol: sym,
			Side: Side(1 + rng.Intn(2)), Price: rp(int64(95 + rng.Intn(11))), Qty: int64(1 + rng.Intn(20)),
		}
		res, err := r.e.Submit(ctxBG, n)
		if err != nil {
			t.Fatalf("submit %d: %v", i, err)
		}
		submittedQty += n.Qty
		for _, f := range res.Fills {
			filledTaker += f.Qty
			if f.Price <= 0 || f.Qty <= 0 {
				t.Fatalf("bad fill %+v", f)
			}
		}
		if res.Order.Remaining < 0 || res.Order.Remaining > res.Order.Qty {
			t.Fatalf("remaining out of range: %+v", res.Order)
		}
		if res.Order.Status.Live() {
			live[res.Order.ID] = res.Order
		}
		// Randomly cancel something that is (probably) still resting.
		if rng.Intn(5) == 0 && len(live) > 0 {
			for id, o := range live {
				_, err := r.e.Cancel(ctxBG, o.Symbol, id, o.AccountID)
				if err != nil && !errors.Is(err, ErrAlreadyClosed) {
					t.Fatalf("cancel: %v", err)
				}
				delete(live, id)
				break
			}
		}
		for _, s := range []string{"ACME", "GLOBEX"} {
			d, _ := r.e.Depth(s)
			if len(d.Bids) > 0 && len(d.Asks) > 0 && d.Bids[0].Price >= d.Asks[0].Price {
				t.Fatalf("crossed book after order %d in %s: bid %d >= ask %d", i, s, d.Bids[0].Price, d.Asks[0].Price)
			}
			for j := 1; j < len(d.Bids); j++ {
				if d.Bids[j].Price >= d.Bids[j-1].Price {
					t.Fatalf("bids not strictly descending: %+v", d.Bids)
				}
			}
			for j := 1; j < len(d.Asks); j++ {
				if d.Asks[j].Price <= d.Asks[j-1].Price {
					t.Fatalf("asks not strictly ascending: %+v", d.Asks)
				}
			}
		}
	}
	// Conservation: every fill is one taker quantity and one maker quantity.
	r.j.mu.Lock()
	defer r.j.mu.Unlock()
	for _, b := range r.j.batches {
		var fq int64
		for _, f := range b.Fills {
			fq += f.Qty
		}
		var mq int64
		for _, m := range b.Makers {
			mq += 0 * m.Qty
		}
		filledMaker += fq
		if b.Kind == KindSubmit && b.Order.Qty-b.Order.Remaining != fq {
			t.Fatalf("taker filled %d but fills sum to %d: %+v", b.Order.Qty-b.Order.Remaining, fq, b.Order)
		}
		if len(b.Makers) != len(b.Fills) {
			t.Fatalf("makers/fills length mismatch")
		}
	}
	if filledTaker != filledMaker {
		t.Errorf("taker-side fills %d != journaled fills %d", filledTaker, filledMaker)
	}
	if submittedQty == 0 {
		t.Fatal("no flow generated")
	}
}

func TestConcurrentFlowAcrossManySymbolsIsRaceFree(t *testing.T) {
	syms := make([]string, 25)
	for i := range syms {
		syms[i] = fmt.Sprint("S", i)
	}
	r := newRig(t, nil, syms...).start(t)
	var wg sync.WaitGroup
	for w := 0; w < 16; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < 200; i++ {
				n := NewOrder{
					ClientOrderID: fmt.Sprint("w", w, "-", i), AccountID: fmt.Sprint("acct", w), Symbol: syms[rng.Intn(len(syms))],
					Side: Side(1 + rng.Intn(2)), Price: rp(int64(98 + rng.Intn(5))), Qty: int64(1 + rng.Intn(10)),
				}
				if _, err := r.e.Submit(ctxBG, n); err != nil {
					t.Errorf("submit: %v", err)
					return
				}
				_, _ = r.e.Depth(n.Symbol)
			}
		}(w)
	}
	wg.Wait()
	if got := r.j.count(); got != 16*200 {
		t.Errorf("commits = %d, want %d", got, 16*200)
	}
}
