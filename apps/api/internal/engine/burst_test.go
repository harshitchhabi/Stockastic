package engine

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stockastic/api/internal/money"
)

// slowJournal models a database round trip per commit and records the real time each took.
type slowJournal struct {
	delay time.Duration
	total atomic.Int64 // ns spent inside Commit
	n     atomic.Int64
}

func (j *slowJournal) Commit(_ context.Context, _ Batch) error {
	t := time.Now()
	time.Sleep(j.delay)
	j.total.Add(int64(time.Since(t)))
	j.n.Add(1)
	return nil
}

func pct(d []time.Duration, p float64) time.Duration { return d[int(float64(len(d)-1)*p)] }

// TestBurstWhenTradingOpens measures the worst realistic load on the design choice that each commit
// runs on its symbol's goroutine: every one of ~750 teams sends an order to the SAME symbol at the
// instant a trading block opens. (Sustained load is tiny: 2 trades/minute/account is ~25 orders/s.)
func TestBurstWhenTradingOpens(t *testing.T) {
	if testing.Short() {
		t.Skip("slow latency measurement; run without -short")
	}
	const teams = 750
	for _, commitMs := range []int{2, 5, 10} {
		t.Run(fmt.Sprintf("commit=%dms", commitMs), func(t *testing.T) {
			j := &slowJournal{delay: time.Duration(commitMs) * time.Millisecond}
			syms := []string{"HOT"}
			e, err := New(Config{Symbols: syms, Journal: j, Log: quiet, QueueSize: 1024})
			if err != nil {
				t.Fatal(err)
			}
			e.Start()
			defer e.Stop(context.Background())

			lat := make([]time.Duration, teams)
			var wg sync.WaitGroup
			start := time.Now()
			for i := 0; i < teams; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					t0 := time.Now()
					_, err := e.Submit(context.Background(), NewOrder{
						ClientOrderID: fmt.Sprint("c", i), AccountID: fmt.Sprint("team", i), Symbol: "HOT",
						Side: Side(1 + i%2), Price: money.Paise(10_000 + i%5), Qty: 1,
					})
					if err != nil {
						t.Errorf("order %d: %v", i, err)
					}
					lat[i] = time.Since(t0)
				}(i)
			}
			wg.Wait()
			total := time.Since(start)
			sort.Slice(lat, func(a, b int) bool { return lat[a] < lat[b] })
			t.Logf("750 simultaneous orders to ONE symbol: drained in %v | latency p50=%v p95=%v p99=%v max=%v | real mean commit=%v",
				total.Round(time.Millisecond), pct(lat, .5).Round(time.Millisecond), pct(lat, .95).Round(time.Millisecond),
				pct(lat, .99).Round(time.Millisecond), lat[len(lat)-1].Round(time.Millisecond),
				time.Duration(j.total.Load()/j.n.Load()).Round(100*time.Microsecond))
			if j.n.Load() != teams {
				t.Errorf("committed %d of %d", j.n.Load(), teams)
			}
			if total > 60*time.Second {
				t.Errorf("a %dms commit let a 750-order burst take %v", commitMs, total)
			}
		})
	}
}

// TestSpreadLoadScalesAcrossSymbols shows the parallelism: the same 750 orders spread over 250 symbols
// finish in roughly (orders per symbol) x (commit time), not (total orders) x (commit time).
func TestSpreadLoadScalesAcrossSymbols(t *testing.T) {
	const teams, nSyms = 750, 250
	syms := make([]string, nSyms)
	for i := range syms {
		syms[i] = fmt.Sprintf("S%03d", i)
	}
	j := &slowJournal{delay: 5 * time.Millisecond}
	e, _ := New(Config{Symbols: syms, Journal: j, Log: quiet, QueueSize: 64})
	e.Start()
	defer e.Stop(context.Background())
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < teams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if _, err := e.Submit(context.Background(), NewOrder{ClientOrderID: fmt.Sprint("c", i), AccountID: fmt.Sprint("t", i),
				Symbol: syms[i%nSyms], Side: Buy, Price: 10_000, Qty: 1}); err != nil {
				t.Errorf("%v", err)
			}
		}(i)
	}
	wg.Wait()
	total := time.Since(start)
	t.Logf("750 orders over 250 symbols (3 each) at 5ms/commit: drained in %v (a single serial queue would need >= 3.75s)", total.Round(time.Millisecond))
	if total > 2*time.Second {
		t.Errorf("spread load took %v; symbols are not running in parallel", total)
	}
}

// TestNoGoroutineLeakAfterStop: 250 symbol goroutines start and, after Stop, every one of them exits.
func TestNoGoroutineLeakAfterStop(t *testing.T) {
	syms := make([]string, 250)
	for i := range syms {
		syms[i] = fmt.Sprintf("S%03d", i)
	}
	runtime.GC()
	before := runtime.NumGoroutine()
	e, _ := New(Config{Symbols: syms, Journal: &slowJournal{}, Log: quiet})
	e.Start()
	for i := 0; i < 500; i++ {
		_, _ = e.Submit(context.Background(), NewOrder{ClientOrderID: fmt.Sprint("c", i), AccountID: "a", Symbol: syms[i%250], Side: Buy, Price: 100, Qty: 1})
	}
	if during := runtime.NumGoroutine(); during < before+250 {
		t.Fatalf("expected ~250 symbol goroutines while running, saw %d -> %d", before, during)
	}
	if err := e.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > before+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before+2 {
		t.Errorf("goroutines leaked: %d before, %d after Stop", before, after)
	}
}
