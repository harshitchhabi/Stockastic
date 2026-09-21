package trading_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"stockastic/api/internal/ledger"
	"stockastic/api/internal/money"
	"stockastic/api/internal/trading"
)

type prices struct {
	mu sync.Mutex
	m  map[string]money.Paise
}

func (p *prices) Price(s string) (money.Paise, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	v, ok := p.m[s]
	return v, ok
}
func (p *prices) set(s string, v money.Paise) { p.mu.Lock(); p.m[s] = v; p.mu.Unlock() }

type journal struct {
	mu     sync.Mutex
	trades []trading.Trade
	fail   error
}

func (j *journal) Commit(_ context.Context, t trading.Trade) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.fail != nil {
		return j.fail
	}
	j.trades = append(j.trades, t)
	return nil
}
func (j *journal) count() int { j.mu.Lock(); defer j.mu.Unlock(); return len(j.trades) }

type env struct {
	l  *ledger.Ledger
	p  *prices
	j  *journal
	ex *trading.Executor
}

func setup(t *testing.T, accounts ...string) *env {
	t.Helper()
	l := ledger.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	for _, a := range accounts {
		if err := l.Open(ledger.Account{ID: a, Name: a, Kind: ledger.KindTeam}, 1_000_000_00); err != nil {
			t.Fatal(err)
		}
	}
	p := &prices{m: map[string]money.Paise{"ACME": 10050, "GLOBEX": 25000}}
	j := &journal{}
	return &env{l: l, p: p, j: j, ex: trading.New(l, p, j, nil)}
}

func req(account, cid, sym string, side trading.Side, qty int64) trading.Request {
	return trading.Request{ClientTradeID: cid, AccountID: account, Symbol: sym, Side: side, Qty: qty}
}

func TestATradeHappensAtTheCurrentPrice(t *testing.T) {
	e := setup(t, "a")
	res, err := e.ex.Execute(context.Background(), req("a", "t1", "ACME", trading.Buy, 10))
	if err != nil {
		t.Fatal(err)
	}
	if res.Trade.Price != 10050 || res.Trade.Qty != 10 || res.Deduped {
		t.Fatalf("trade = %+v", res)
	}
	s, _ := e.l.Snapshot("a")
	if s.Cash != 1_000_000_00-10*10050 || s.Positions[0].Qty != 10 {
		t.Fatalf("account = %+v", s)
	}
	// The price moves; the next trade uses the new price, the old trade is unchanged.
	e.p.set("ACME", 11000)
	res, _ = e.ex.Execute(context.Background(), req("a", "t2", "ACME", trading.Sell, 4))
	if res.Trade.Price != 11000 {
		t.Fatalf("second trade price = %d", res.Trade.Price)
	}
}

func TestARetryReturnsTheOriginalTradeAndNeverTradesTwice(t *testing.T) {
	e := setup(t, "a")
	first, err := e.ex.Execute(context.Background(), req("a", "same", "ACME", trading.Buy, 5))
	if err != nil {
		t.Fatal(err)
	}
	e.p.set("ACME", 99999) // even if the price has moved, a retry is the original trade
	for i := 0; i < 5; i++ {
		again, err := e.ex.Execute(context.Background(), req("a", "same", "ACME", trading.Buy, 5))
		if err != nil || !again.Deduped || again.Trade.ID != first.Trade.ID || again.Trade.Price != 10050 {
			t.Fatalf("retry %d: %+v %v", i, again, err)
		}
	}
	if e.j.count() != 1 {
		t.Fatalf("%d trades were saved for one instruction", e.j.count())
	}
	if _, err := e.ex.Execute(context.Background(), req("a", "same", "ACME", trading.Buy, 6)); !errors.Is(err, trading.ErrIdempotencyMismatch) {
		t.Fatalf("the same id for a different trade: %v", err)
	}
	// Another account may use the same client id.
	e2 := setup(t, "a", "b")
	_, _ = e2.ex.Execute(context.Background(), req("a", "x", "ACME", trading.Buy, 1))
	if r, err := e2.ex.Execute(context.Background(), req("b", "x", "ACME", trading.Buy, 1)); err != nil || r.Deduped {
		t.Fatalf("a different account with the same client id: %+v %v", r, err)
	}
}

func TestConcurrentDuplicatesProduceExactlyOneTrade(t *testing.T) {
	e := setup(t, "a")
	var wg sync.WaitGroup
	var fresh atomic.Int64
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := e.ex.Execute(context.Background(), req("a", "one", "ACME", trading.Buy, 3))
			if err != nil {
				t.Error(err)
				return
			}
			if !r.Deduped {
				fresh.Add(1)
			}
		}()
	}
	wg.Wait()
	if fresh.Load() != 1 || e.j.count() != 1 {
		t.Fatalf("%d new trades and %d saved, want 1 and 1", fresh.Load(), e.j.count())
	}
	if s, _ := e.l.Snapshot("a"); s.Positions[0].Qty != 3 {
		t.Fatalf("holding = %d", s.Positions[0].Qty)
	}
}

