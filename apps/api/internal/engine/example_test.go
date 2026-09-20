package engine_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"

	"stockastic/api/internal/engine"
	"stockastic/api/internal/money"
)

type nopJournal struct{}

func (nopJournal) Commit(context.Context, engine.Batch) error { return nil }

// Example_limitOrderBook walks the real engine through price-time priority, partial fills, walking
// several price levels, resting remainders and cancellation.
func Example_limitOrderBook() {
	e, _ := engine.New(engine.Config{
		Symbols: []string{"ACME"}, Journal: nopJournal{},
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	e.Start()
	defer e.Stop(context.Background())

	rupees := func(p money.Paise) string { return fmt.Sprintf("%.2f", p.Rupees()) }
	n := 0
	order := func(who string, side engine.Side, price float64, qty int64) engine.Order {
		n++
		res, _ := e.Submit(context.Background(), engine.NewOrder{
			ClientOrderID: fmt.Sprint(n), AccountID: who, Symbol: "ACME", Side: side, Price: money.FromRupees(price), Qty: qty,
		})
		verb := map[engine.Side]string{engine.Buy: "BUY ", engine.Sell: "SELL"}[side]
		fmt.Printf("\n%s %2d @ %s  (%s)\n", verb, qty, rupees(money.FromRupees(price)), who)
		for _, f := range res.Fills {
			fmt.Printf("   FILL %2d @ %s  <- resting order of %s\n", f.Qty, rupees(f.Price), f.MakerAccountID)
		}
		if res.Order.Remaining > 0 {
			fmt.Printf("   %d left, resting in the book\n", res.Order.Remaining)
		}
		show(e, rupees)
		return res.Order
	}

	order("alice", engine.Sell, 100.00, 10)
	order("bob", engine.Sell, 100.50, 5)
	order("carol", engine.Sell, 100.00, 5) // same price as Alice, but later: queues behind her
	order("dave", engine.Buy, 100.00, 12)  // takes all of Alice's 10 first, then 2 of Carol's
	erin := order("erin", engine.Buy, 99.50, 4)
	order("frank", engine.Buy, 101.00, 20) // sweeps 100.00 then 100.50, remainder rests as the best bid

	_, _ = e.Cancel(context.Background(), "ACME", erin.ID, "erin")
	fmt.Println("\nERIN cancels her 99.50 bid")
	show(e, rupees)

	// Output:
	//
	// SELL 10 @ 100.00  (alice)
	//    10 left, resting in the book
	//    asks: 100.00 x10
	//    bids: -
	//
	// SELL  5 @ 100.50  (bob)
	//    5 left, resting in the book
	//    asks: 100.00 x10 | 100.50 x5
	//    bids: -
	//
	// SELL  5 @ 100.00  (carol)
	//    5 left, resting in the book
	//    asks: 100.00 x15 | 100.50 x5
	//    bids: -
	//
	// BUY  12 @ 100.00  (dave)
	//    FILL 10 @ 100.00  <- resting order of alice
	//    FILL  2 @ 100.00  <- resting order of carol
	//    asks: 100.00 x3 | 100.50 x5
	//    bids: -
	//
	// BUY   4 @ 99.50  (erin)
	//    4 left, resting in the book
	//    asks: 100.00 x3 | 100.50 x5
	//    bids: 99.50 x4
	//
	// BUY  20 @ 101.00  (frank)
	//    FILL  3 @ 100.00  <- resting order of carol
	//    FILL  5 @ 100.50  <- resting order of bob
	//    12 left, resting in the book
	//    asks: -
	//    bids: 101.00 x12 | 99.50 x4
	//
	// ERIN cancels her 99.50 bid
	//    asks: -
	//    bids: 101.00 x12
}

func show(e *engine.Engine, rupees func(money.Paise) string) {
	d, _ := e.Depth("ACME")
	side := func(ls []engine.Level) string {
		if len(ls) == 0 {
			return "-"
		}
		s := ""
		for i, l := range ls {
			if i > 0 {
				s += " | "
			}
			s += fmt.Sprintf("%s x%d", rupees(l.Price), l.Qty)
		}
		return s
	}
	fmt.Printf("   asks: %s\n   bids: %s\n", side(d.Asks), side(d.Bids))
}
