package engine

import (
	"context"
	"fmt"
	"testing"

	"stockastic/api/internal/money"
)

// FuzzBook feeds the engine arbitrary byte-derived sequences of submits and cancels across a handful
// of accounts and a narrow price band (so orders constantly cross), and after EVERY operation checks:
//   - the book is never crossed and its levels are strictly ordered with positive quantity;
//   - each fill trades at its maker's resting price, and a taker's filled quantity equals its fills;
//   - at the end, an engine rebuilt purely from the committed journal has an identical book.
//
// Run:  go test -run=^$ -fuzz=FuzzBook -fuzztime=60s -parallel=4 ./internal/engine/
//
// Use -parallel=4 (or fewer) on memory-constrained machines: each fuzz worker reserves a 100 MB coverage
// buffer, and 12 workers hit Windows errno 1455 ("paging file too small") on a dev laptop running Docker.
func FuzzBook(f *testing.F) {
	f.Add([]byte{0, 0, 0, 5, 3, 0, 1, 1, 5, 3})                // a resting sell then a crossing buy
	f.Add([]byte{0, 0, 0, 5, 3, 3, 0, 0, 0, 0})                // submit then cancel
	f.Add([]byte{0, 0, 1, 2, 8, 0, 1, 0, 9, 8, 0, 2, 1, 5, 1}) // walk multiple levels
	f.Add([]byte{0, 1, 0, 7, 2, 0, 2, 0, 7, 2, 0, 0, 1, 7, 4}) // time priority within one level

	f.Fuzz(func(t *testing.T, data []byte) {
		j := &journal{}
		e, err := New(Config{Symbols: []string{"FZ"}, Journal: j, Log: quiet})
		if err != nil {
			t.Fatal(err)
		}
		e.Start()
		defer e.Stop(context.Background())

		var mine []Order // orders we may cancel later
		for i, n := 0, 0; i+5 <= len(data) && n < 300; i, n = i+5, n+1 {
			op, acct, side, price, qty := data[i]%4, data[i+1]%4, data[i+2]%2, data[i+3]%16, int64(data[i+4]%9)+1
			if op < 3 {
				res, err := e.Submit(context.Background(), NewOrder{
					ClientOrderID: fmt.Sprint("c", n), AccountID: fmt.Sprint("a", acct), Symbol: "FZ",
					Side: Side(1 + side), Price: money.Paise(9_990 + int64(price)), Qty: qty,
				})
				if err != nil {
					t.Fatalf("submit: %v", err)
				}
				var filled int64
				for _, fl := range res.Fills {
					filled += fl.Qty
					if fl.Qty <= 0 || fl.Price <= 0 {
						t.Fatalf("bad fill %+v", fl)
					}
				}
				if res.Order.Qty-res.Order.Remaining != filled {
					t.Fatalf("taker filled %d but its fills sum to %d", res.Order.Qty-res.Order.Remaining, filled)
				}
				if res.Order.Status.Live() {
					mine = append(mine, res.Order)
				}
			} else if len(mine) > 0 {
				o := mine[int(data[i+4])%len(mine)]
				if _, err := e.Cancel(context.Background(), "FZ", o.ID, o.AccountID); err != nil && err != ErrAlreadyClosed {
					t.Fatalf("cancel: %v", err)
				}
			}
			checkBook(t, e)
		}

		// Every committed fill traded at its maker's price.
		j.mu.Lock()
		batches := append([]Batch(nil), j.batches...)
		j.mu.Unlock()
		latest := map[string]Order{}
		for _, b := range batches {
			for k, fl := range b.Fills {
				if fl.Price != b.Makers[k].Price {
					t.Fatalf("fill at %d but the maker rested at %d", fl.Price, b.Makers[k].Price)
				}
			}
			latest[b.Order.ID] = b.Order
			for _, m := range b.Makers {
				latest[m.ID] = m
			}
		}

		// Durability: rebuild from the journal alone and compare the books.
		var orders []Order
		for _, o := range latest {
			orders = append(orders, o)
		}
		e2, _ := New(Config{Symbols: []string{"FZ"}, Journal: &journal{}, Log: quiet})
		if err := e2.Restore(orders, nil); err != nil {
			t.Fatal(err)
		}
		d1, _ := e.Depth("FZ")
		d2, _ := e2.Depth("FZ")
		if fmt.Sprintf("%+v", d1) != fmt.Sprintf("%+v", d2) {
			t.Fatalf("a book rebuilt from the journal differs:\n live    %+v\n rebuilt %+v", d1, d2)
		}
	})
}

func checkBook(t *testing.T, e *Engine) {
	t.Helper()
	d, _ := e.Depth("FZ")
	if len(d.Bids) > 0 && len(d.Asks) > 0 && d.Bids[0].Price >= d.Asks[0].Price {
		t.Fatalf("crossed book: best bid %d >= best ask %d", d.Bids[0].Price, d.Asks[0].Price)
	}
	for i, l := range d.Bids {
		if l.Qty <= 0 || l.Orders <= 0 || (i > 0 && l.Price >= d.Bids[i-1].Price) {
			t.Fatalf("bad bid ladder %+v", d.Bids)
		}
	}
	for i, l := range d.Asks {
		if l.Qty <= 0 || l.Orders <= 0 || (i > 0 && l.Price <= d.Asks[i-1].Price) {
			t.Fatalf("bad ask ladder %+v", d.Asks)
		}
	}
}
