package ledger

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"stockastic/api/internal/engine"
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

// grant gives an account shares at an exact total cost, bypassing the average-cost arithmetic of the
// production Grant method, so tests can set a precise starting position.
func (l *Ledger) grant(id, symbol string, qty int64, cost money.Paise) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.accts[id].pos[symbol] = &Position{Symbol: symbol, Qty: qty, Cost: cost}
}

func buy(id, client string, price, qty int64) engine.NewOrder {
	return engine.NewOrder{ClientOrderID: client, AccountID: id, Symbol: "ACME", Side: engine.Buy, Price: rp(price), Qty: qty}
}
func sell(id, client string, price, qty int64) engine.NewOrder {
	n := buy(id, client, price, qty)
	n.Side = engine.Sell
	return n
}

func TestNoLeverage_BuyMustBeCoveredByUnreservedCash(t *testing.T) {
	l := newLedger(t, 1000, "a")
	if _, err := l.Reserve(buy("a", "1", 100, 10)); err != nil { // exactly 1000
		t.Fatalf("an exactly-covered buy must pass: %v", err)
	}
	if _, err := l.Reserve(buy("a", "2", 1, 1)); !errors.Is(err, ErrInsufficientCash) {
		t.Fatalf("cash is fully reserved, got %v", err)
	}
	l.Release("a", "1")
	if _, err := l.Reserve(buy("a", "2", 1, 1)); err != nil {
		t.Errorf("releasing frees the cash: %v", err)
	}
}

func TestNoShorting_SellMustBeCoveredByUnreservedShares(t *testing.T) {
	l := newLedger(t, 0, "a")
	if _, err := l.Reserve(sell("a", "1", 100, 1)); !errors.Is(err, ErrInsufficientShares) {
		t.Fatalf("selling shares you do not own: %v", err)
	}
	l.grant("a", "ACME", 10, rp(1000))
	if _, err := l.Reserve(sell("a", "1", 100, 6)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(sell("a", "2", 100, 5)); !errors.Is(err, ErrInsufficientShares) {
		t.Errorf("4 shares are unreserved, asking for 5: %v", err)
	}
	if _, err := l.Reserve(sell("a", "3", 100, 4)); err != nil {
		t.Errorf("the remaining 4 are available: %v", err)
	}
}

func TestReserveIsIdempotentPerClientOrderID(t *testing.T) {
	l := newLedger(t, 1000, "a")
	c1, _ := l.Reserve(buy("a", "same", 100, 10))
	c2, err := l.Reserve(buy("a", "same", 100, 10))
	if !c1 || c2 || err != nil {
		t.Fatalf("created=%v/%v err=%v: a retry must not reserve twice", c1, c2, err)
	}
	s, _ := l.Snapshot("a")
	if s.ReservedCash != rp(1000) {
		t.Errorf("reserved = %v, want 1000", s.ReservedCash)
	}
}

func TestUnknownAccount(t *testing.T) {
	l := newLedger(t, 1, "a")
	if _, err := l.Reserve(buy("ghost", "1", 1, 1)); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("got %v", err)
	}
	if err := l.Open(Account{ID: "a"}, 0); !errors.Is(err, ErrAccountExists) {
		t.Errorf("duplicate open: %v", err)
	}
}

