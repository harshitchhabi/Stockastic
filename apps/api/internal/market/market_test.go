package market_test

import (
	"sync"
	"testing"
	"time"

	"stockastic/api/internal/market"
	"stockastic/api/internal/money"
	"stockastic/api/internal/universe"
)

func fresh() (*market.Prices, time.Time) {
	t0 := time.Unix(1000, 0)
	return market.New([]universe.Company{{Symbol: "A", Name: "A", Open: 10000}, {Symbol: "B", Name: "B", Open: 5000}}, t0), t0
}

func TestEveryCompanyStartsAtItsOpeningPrice(t *testing.T) {
	p, _ := fresh()
	if px, ok := p.Price("A"); !ok || px != 10000 {
		t.Fatalf("A = %v %v", px, ok)
	}
	if px, _ := p.Open("B"); px != 5000 {
		t.Fatalf("B open = %d", px)
	}
	if _, ok := p.Price("NOPE"); ok {
		t.Fatal("an unknown company has a price")
	}
	if h := p.History("A"); len(h) != 1 || h[0].Price != 10000 {
		t.Fatalf("history = %+v", h)
	}
}

func TestSettingPricesMovesTheCurrentPriceButNeverTheOpen(t *testing.T) {
	p, t0 := fresh()
	p.Set("A", 10250, t0.Add(time.Minute))
	p.SetAll(map[string]money.Paise{"A": 10300, "B": 5100, "NOPE": 1}, t0.Add(2*time.Minute))
	if px, _ := p.Price("A"); px != 10300 {
		t.Fatalf("A = %d", px)
	}
	if px, _ := p.Open("A"); px != 10000 {
		t.Fatalf("the open moved to %d", px)
	}
	p.Set("A", 0, t0)  // a non-positive price is ignored
	p.Set("A", -5, t0) //
	if px, _ := p.Price("A"); px != 10300 {
		t.Fatalf("a bad price was accepted: %d", px)
	}
	if got := p.Symbols(); len(got) != 2 || got[0] != "A" || got[1] != "B" {
		t.Fatalf("symbols = %v", got)
	}
}

func TestHistoryIsBoundedAndACopy(t *testing.T) {
	p, t0 := fresh()
	for i := 0; i < market.MaxHistory+500; i++ {
		p.Set("A", money.Paise(10000+i), t0.Add(time.Duration(i)*time.Second))
	}
	h := p.History("A")
	if len(h) != market.MaxHistory || h[len(h)-1].Price != money.Paise(10000+market.MaxHistory+499) {
		t.Fatalf("history length %d, last %d", len(h), h[len(h)-1].Price)
	}
	h[0].Price = 1
	if p.History("A")[0].Price == 1 {
		t.Fatal("History returned the internal slice")
	}
}

func TestASnapshotDoesNotChangeWhenPricesDo(t *testing.T) {
	p, t0 := fresh()
	snap := p.Take("phase1", t0)
	p.Set("A", 99999, t0.Add(time.Hour))
	if px, _ := snap.Price("A"); px != 10000 {
		t.Fatalf("the freeze-time price drifted to %d", px)
	}
	if _, ok := snap.Price("NOPE"); ok {
		t.Fatal("snapshot invented a company")
	}
}

func TestConcurrentReadsAndWritesAreSafe(t *testing.T) {
	p, t0 := fresh()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(2)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				p.Set("A", money.Paise(10000+i), t0.Add(time.Duration(i)*time.Second))
			}
		}(w)
		go func() {
			defer wg.Done()
			for i := 0; i < 500; i++ {
				p.Price("A")
				p.All()
				p.History("A")
			}
		}()
	}
	wg.Wait()
}
