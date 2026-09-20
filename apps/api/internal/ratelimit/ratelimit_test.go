package ratelimit

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stockastic/api/internal/rulebook"
)

var t0 = time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

func TestTwoTradesPerMinutePerAccountFromTheRulebook(t *testing.T) {
	rb, err := rulebook.Default()
	if err != nil {
		t.Fatal(err)
	}
	l := FromRulebook(rb.RateLimits)
	for i, want := range []bool{true, true, false} {
		if ok, _ := l.Allow("acct", t0.Add(time.Duration(i)*time.Second)); ok != want {
			t.Errorf("trade %d allowed = %v, want %v", i+1, ok, want)
		}
	}
}

func TestRollingWindowBoundaryIsExact(t *testing.T) {
	l := New(2, time.Minute)
	l.Allow("a", t0)
	l.Allow("a", t0.Add(10*time.Second))
	if ok, wait := l.Allow("a", t0.Add(59*time.Second+999*time.Millisecond)); ok || wait != time.Millisecond {
		t.Errorf("ok=%v wait=%v: still blocked, 1ms until the first trade ages out", ok, wait)
	}
	if ok, _ := l.Allow("a", t0.Add(time.Minute)); !ok {
		t.Error("the first trade stops counting at exactly t+window")
	}
	if ok, _ := l.Allow("a", t0.Add(time.Minute+time.Second)); ok {
		t.Error("that was the 2nd counted trade (10s and 60s); a 3rd inside the window must wait")
	}
}

func TestLimitIsPerAccountSoASixMemberFundSharesOne(t *testing.T) {
	l := New(2, time.Minute)
	const fundAccount = "fund-3-shared-account"
	allowed := 0
	for member := 0; member < 6; member++ { // six members, all trading the ONE fund account
		if ok, _ := l.Allow(fundAccount, t0); ok {
			allowed++
		}
	}
	if allowed != 2 {
		t.Errorf("a 6-person fund got %d trades in the minute, want the same 2 as a 3-person team", allowed)
	}
	if ok, _ := l.Allow("some-investor-team", t0); !ok {
		t.Error("other accounts are independent")
	}
}

func TestRejectedAttemptsAreNotCounted(t *testing.T) {
	l := New(1, time.Minute)
	l.Allow("a", t0)
	for i := 0; i < 50; i++ {
		l.Allow("a", t0.Add(time.Duration(i)*time.Second)) // hammering while blocked
	}
	if ok, _ := l.Allow("a", t0.Add(time.Minute)); !ok {
		t.Error("blocked attempts must not extend the block")
	}
}

func TestRefundGivesBackTheSlot(t *testing.T) {
	l := New(2, time.Minute)
	l.Allow("a", t0)
	l.Allow("a", t0)
	if ok, _ := l.Allow("a", t0); ok {
		t.Fatal("limit reached")
	}
	l.Refund("a") // the second order was rejected by the platform (e.g. frozen)
	if ok, _ := l.Allow("a", t0); !ok {
		t.Error("a refunded slot must be usable again")
	}
	l.Refund("nobody") // no-op
}

func TestSweepBoundsMemory(t *testing.T) {
	l := New(2, time.Minute)
	for i := 0; i < 1000; i++ {
		l.Allow(fmt.Sprint("a", i), t0)
	}
	if l.Tracked() != 1000 {
		t.Fatal("setup")
	}
	l.Sweep(t0.Add(2 * time.Minute))
	if l.Tracked() != 0 {
		t.Errorf("tracked = %d after everything aged out", l.Tracked())
	}
}

func TestConcurrentAttemptsNeverExceedTheLimit(t *testing.T) {
	l := New(2, time.Minute)
	var ok atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 500; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if a, _ := l.Allow("hot", t0); a {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 2 {
		t.Errorf("%d allowed, want exactly 2", ok.Load())
	}
}