func TestConcurrentReservationsCanNeverOversubscribeCash(t *testing.T) {
	l := newLedger(t, 1000, "a") // room for exactly 10 orders of 100
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := l.Reserve(buy("a", fmt.Sprint("c", i), 100, 1)); err == nil {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 10 {
		t.Errorf("%d reservations succeeded, want exactly 10", ok.Load())
	}
}

// ---- fills through the real engine -----------------------------------------------------------

type memJournal struct {
	mu sync.Mutex
	n  int
}

func (j *memJournal) Commit(_ context.Context, _ engine.Batch) error {
	j.mu.Lock()
	j.n++
	j.mu.Unlock()
	return nil
}

func rig(t *testing.T, l *Ledger) *engine.Engine {
	t.Helper()
	e, err := engine.New(engine.Config{Symbols: []string{"ACME"}, Journal: &memJournal{}, Sink: l, Log: quiet})
	if err != nil {
		t.Fatal(err)
	}
	e.Start()
	t.Cleanup(func() { _ = e.Stop(context.Background()) })
	return e
}

// place does what the API layer will: reserve, submit, release on failure.
func place(t *testing.T, l *Ledger, e *engine.Engine, n engine.NewOrder) engine.Result {
	t.Helper()
	created, err := l.Reserve(n)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	res, err := e.Submit(context.Background(), n)
	if err != nil || res.Deduped {
		if created {
			l.Release(n.AccountID, n.ClientOrderID)
		}
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
	}
	return res
}

func TestFillsMoveCashAndSharesOnBothSidesAtTheMakerPrice(t *testing.T) {
	l := newLedger(t, 100_000, "seller", "buyer")
	l.grant("seller", "ACME", 10, rp(500))
	e := rig(t, l)

	place(t, l, e, sell("seller", "s1", 100, 10))
	place(t, l, e, buy("buyer", "b1", 105, 10)) // willing to pay 105, trades at the resting 100

	b, _ := l.Snapshot("buyer")
	s, _ := l.Snapshot("seller")
	if b.Cash != rp(100_000-1000) || len(b.Positions) != 1 || b.Positions[0].Qty != 10 || b.Positions[0].Cost != rp(1000) {
		t.Errorf("buyer = %+v", b)
	}
	if s.Cash != rp(100_000+1000) || len(s.Positions) != 0 {
		t.Errorf("seller = %+v", s)
	}
	if b.ReservedCash != 0 || s.ReservedCash != 0 {
		t.Errorf("a fully-filled order must hold no reservation (price improvement returned): %v %v", b.ReservedCash, s.ReservedCash)
	}
	if l.Anomalies() != 0 {
		t.Error("anomalies")
	}
}

func TestPartialFillShrinksTheReservationAndCancelReleasesIt(t *testing.T) {
	l := newLedger(t, 100_000, "seller", "buyer")
	l.grant("seller", "ACME", 4, rp(400))
	e := rig(t, l)

	place(t, l, e, sell("seller", "s1", 100, 4))
	res := place(t, l, e, buy("buyer", "b1", 100, 10)) // fills 4, rests 6
	b, _ := l.Snapshot("buyer")
	if b.ReservedCash != rp(600) || b.Cash != rp(100_000-400) {
		t.Fatalf("buyer after partial fill = %+v (want 6 shares x 100 still reserved)", b)
	}
	if _, err := e.Cancel(context.Background(), "ACME", res.Order.ID, "buyer"); err != nil {
		t.Fatal(err)
	}
	b, _ = l.Snapshot("buyer")
	if b.ReservedCash != 0 || b.AvailableCash != b.Cash {
		t.Errorf("cancel must release the reservation: %+v", b)
	}
}

func TestSellingAvoidsShortingEvenWhenOrdersRace(t *testing.T) {
	l := newLedger(t, 0, "a")
	l.grant("a", "ACME", 5, rp(500))
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := l.Reserve(sell("a", fmt.Sprint("c", i), 100, 1)); err == nil {
				ok.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if ok.Load() != 5 {
		t.Errorf("%d sell reservations succeeded, want exactly the 5 shares owned", ok.Load())
	}
}

func TestAverageCostAcrossBuysAndPartialSells(t *testing.T) {
	l := newLedger(t, 1_000_000, "a", "mm")
	l.grant("mm", "ACME", 100, rp(0))
	e := rig(t, l)
	place(t, l, e, sell("mm", "s1", 100, 10))
	place(t, l, e, sell("mm", "s2", 120, 10))
	place(t, l, e, buy("a", "b1", 100, 10)) // 10 @ 100
	place(t, l, e, buy("a", "b2", 120, 10)) // 10 @ 120  -> 20 shares, cost 2200
	s, _ := l.Snapshot("a")
	if s.Positions[0].Qty != 20 || s.Positions[0].Cost != rp(2200) {
		t.Fatalf("position = %+v", s.Positions[0])
	}
	place(t, l, e, sell("a", "s3", 110, 5)) // no buyer -> rests, no fill
	place(t, l, e, buy("mm", "mb", 110, 5)) // fills: a sells 5 at 110, removing 5 shares at avg cost 110
	s, _ = l.Snapshot("a")
	if s.Positions[0].Qty != 15 || s.Positions[0].Cost != rp(1650) {
		t.Errorf("after selling 5: %+v (want qty 15, cost 1650)", s.Positions[0])
	}
}

func TestValuationUsesPricesAndFallsBackToCost(t *testing.T) {
	l := newLedger(t, 1000, "a")
	l.grant("a", "ACME", 10, rp(500))
	l.grant("a", "GLOBEX", 4, rp(200))
	prices := func(sym string) (money.Paise, bool) {
		if sym == "ACME" {
			return rp(70), true
		}
		return 0, false // GLOBEX has not traded: carry at cost
	}
	v, err := l.DirectValue("a", prices)
	if err != nil || v != rp(1000+700+200) {
		t.Errorf("value = %v err=%v, want 1900", v, err)
	}
}

func TestTransferIsAtomicAndRespectsReservations(t *testing.T) {
	l := newLedger(t, 1000, "inv", "fund")
	if _, err := l.Reserve(buy("inv", "r", 100, 6)); err != nil { // 600 reserved
		t.Fatal(err)
	}
	if err := l.Transfer("inv", "fund", rp(500)); !errors.Is(err, ErrInsufficientCash) {
		t.Errorf("only 400 is unreserved: %v", err)
	}
	inv, _ := l.Snapshot("inv")
	fund, _ := l.Snapshot("fund")
	if inv.Cash != rp(1000) || fund.Cash != rp(1000) {
		t.Errorf("a failed transfer must change nothing: %v %v", inv.Cash, fund.Cash)
	}
	if err := l.Transfer("inv", "fund", rp(400)); err != nil {
		t.Fatal(err)
	}
	inv, _ = l.Snapshot("inv")
	fund, _ = l.Snapshot("fund")
	if inv.Cash != rp(600) || fund.Cash != rp(1400) {
		t.Errorf("after transfer: %v %v", inv.Cash, fund.Cash)
	}
	if err := l.Transfer("inv", "ghost", 1); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("got %v", err)
	}
	if err := l.Transfer("inv", "fund", 0); err == nil {
		t.Error("zero transfer must be rejected")
	}
}

func TestRestoreReplaysFillsAndRebuildsReservations(t *testing.T) {
	// Run a session, then rebuild a fresh ledger from the "durable" record of it.
	live := newLedger(t, 100_000, "seller", "buyer")
	live.grant("seller", "ACME", 10, rp(1000))
	var fills []engine.Fill
	var liveOrders []engine.Order
	j := &recordingJournal{}
	e, _ := engine.New(engine.Config{Symbols: []string{"ACME"}, Journal: j, Sink: live, Log: quiet})
	e.Start()
	place(t, live, e, sell("seller", "s1", 100, 10))
	place(t, live, e, buy("buyer", "b1", 100, 4)) // fills 4
	place(t, live, e, buy("buyer", "b2", 90, 5))  // rests
	_ = e.Stop(context.Background())
	fills, liveOrders = j.fills, j.liveOrders()

	fresh := newLedger(t, 100_000, "seller", "buyer")
	fresh.grant("seller", "ACME", 10, rp(1000)) // opening inventory is part of the durable opening state
	fresh.Restore(fills, liveOrders)

	for _, id := range []string{"seller", "buyer"} {
		a, _ := live.Snapshot(id)
		b, _ := fresh.Snapshot(id)
		if fmt.Sprint(a) != fmt.Sprint(b) {
			t.Errorf("%s differs after restore:\n live  %+v\n fresh %+v", id, a, b)
		}
	}
}

type recordingJournal struct {
	mu     sync.Mutex
	fills  []engine.Fill
	orders map[string]engine.Order
}

func (j *recordingJournal) Commit(_ context.Context, b engine.Batch) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.orders == nil {
		j.orders = map[string]engine.Order{}
	}
	j.fills = append(j.fills, b.Fills...)
	j.orders[b.Order.ID] = b.Order
	for _, m := range b.Makers {
		j.orders[m.ID] = m
	}
	return nil
}

