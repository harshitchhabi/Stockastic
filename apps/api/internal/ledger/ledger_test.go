package ledger

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"sync"
	"testing"

	"stockastic/api/internal/money"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func rp(v int64) money.Paise { return money.Paise(v * 100) }

func newLedger(t *testing.T, cash int64, ids ...string) *Ledger {
	t.Helper()
	l := New(quiet)
	for _, id := range ids {
		if err := l.Open(Account{ID: id, Name: id, Kind: KindTeam}, rp(cash)); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func snap(t *testing.T, l *Ledger, id string) Snapshot {
	t.Helper()
	s, err := l.Snapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestBuyAndSellMoveCashAndHoldingsExactly(t *testing.T) {
	l := newLedger(t, 2000, "a")
	if err := l.Trade("a", "ACME", true, 10, 10025, nil); err != nil { // 10 shares at 100.25
		t.Fatal(err)
	}
	s := snap(t, l, "a")
	if s.Cash != rp(2000)-10*10025 || len(s.Positions) != 1 || s.Positions[0].Qty != 10 || s.Positions[0].Cost != 10*10025 {
		t.Fatalf("after buy: %+v", s)
	}
	// Sell 4 at a different price: cash rises by 4 x price, cost leaves at the average.
	if err := l.Trade("a", "ACME", false, 4, 11000, nil); err != nil {
		t.Fatal(err)
	}
	s = snap(t, l, "a")
	if s.Cash != rp(2000)-10*10025+4*11000 || s.Positions[0].Qty != 6 || s.Positions[0].Cost != 6*10025 {
		t.Fatalf("after sell: %+v", s)
	}
	// Selling everything removes the position.
	if err := l.Trade("a", "ACME", false, 6, 10000, nil); err != nil {
		t.Fatal(err)
	}
	if s = snap(t, l, "a"); len(s.Positions) != 0 {
		t.Fatalf("an empty position is still listed: %+v", s.Positions)
	}
}

func TestNobodyCanOverspendOrSellWhatTheyDoNotOwn(t *testing.T) {
	l := newLedger(t, 100, "a")
	if err := l.Trade("a", "ACME", true, 1, rp(100)+1, nil); !errors.Is(err, ErrInsufficientCash) {
		t.Fatalf("a buy one paisa over the balance: %v", err)
	}
	if err := l.Trade("a", "ACME", true, 1, rp(100), nil); err != nil {
		t.Fatalf("spending exactly the balance: %v", err)
	}
	if s := snap(t, l, "a"); s.Cash != 0 {
		t.Fatalf("cash = %d", s.Cash)
	}
	if err := l.Trade("a", "ACME", false, 2, rp(1), nil); !errors.Is(err, ErrInsufficientShares) {
		t.Fatalf("selling more than held: %v", err)
	}
	if err := l.Trade("a", "OTHER", false, 1, rp(1), nil); !errors.Is(err, ErrInsufficientShares) {
		t.Fatalf("short selling: %v", err)
	}
	for name, f := range map[string]func() error{
		"zero quantity":  func() error { return l.Trade("a", "ACME", true, 0, rp(1), nil) },
		"zero price":     func() error { return l.Trade("a", "ACME", true, 1, 0, nil) },
		"unknown acct":   func() error { return l.Trade("nobody", "ACME", true, 1, rp(1), nil) },
		"negative grant": func() error { return l.Grant("a", "ACME", -1, rp(1)) },
	} {
		if err := f(); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestAFailedCommitChangesNothing(t *testing.T) {
	l := newLedger(t, 1000, "a")
	before := snap(t, l, "a")
	boom := errors.New("disk full")
	if err := l.Trade("a", "ACME", true, 3, rp(10), func() error { return boom }); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if after := snap(t, l, "a"); after.Cash != before.Cash || len(after.Positions) != 0 {
		t.Fatalf("a trade that could not be saved changed the account: %+v", after)
	}
	// A refused trade never even calls commit.
	called := false
	_ = l.Trade("a", "ACME", true, 1, rp(5000), func() error { called = true; return nil })
	if called {
		t.Fatal("commit was called for a trade the account could not afford")
	}
}

func TestGrantRevokeAndAdjustCash(t *testing.T) {
	l := newLedger(t, 1000, "a")
	if err := l.Grant("a", "X", 10, rp(50)); err != nil {
		t.Fatal(err)
	}
	if err := l.Grant("a", "X", 10, rp(150)); err != nil { // average cost is now 100
		t.Fatal(err)
	}
	if s := snap(t, l, "a"); s.Positions[0].Qty != 20 || s.Positions[0].Cost != 20*rp(100) {
		t.Fatalf("average cost: %+v", s.Positions)
	}
	if err := l.Revoke("a", "X", 21, false); !errors.Is(err, ErrInsufficientShares) {
		t.Fatalf("revoking more than held: %v", err)
	}
	if err := l.Revoke("a", "X", 5, false); err != nil {
		t.Fatal(err)
	}
	if s := snap(t, l, "a"); s.Positions[0].Qty != 15 || s.Positions[0].Cost != 15*rp(100) {
		t.Fatalf("after revoke: %+v", s.Positions)
	}
	if err := l.AdjustCash("a", -rp(1001), false); !errors.Is(err, ErrInsufficientCash) {
		t.Fatalf("taking cash below zero: %v", err)
	}
	if err := l.AdjustCash("a", -rp(1000), false); err != nil {
		t.Fatal(err)
	}
	if err := l.AdjustCash("a", rp(7), false); err != nil {
		t.Fatal(err)
	}
	if s := snap(t, l, "a"); s.Cash != rp(7) {
		t.Fatalf("cash = %d", s.Cash)
	}
}

func TestValueUsesPricesAndCarriesUnpricedHoldingsAtCost(t *testing.T) {
	l := newLedger(t, 1000, "a")
	_ = l.Grant("a", "A", 10, rp(10))
	_ = l.Grant("a", "B", 5, rp(20))
	prices := map[string]money.Paise{"A": rp(15)}
	v, err := l.DirectValue("a", func(s string) (money.Paise, bool) { p, ok := prices[s]; return p, ok })
	if err != nil {
		t.Fatal(err)
	}
	if want := rp(1000) + 10*rp(15) + 5*rp(20); v != want {
		t.Fatalf("value = %d, want %d (B has no price, so it is carried at cost)", v, want)
	}
}

func TestConcurrentTradesNeverBreakAnAccount(t *testing.T) {
	const accounts, workers, each = 8, 16, 400
	ids := make([]string, accounts)
	for i := range ids {
		ids[i] = fmt.Sprintf("acct%d", i)
	}
	l := newLedger(t, 500, ids...)
	type tally struct {
		mu   sync.Mutex
		cash int64
		qty  int64
	}
	expect := map[string]*tally{}
	for _, id := range ids {
		expect[id] = &tally{cash: 500 * 100}
	}
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(w)))
			for i := 0; i < each; i++ {
				id := ids[rng.Intn(accounts)]
				buy := rng.Intn(2) == 0
				qty := int64(1 + rng.Intn(5))
				price := money.Paise(100 + rng.Intn(400))
				tl := expect[id]
				tl.mu.Lock() // hold the tally while trading so its expectation matches the ledger's order
				if err := l.Trade(id, "X", buy, qty, price, nil); err == nil {
					if buy {
						tl.cash -= int64(price) * qty
						tl.qty += qty
					} else {
						tl.cash += int64(price) * qty
						tl.qty -= qty
					}
				}
				tl.mu.Unlock()
				if s := snap(t, l, id); s.Cash < 0 {
					t.Errorf("%s went below zero: %d", id, s.Cash)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	for _, id := range ids {
		s := snap(t, l, id)
		var q int64
		for _, p := range s.Positions {
			q += p.Qty
		}
		if int64(s.Cash) != expect[id].cash || q != expect[id].qty || q < 0 {
			t.Errorf("%s: cash %d qty %d, want %d and %d", id, s.Cash, q, expect[id].cash, expect[id].qty)
		}
	}
}

func TestMovesAreAtomicAndCannotDeadlock(t *testing.T) {
	l := newLedger(t, 1000, "a", "b", "c")
	pairs := [][2]string{{"a", "b"}, {"b", "a"}, {"b", "c"}, {"c", "b"}, {"a", "c"}, {"c", "a"}}
	var wg sync.WaitGroup
	for w := 0; w < 12; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				p := pairs[(w+i)%len(pairs)]
				_ = l.Move(p[0], p[1], money.Paise(1+i%50), nil)
			}
		}(w)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	<-done // a deadlock would hang the test until the timeout
	cash, _ := l.Total()
	if cash != 3*rp(1000) {
		t.Fatalf("cash across the accounts = %d, want %d: moves created or destroyed money", cash, 3*rp(1000))
	}
	if err := l.Move("a", "a", 1, nil); err == nil {
		t.Fatal("a move to the same account was accepted")
	}
	if err := l.Move("a", "b", rp(5000), nil); !errors.Is(err, ErrInsufficientCash) {
		t.Fatalf("moving more than held: %v", err)
	}
}

func TestReplayRebuildsTheSameAccountsExactly(t *testing.T) {
	live := newLedger(t, 100000, "a", "b")
	type op struct {
		kind, id, sym string
		buy           bool
		qty           int64
		price         money.Paise
		delta         money.Paise
	}
	var log []op
	rng := rand.New(rand.NewSource(7))
	syms := []string{"P", "Q", "R"}
	for i := 0; i < 600; i++ {
		id := []string{"a", "b"}[rng.Intn(2)]
		sym := syms[rng.Intn(3)]
		switch rng.Intn(6) {
		case 0:
			q, p := int64(1+rng.Intn(20)), money.Paise(50+rng.Intn(200))
			if live.Grant(id, sym, q, p) == nil {
				log = append(log, op{kind: "grant", id: id, sym: sym, qty: q, price: p})
			}
		case 1:
			q := int64(1 + rng.Intn(10))
			if live.Revoke(id, sym, q, false) == nil {
				log = append(log, op{kind: "revoke", id: id, sym: sym, qty: q})
			}
		case 2:
			d := money.Paise(rng.Intn(20000) - 10000)
			if live.AdjustCash(id, d, false) == nil {
				log = append(log, op{kind: "cash", id: id, delta: d})
			}
		default:
			buy, q, p := rng.Intn(2) == 0, int64(1+rng.Intn(15)), money.Paise(50+rng.Intn(200))
			if live.Trade(id, sym, buy, q, p, nil) == nil {
				log = append(log, op{kind: "trade", id: id, sym: sym, buy: buy, qty: q, price: p})
			}
		}
	}
	if len(log) < 200 {
		t.Fatalf("only %d operations succeeded; the test is not exercising anything", len(log))
	}
	replayed := newLedger(t, 100000, "a", "b")
	for _, o := range log {
		var err error
		switch o.kind {
		case "grant":
			err = replayed.Grant(o.id, o.sym, o.qty, o.price)
		case "revoke":
			err = replayed.Revoke(o.id, o.sym, o.qty, true)
		case "cash":
			err = replayed.AdjustCash(o.id, o.delta, true)
		case "trade":
			err = replayed.ReplayTrade(o.id, o.sym, o.buy, o.qty, o.price)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"a", "b"} {
		x, y := snap(t, live, id), snap(t, replayed, id)
		if fmt.Sprint(x.Cash, x.Positions) != fmt.Sprint(y.Cash, y.Positions) {
			t.Errorf("%s differs after replay:\n live     %d %v\n replayed %d %v", id, x.Cash, x.Positions, y.Cash, y.Positions)
		}
	}
}
