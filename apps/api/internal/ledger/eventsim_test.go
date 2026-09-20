package ledger

// A system-level property test that lives here (not in test/integration) only because it needs this
// package's test-only inventory seeding (grant): how real liquidity is seeded is an open decision.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stockastic/api/internal/engine"
	"stockastic/api/internal/money"
	"stockastic/api/internal/ratelimit"
)

// simJournal is a durable log with injected transient failures, like a database having a bad moment.
type simJournal struct {
	mu       sync.Mutex
	rng      *rand.Rand
	failPct  int
	batches  []engine.Batch
	failures int
}

func (j *simJournal) Commit(_ context.Context, b engine.Batch) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.rng.Intn(100) < j.failPct {
		j.failures++
		return errors.New("transient database error")
	}
	j.batches = append(j.batches, b)
	return nil
}

type seedGrant struct {
	account, symbol string
	qty             int64
}

type simStats struct {
	placed, rejectedCash, rejectedShares, rateLimited, frozenOut, journalFailed, deduped, cancels atomic.Int64
}

func TestSimulationConservesMoneyAndSharesAndRebuildsExactlyFromTheJournal(t *testing.T) {
	const nSyms, nAccts, workers = 250, 120, 12
	opsPerWorker := 5000
	if testing.Short() {
		opsPerWorker = 200
	}
	startCash := money.FromRupees(1_000_000)

	syms := make([]string, nSyms)
	for i := range syms {
		syms[i] = fmt.Sprintf("S%03d", i)
	}
	accts := make([]string, nAccts)
	for i := range accts {
		accts[i] = fmt.Sprintf("acct-%03d", i)
	}

	// Opening state: identical cash for everyone, plus scattered opening inventory.
	seedRng := rand.New(rand.NewSource(1))
	var grants []seedGrant
	initialShares := map[string]int64{}
	for _, a := range accts {
		for k := 0; k < 20; k++ {
			g := seedGrant{account: a, symbol: syms[seedRng.Intn(40)], qty: int64(10 + seedRng.Intn(50))} // concentrated in 40 hot symbols
			grants = append(grants, g)
			initialShares[g.symbol] += g.qty
		}
	}
	openLedger := func() *Ledger {
		l := New(quiet)
		for _, a := range accts {
			if err := l.Open(Account{ID: a, Name: a, Kind: KindTeam}, startCash); err != nil {
				t.Fatal(err)
			}
		}
		for _, g := range grants {
			l.mu.Lock()
			p := l.accts[g.account].pos[g.symbol]
			if p == nil {
				p = &Position{Symbol: g.symbol}
				l.accts[g.account].pos[g.symbol] = p
			}
			p.Qty += g.qty
			p.Cost += money.Paise(g.qty) * money.FromRupees(100)
			l.mu.Unlock()
		}
		return l
	}

	l := openLedger()
	j := &simJournal{rng: rand.New(rand.NewSource(2)), failPct: 3}
	e, err := engine.New(engine.Config{Symbols: syms, Journal: j, Sink: l, Log: quiet, QueueSize: 64, DepthLevels: 1000})
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	// Modest limit so the rate-limited path (and its refund) is genuinely exercised.
	limiter := ratelimit.New(40, time.Minute)
	var st simStats
	ctx := context.Background()

	// place is exactly the sequence the API layer will run: reserve -> rate limit -> submit -> clean up.
	place := func(n engine.NewOrder) (engine.Result, error) {
		created, err := l.Reserve(n)
		switch {
		case errors.Is(err, ErrInsufficientCash):
			st.rejectedCash.Add(1)
			return engine.Result{}, err
		case errors.Is(err, ErrInsufficientShares):
			st.rejectedShares.Add(1)
			return engine.Result{}, err
		case err != nil:
			t.Errorf("reserve: %v", err)
			return engine.Result{}, err
		}
		release := func() {
			if created {
				l.Release(n.AccountID, n.ClientOrderID)
			}
		}
		if ok, _ := limiter.Allow(n.AccountID, time.Now()); !ok {
			st.rateLimited.Add(1)
			release()
			return engine.Result{}, errors.New("rate_limited")
		}
		res, err := e.Submit(ctx, n)
		switch {
		case errors.Is(err, engine.ErrFrozen):
			st.frozenOut.Add(1)
			release()
			limiter.Refund(n.AccountID)
		case errors.Is(err, engine.ErrJournal):
			st.journalFailed.Add(1)
			release()
			limiter.Refund(n.AccountID)
		case err != nil:
			t.Errorf("submit: %v", err)
			release()
		case res.Deduped:
			st.deduped.Add(1)
			release() // a replay of an order that already finished: nothing new is live
		default:
			st.placed.Add(1)
		}
		return res, err
	}

	// Chaos: the organiser force-freezes and unfreezes at random while everything is running.
	stopChaos := make(chan struct{})
	var chaosWG sync.WaitGroup
	chaosWG.Add(1)
	go func() {
		defer chaosWG.Done()
		r := rand.New(rand.NewSource(99))
		for {
			select {
			case <-stopChaos:
				return
			case <-time.After(time.Duration(5+r.Intn(20)) * time.Millisecond):
				e.Freeze()
				time.Sleep(time.Duration(1+r.Intn(4)) * time.Millisecond)
				e.Unfreeze()
			}
		}
	}()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			r := rand.New(rand.NewSource(int64(1000 + w)))
			var sent []engine.NewOrder
			var live []engine.Order
			for i := 0; i < opsPerWorker; i++ {
				switch x := r.Intn(100); {
				case x < 72: // a new order
					sym := syms[r.Intn(40)] // hot symbols so books actually cross
					if r.Intn(10) == 0 {
						sym = syms[r.Intn(nSyms)]
					}
					n := engine.NewOrder{
						ClientOrderID: fmt.Sprintf("w%d-%d", w, i), AccountID: accts[r.Intn(nAccts)], Symbol: sym,
						Side: engine.Side(1 + r.Intn(2)), Price: money.Paise(9800 + r.Intn(401)), Qty: int64(1 + r.Intn(10)),
					}
					if res, err := place(n); err == nil {
						sent = append(sent, n)
						if res.Order.Status.Live() {
							live = append(live, res.Order)
						}
					}
				case x < 84 && len(sent) > 0: // a client retries something it already sent
					_, _ = place(sent[r.Intn(len(sent))])
				case len(live) > 0: // cancel one of our resting orders
					k := r.Intn(len(live))
					o := live[k]
					live = append(live[:k], live[k+1:]...)
					if _, err := e.Cancel(ctx, o.Symbol, o.ID, o.AccountID); err == nil {
						st.cancels.Add(1)
					}
				}
			}
		}(w)
	}
	wg.Wait()
	close(stopChaos)
	chaosWG.Wait()
	e.Unfreeze()
	stopCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := e.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}

	j.mu.Lock()
	batches := append([]engine.Batch(nil), j.batches...)
	failures := j.failures
	j.mu.Unlock()

	// Derive durable truth from the journal alone.
	var fills []engine.Fill
	latest := map[string]engine.Order{}
	type key struct{ acct, client string }
	seen := map[key]bool{}
	for _, b := range batches {
		fills = append(fills, b.Fills...)
		latest[b.Order.ID] = b.Order
		for _, m := range b.Makers {
			latest[m.ID] = m
		}
		if b.Kind == engine.KindSubmit {
			k := key{b.Order.AccountID, b.Order.ClientOrderID}
			if seen[k] {
				t.Errorf("order %v was committed twice: idempotency failed", k)
			}
			seen[k] = true
		}
	}
	var liveOrders []engine.Order
	for _, o := range latest {
		if o.Status.Live() {
			liveOrders = append(liveOrders, o)
		}
	}
	t.Logf("orders committed=%d fills=%d live=%d | rejected: cash=%d shares=%d rate=%d frozen=%d journal=%d | deduped=%d cancels=%d journalFailures=%d",
		len(seen), len(fills), len(liveOrders), st.rejectedCash.Load(), st.rejectedShares.Load(), st.rateLimited.Load(),
		st.frozenOut.Load(), st.journalFailed.Load(), st.deduped.Load(), st.cancels.Load(), failures)

	// The run must actually have exercised the hard paths, otherwise a pass proves nothing.
	if !testing.Short() {
		for name, got := range map[string]int64{
			"fills": int64(len(fills)), "journal failures": int64(failures), "dedupes": st.deduped.Load(),
			"cancels": st.cancels.Load(), "rate-limit hits": st.rateLimited.Load(), "frozen rejections": st.frozenOut.Load(),
			"share rejections": st.rejectedShares.Load(),
		} {
			if got == 0 {
				t.Errorf("the simulation never exercised %q", name)
			}
		}
	}

	// 1. Conservation: no cash or shares were created or destroyed.
	var totalCash money.Paise
	shares := map[string]int64{}
	for _, a := range accts {
		s, err := l.Snapshot(a)
		if err != nil {
			t.Fatal(err)
		}
		totalCash += s.Cash
		if s.Cash < 0 || s.ReservedCash < 0 || s.AvailableCash < 0 {
			t.Errorf("%s has negative money: %+v", a, s)
		}
		for _, p := range s.Positions {
			if p.Qty < 0 {
				t.Errorf("%s is short %d %s", a, -p.Qty, p.Symbol)
			}
			shares[p.Symbol] += p.Qty
		}
	}
	if want := money.Paise(nAccts) * startCash; totalCash != want {
		t.Errorf("total cash %d != %d: money was created or destroyed (diff %d)", totalCash, want, totalCash-want)
	}
	for sym, want := range initialShares {
		if shares[sym] != want {
			t.Errorf("%s: %d shares in the system, started with %d", sym, shares[sym], want)
		}
	}
	if l.Anomalies() != 0 {
		t.Errorf("ledger recorded %d integrity anomalies", l.Anomalies())
	}

	// 2. The book in memory equals the durable record: per symbol, resting quantity matches the journal.
	wantBid, wantAsk := map[string]int64{}, map[string]int64{}
	for _, o := range liveOrders {
		if o.Side == engine.Buy {
			wantBid[o.Symbol] += o.Remaining
		} else {
			wantAsk[o.Symbol] += o.Remaining
		}
	}
	// Engine is stopped but Depth reads the last published snapshot.
	for _, sym := range syms {
		d, _ := e.Depth(sym)
		var b, a int64
		for _, lv := range d.Bids {
			b += lv.Qty
		}
		for _, lv := range d.Asks {
			a += lv.Qty
		}
		if b != wantBid[sym] || a != wantAsk[sym] {
			t.Errorf("%s: book has bid=%d ask=%d, journal says bid=%d ask=%d", sym, b, a, wantBid[sym], wantAsk[sym])
		}
		if len(d.Bids) > 0 && len(d.Asks) > 0 && d.Bids[0].Price >= d.Asks[0].Price {
			t.Errorf("%s: crossed book %d >= %d", sym, d.Bids[0].Price, d.Asks[0].Price)
		}
	}

	// 3. Crash recovery: a ledger rebuilt purely from the journal is identical to the live one.
	fresh := openLedger()
	fresh.Restore(fills, liveOrders)
	mismatches := 0
	for _, a := range accts {
		live, _ := l.Snapshot(a)
		rebuilt, _ := fresh.Snapshot(a)
		if fmt.Sprintf("%+v", live) != fmt.Sprintf("%+v", rebuilt) {
			if mismatches++; mismatches <= 3 {
				t.Errorf("%s differs after rebuild:\n live    %+v\n rebuilt %+v", a, live, rebuilt)
			}
		}
	}
	if mismatches > 0 {
		t.Errorf("%d of %d accounts differ after rebuilding from the journal", mismatches, nAccts)
	}
}