func (j *recordingJournal) liveOrders() (out []engine.Order) {
	for _, o := range j.orders {
		if o.Status.Live() {
			out = append(out, o)
		}
	}
	return out
}

func TestFillReferencingAnUnknownAccountIsCountedNotSwallowed(t *testing.T) {
	l := newLedger(t, 100, "known")
	l.OnApplied(engine.Applied{Batch: engine.Batch{Kind: engine.KindSubmit, Fills: []engine.Fill{{
		ID: "f", Symbol: "ACME", Price: rp(1), Qty: 1, TakerAccountID: "known", MakerAccountID: "ghost", TakerSide: engine.Buy,
	}}}})
	if l.Anomalies() != 1 {
		t.Errorf("anomalies = %d, want 1", l.Anomalies())
	}
}

func TestMarketMakerIsJustAnAccountKindForNow(t *testing.T) {
	l := New(quiet)
	if err := l.Open(Account{ID: "mm", Name: "MM", Kind: KindMarketMaker}, rp(1)); err != nil {
		t.Fatal(err)
	}
	// Uniform checks: an unseeded market maker cannot sell shares it does not hold.
	if _, err := l.Reserve(sell("mm", "1", 1, 1)); !errors.Is(err, ErrInsufficientShares) {
		t.Errorf("got %v; MM exemption is an open decision so no special case exists", err)
	}
	if KindMarketMaker.String() != "market_maker" || KindFund.String() != "fund" {
		t.Error("kind names")
	}
}