func TestAnExpectedPriceIsHonoured(t *testing.T) {
	e := setup(t, "a")
	r := req("a", "t1", "ACME", trading.Buy, 1)
	r.ExpectedPrice = 10000 // the price has moved to 10050
	_, err := e.ex.Execute(context.Background(), r)
	var pc *trading.PriceChanged
	if !errors.As(err, &pc) || pc.Current != 10050 {
		t.Fatalf("err = %v, want PriceChanged at 10050", err)
	}
	if e.j.count() != 0 {
		t.Fatal("a trade at a price nobody agreed to was saved")
	}
	r.ExpectedPrice = 10050
	if _, err := e.ex.Execute(context.Background(), r); err != nil {
		t.Fatalf("the matching price: %v", err)
	}
}

func TestAFailedSaveChangesNothingAndCanBeRetried(t *testing.T) {
	e := setup(t, "a")
	e.j.fail = errors.New("disk full")
	_, err := e.ex.Execute(context.Background(), req("a", "t1", "ACME", trading.Buy, 4))
	if !errors.Is(err, trading.ErrJournal) {
		t.Fatalf("err = %v", err)
	}
	if s, _ := e.l.Snapshot("a"); s.Cash != 1_000_000_00 || len(s.Positions) != 0 {
		t.Fatalf("an unsaved trade changed the account: %+v", s)
	}
	if e.ex.Seen("a", "t1") {
		t.Fatal("an unsaved trade was remembered as done")
	}
	e.j.fail = nil
	if r, err := e.ex.Execute(context.Background(), req("a", "t1", "ACME", trading.Buy, 4)); err != nil || r.Deduped {
		t.Fatalf("retry after the disk recovered: %+v %v", r, err)
	}
}

func TestRefusedTrades(t *testing.T) {
	e := setup(t, "a")
	ctx := context.Background()
	cases := map[string]struct {
		r    trading.Request
		want error
	}{
		"unknown company":    {req("a", "1", "NOPE", trading.Buy, 1), trading.ErrUnknownSymbol},
		"not enough cash":    {req("a", "2", "GLOBEX", trading.Buy, 100_000), ledger.ErrInsufficientCash},
		"nothing to sell":    {req("a", "3", "ACME", trading.Sell, 1), ledger.ErrInsufficientShares},
		"zero quantity":      {req("a", "4", "ACME", trading.Buy, 0), trading.ErrInvalid},
		"negative quantity":  {req("a", "5", "ACME", trading.Buy, -3), trading.ErrInvalid},
		"no side":            {trading.Request{ClientTradeID: "6", AccountID: "a", Symbol: "ACME", Qty: 1}, trading.ErrInvalid},
		"no client id":       {req("a", "", "ACME", trading.Buy, 1), trading.ErrInvalid},
		"an unknown account": {req("ghost", "8", "ACME", trading.Buy, 1), ledger.ErrUnknownAccount},
	}
	for name, c := range cases {
		if _, err := e.ex.Execute(ctx, c.r); !errors.Is(err, c.want) {
			t.Errorf("%s: %v, want %v", name, err, c.want)
		}
	}
	if e.j.count() != 0 {
		t.Fatalf("%d refused trades were saved", e.j.count())
	}
	// A refused trade does not use up its client id: the same id can be tried again.
	e.p.set("GLOBEX", 1)
	if _, err := e.ex.Execute(ctx, req("a", "2", "GLOBEX", trading.Buy, 100_000)); err != nil {
		t.Fatalf("retrying a refused id once it is affordable: %v", err)
	}
}

func TestSeededTradesAreRecognisedAfterARestart(t *testing.T) {
	e := setup(t, "a")
	orig := trading.Trade{ID: "old", ClientTradeID: "pre-crash", AccountID: "a", Symbol: "ACME", Side: trading.Buy, Qty: 2, Price: 9999}
	e.ex.Seed(orig)
	r, err := e.ex.Execute(context.Background(), req("a", "pre-crash", "ACME", trading.Buy, 2))
	if err != nil || !r.Deduped || r.Trade.ID != "old" || r.Trade.Price != 9999 {
		t.Fatalf("retry of a pre-crash trade: %+v %v", r, err)
	}
	if e.j.count() != 0 {
		t.Fatal("the retry was saved again")
	}
}

func TestDifferentAccountsTradeInParallelWithoutInterference(t *testing.T) {
	const n = 40
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("acct%d", i)
	}
	e := setup(t, ids...)
	var wg sync.WaitGroup
	for _, id := range ids {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				side := trading.Buy
				if i%2 == 1 {
					side = trading.Sell
				}
				if _, err := e.ex.Execute(context.Background(), req(id, fmt.Sprint(i), "ACME", side, 2)); err != nil {
					t.Error(err)
					return
				}
			}
		}(id)
	}
	wg.Wait()
	if e.j.count() != n*25 {
		t.Fatalf("%d trades saved, want %d", e.j.count(), n*25)
	}
	for _, id := range ids {
		s, _ := e.l.Snapshot(id)
		if s.Positions[0].Qty != 2 { // 13 buys and 12 sells of 2
			t.Fatalf("%s holds %d, want 2", id, s.Positions[0].Qty)
		}
	}
}
