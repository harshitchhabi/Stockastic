package market_test

import (
	"testing"
	"time"

	"stockastic/api/internal/market"
	"stockastic/api/internal/money"
	"stockastic/api/internal/universe"
)

func TestTrackerFollowsTradesAndBoundsHistory(t *testing.T) {
	t0 := time.Unix(1000, 0)
	tr := market.New([]universe.Company{{Symbol: "A", Name: "A", Open: 10000}}, t0)

	if p, ok := tr.Last("A"); !ok || p != 10000 {
		t.Fatalf("Last before any trade = %v %v", p, ok)
	}
	if _, ok := tr.Traded("A"); ok {
		t.Fatal("an untraded company reports a traded price")
	}
	tr.PriceUpdate("A", 10250, t0.Add(time.Second))
	if p, _ := tr.Traded("A"); p != 10250 {
		t.Fatalf("Traded = %v", p)
	}
	if o, _ := tr.Open("A"); o != 10000 {
		t.Fatalf("open moved to %v", o)
	}
	tr.PriceUpdate("NOPE", 1, t0) // unknown symbols are ignored, not a crash
	if _, ok := tr.Last("NOPE"); ok {
		t.Fatal("an unknown symbol appeared")
	}

	for i := 0; i < market.MaxHistory+500; i++ {
		tr.PriceUpdate("A", money.Paise(10000+i), t0.Add(time.Duration(i)*time.Second))
	}
	h := tr.History("A")
	if len(h) != market.MaxHistory {
		t.Fatalf("history length %d, want the cap %d", len(h), market.MaxHistory)
	}
	if h[len(h)-1].Price != money.Paise(10000+market.MaxHistory+499) {
		t.Fatal("history lost its most recent point")
	}
	h[0].Price = 1 // callers get a copy
	if tr.History("A")[0].Price == 1 {
		t.Fatal("History returned the internal slice")
	}
}