func TestAdjustCashAndRevokeRespectWhatIsHeldBack(t *testing.T) {
	l := newLedger(t, 1000, "a") // 1000 rupees of cash

	// Hold 100 rupees back for a working buy.
	if _, err := l.Reserve(buy("a", "c1", 10, 10)); err != nil {
		t.Fatal(err)
	}
	if err := l.AdjustCash("a", -rp(950), false); !errors.Is(err, ErrInsufficientCash) {
		t.Fatalf("a debit into reserved cash: %v", err)
	}
	if err := l.AdjustCash("a", -rp(900), false); err != nil {
		t.Fatalf("a debit that leaves the reservation covered: %v", err)
	}
	if err := l.AdjustCash("a", rp(50), false); err != nil {
		t.Fatal(err)
	}
	if err := l.AdjustCash("a", -rp(500), true); err != nil { // replaying the log never refuses
		t.Fatal(err)
	}
	if err := l.AdjustCash("nobody", 1, false); !errors.Is(err, ErrUnknownAccount) {
		t.Fatalf("unknown account: %v", err)
	}

	if err := l.Grant("a", "X", 10, rp(50)); err != nil {
		t.Fatal(err)
	}
	if err := l.Revoke("a", "X", 11, false); !errors.Is(err, ErrInsufficientShares) {
		t.Fatalf("revoking more than held: %v", err)
	}
	if err := l.Revoke("a", "X", 4, false); err != nil {
		t.Fatal(err)
	}
	snap, _ := l.Snapshot("a")
	if len(snap.Positions) != 1 || snap.Positions[0].Qty != 6 || snap.Positions[0].Cost != 6*rp(50) {
		t.Fatalf("position after revoke = %+v (cost is taken back at average cost)", snap.Positions)
	}
	if err := l.Revoke("a", "X", 6, false); err != nil {
		t.Fatal(err)
	}
	if snap, _ = l.Snapshot("a"); len(snap.Positions) != 0 {
		t.Fatalf("a fully revoked position is still listed: %+v", snap.Positions)
	}
	if err := l.Revoke("a", "Y", 1, false); !errors.Is(err, ErrInsufficientShares) {
		t.Fatalf("revoking an unheld company: %v", err)
	}
	// Shares held back for a working sell cannot be taken away.
	if err := l.Grant("a", "ACME", 5, rp(50)); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Reserve(sell("a", "s1", 60, 5)); err != nil {
		t.Fatal(err)
	}
	if err := l.Revoke("a", "ACME", 1, false); !errors.Is(err, ErrInsufficientShares) {
		t.Fatalf("revoking shares held back for a working sell: %v", err)
	}
}
