package eventclock

import (
	"errors"
	"testing"
	"time"

	"stockastic/api/internal/rulebook"
)

func TestTheOrganiserCanMakeTheEventLongerOrShorter(t *testing.T) {
	c, _, rb := newClock(t)
	planned := rb.TotalDuration()
	if c.Total() != planned {
		t.Fatalf("Total = %v, want the planned %v", c.Total(), planned)
	}

	// Before the event starts, any block may be changed.
	if err := c.SetBlockDuration("p1_trading", 90*time.Minute); err != nil {
		t.Fatal(err)
	}
	trading := 0
	for i, b := range rb.Event.Timeline {
		if b.ID == "p1_trading" {
			trading = i
		}
	}
	if got, want := c.Total(), planned-blockLen(rb, "p1_trading")+90*time.Minute; got != want {
		t.Fatalf("Total after lengthening = %v, want %v", got, want)
	}
	sch := c.Schedule()
	if sch[trading].Duration != 90*time.Minute {
		t.Fatalf("schedule = %+v", sch[trading])
	}
	// Everything after it moved back by the extra time.
	if sch[trading+1].Start != sch[trading].Start+90*time.Minute {
		t.Fatalf("the next block starts at %v, want %v", sch[trading+1].Start, sch[trading].Start+90*time.Minute)
	}
}

func blockLen(rb *rulebook.Rulebook, id string) time.Duration {
	for _, b := range rb.Event.Timeline {
		if b.ID == id {
			return b.Duration()
		}
	}
	panic("no such block " + id)
}

func TestBlockLengthLimits(t *testing.T) {
	c, ft, _ := newClock(t)
	if err := c.SetBlockDuration("nope", time.Hour); !errors.Is(err, ErrUnknownBlock) {
		t.Fatalf("unknown block: %v", err)
	}
	for _, d := range []time.Duration{0, 30 * time.Second, MaxBlock + time.Minute} {
		if err := c.SetBlockDuration("p1_trading", d); err == nil {
			t.Fatalf("a block of %v was accepted", d)
		}
	}

	_ = c.Start()
	_ = c.JumpTo("p1_trading")
	ft.advance(10 * time.Minute) // 10 minutes into trading

	// The block in progress cannot be cut shorter than the time already spent in it.
	if err := c.SetBlockDuration("p1_trading", 5*time.Minute); err == nil {
		t.Fatal("shortened the current block below the time already spent")
	}
	if err := c.SetBlockDuration("p1_trading", 10*time.Minute); err != nil {
		t.Fatalf("ending the current block right now should be allowed: %v", err)
	}
	// A block that already finished is history.
	if err := c.SetBlockDuration("briefing", time.Hour); !errors.Is(err, ErrBlockInPast) {
		t.Fatalf("changing a finished block: %v", err)
	}
	// Later blocks may still be changed.
	if err := c.SetBlockDuration("p1_freeze", 20*time.Minute); err != nil {
		t.Fatal(err)
	}
}

func TestExtendingTheCurrentBlockKeepsTheClockWhereItIs(t *testing.T) {
	c, ft, _ := newClock(t)
	_ = c.Start()
	_ = c.JumpTo("p1_trading")
	ft.advance(30 * time.Minute)
	before := c.Position()
	if before.Block.ID != "p1_trading" {
		t.Fatalf("setup: %+v", before)
	}
	if err := c.SetBlockDuration("p1_trading", 120*time.Minute); err != nil {
		t.Fatal(err)
	}
	after := c.Position()
	if after.Block.ID != "p1_trading" || after.Into != before.Into {
		t.Fatalf("extending moved the clock: before %+v after %+v", before, after)
	}
	if after.Remaining != 120*time.Minute-before.Into {
		t.Fatalf("remaining = %v, want %v", after.Remaining, 120*time.Minute-before.Into)
	}
}

func TestEndingTheEventEarlyDeliversEveryBlockOnceAndStopsTheMarket(t *testing.T) {
	c, ft, rb := newClock(t)
	if err := c.End(); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("ending an event that never started: %v", err)
	}
	var seen []int
	c.OnTransition(func(tr Transition) { seen = append(seen, tr.Index) })
	_ = c.Start()
	_ = c.JumpTo("p1_trading")
	c.Tick()
	ft.advance(5 * time.Minute)
	if !c.MarketOpen() {
		t.Fatal("setup: market should be open")
	}

	if err := c.End(); err != nil {
		t.Fatal(err)
	}
	c.Tick()
	if p := c.Position(); !p.Ended {
		t.Fatalf("not ended: %+v", p)
	}
	if c.MarketOpen() {
		t.Fatal("the market is still open after the event ended")
	}
	if len(seen) != len(rb.Event.Timeline) {
		t.Fatalf("delivered %d transitions, want one for each of the %d blocks", len(seen), len(rb.Event.Timeline))
	}
	for i, idx := range seen {
		if idx != i {
			t.Fatalf("transitions out of order or repeated: %v", seen)
		}
	}
}

func TestChangedBlockLengthsSurviveARestart(t *testing.T) {
	c, _, rb := newClock(t)
	_ = c.Start()
	if err := c.SetBlockDuration("p1_trading", 75*time.Minute); err != nil {
		t.Fatal(err)
	}
	snap := c.Snapshot()

	c2 := New(rb, c.now, quiet)
	if err := c2.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if c2.Total() != c.Total() {
		t.Fatalf("after a restart the length is %v, want %v", c2.Total(), c.Total())
	}
}
