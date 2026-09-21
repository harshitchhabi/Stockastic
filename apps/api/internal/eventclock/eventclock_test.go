package eventclock

import (
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"stockastic/api/internal/rulebook"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func load(t *testing.T) *rulebook.Rulebook {
	t.Helper()
	rb, err := rulebook.Default()
	if err != nil {
		t.Fatal(err)
	}
	return rb
}

type fakeTime struct{ t time.Time }

func (f *fakeTime) now() time.Time          { return f.t }
func (f *fakeTime) advance(d time.Duration) { f.t = f.t.Add(d) }

func newClock(t *testing.T) (*Clock, *fakeTime, *rulebook.Rulebook) {
	t.Helper()
	rb := load(t)
	ft := &fakeTime{t: time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)}
	return New(rb, ft.now, quiet), ft, rb
}

func bp(b bool) *bool { return &b }

func TestNothingIsOpenBeforeStart(t *testing.T) {
	c, _, _ := newClock(t)
	if p := c.Position(); p.Started || p.Index != -1 {
		t.Errorf("%+v", p)
	}
	if c.MarketOpen() || c.WindowOpen(0) {
		t.Error("nothing may be open before the event starts")
	}
	if err := c.JumpTo("p1_trading"); !errors.Is(err, ErrNotStarted) {
		t.Errorf("JumpTo before start: %v", err)
	}
	if err := c.Pause(); !errors.Is(err, ErrNotStarted) {
		t.Errorf("Pause before start: %v", err)
	}
}

func TestEveryBlockMatchesTheRulebookAtItsMidpoint(t *testing.T) {
	c, ft, rb := newClock(t)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); !errors.Is(err, ErrAlreadyStarted) {
		t.Errorf("second Start: %v", err)
	}
	offsets := rb.BlockOffsets()
	start := ft.t
	for i, b := range rb.Event.Timeline {
		ft.t = start.Add(offsets[i] + b.Duration()/2)
		p := c.Position()
		if p.Index != i || p.Block.ID != b.ID {
			t.Fatalf("at the middle of %s the clock says block %d (%s)", b.ID, p.Index, p.Block.ID)
		}
		if c.MarketOpen() != b.MarketOpen {
			t.Errorf("%s: market open = %v, rulebook says %v", b.ID, c.MarketOpen(), b.MarketOpen)
		}
		for w := 0; w < 4; w++ {
			want := b.AllocationWindow != nil && *b.AllocationWindow == w
			if c.WindowOpen(w) != want {
				t.Errorf("%s: window %d open = %v, want %v", b.ID, w, c.WindowOpen(w), want)
			}
		}
	}
	ft.t = start.Add(rb.TotalDuration())
	if p := c.Position(); !p.Ended || c.MarketOpen() {
		t.Errorf("at exactly 300 minutes the event is over and closed: %+v", p)
	}
}

func TestBlockBoundariesAreExact(t *testing.T) {
	c, ft, _ := newClock(t)
	_ = c.Start()
	ft.advance(20*time.Minute - time.Second)
	if c.MarketOpen() {
		t.Error("Phase 1 trading opens at T+00:20, not a second early")
	}
	ft.advance(time.Second)
	if !c.MarketOpen() {
		t.Error("open at T+00:20")
	}
	ft.advance(40*time.Minute - time.Second)
	if !c.MarketOpen() {
		t.Error("still open one second before the freeze")
	}
	ft.advance(time.Second)
	if c.MarketOpen() {
		t.Error("Phase 1 freezes at T+01:00")
	}
}

func TestAllocationWindowsHardCloseWhenTheirBlockEnds(t *testing.T) {
	c, ft, _ := newClock(t)
	_ = c.Start()
	ft.advance(90 * time.Minute) // T+01:30: Window 0
	if !c.WindowOpen(0) {
		t.Fatal("Window 0 opens with the Phase-2 transition")
	}
	ft.advance(10*time.Minute - time.Second)
	if !c.WindowOpen(0) {
		t.Error("open until the last second")
	}
	ft.advance(time.Second) // T+01:40: trading block 1
	if c.WindowOpen(0) || !c.MarketOpen() {
		t.Error("the window closes at its hard end, whatever any fund's cap looks like")
	}
	ft.advance(35 * time.Minute) // T+02:15: Window 1
	if !c.WindowOpen(1) || c.WindowOpen(0) || c.MarketOpen() {
		t.Error("Window 1 open, market closed")
	}
}

func TestOverridesLayerOverTheScheduleAndForceFreezeAlwaysWins(t *testing.T) {
	c, ft, _ := newClock(t)
	_ = c.Start() // briefing: closed
	c.SetMarketOverride(bp(true))
	if !c.MarketOpen() {
		t.Error("organiser can force the market open")
	}
	c.SetFrozen(true)
	if c.MarketOpen() {
		t.Error("force-freeze beats an open override")
	}
	c.SetFrozen(false)
	c.SetMarketOverride(nil)
	if c.MarketOpen() {
		t.Error("clearing the override returns to the schedule")
	}

	ft.advance(30 * time.Minute) // Phase 1 trading
	c.SetMarketOverride(bp(false))
	if c.MarketOpen() {
		t.Error("organiser can force the market closed")
	}

	if err := c.SetWindowOverride(2, bp(true)); err != nil {
		t.Fatal(err)
	}
	if !c.WindowOpen(2) || c.WindowOpen(1) {
		t.Error("window override applies to that window only")
	}
	if err := c.SetWindowOverride(9, nil); err == nil {
		t.Error("window 9 does not exist")
	}
	if c.WindowOpen(-1) || c.WindowOpen(4) {
		t.Error("out-of-range windows are never open")
	}
}

func TestPauseFreezesTheClockAndResumeRecoversTheLostTimeFromABufferBlock(t *testing.T) {
	c, ft, rb := newClock(t)
	_ = c.Start()
	ft.advance(30 * time.Minute) // mid Phase-1 trading
	before := c.Position()

	if err := c.Pause(); err != nil {
		t.Fatal(err)
	}
	if c.MarketOpen() {
		t.Error("a paused clock accepts no orders")
	}
	ft.advance(7 * time.Minute)
	if got := c.Position(); got.Elapsed != before.Elapsed || !got.Paused {
		t.Errorf("elapsed moved while paused: %v -> %v", before.Elapsed, got.Elapsed)
	}
	if err := c.Pause(); !errors.Is(err, ErrPaused) {
		t.Errorf("double pause: %v", err)
	}

	absorbed, overrun, err := c.Resume("settlement")
	if err != nil {
		t.Fatal(err)
	}
	if absorbed != 7*time.Minute || overrun != 0 {
		t.Errorf("absorbed=%v overrun=%v, want the 7 lost minutes taken out of the 20-minute buffer", absorbed, overrun)
	}
	if after := c.Position(); after.Elapsed != before.Elapsed || after.Paused {
		t.Errorf("resume must continue from where it paused: %+v", after)
	}
	// The 5-hour cap holds: total is 300 - 7 minutes of buffer, and the paused 7 minutes were lost.
	st := c.Snapshot()
	var total time.Duration
	for _, d := range st.Durations {
		total += d
	}
	if total != rb.TotalDuration()-7*time.Minute {
		t.Errorf("total = %v, want %v", total, rb.TotalDuration()-7*time.Minute)
	}
	if _, _, err := c.Resume("settlement"); !errors.Is(err, ErrNotPaused) {
		t.Errorf("resume when running: %v", err)
	}
}

func TestResumeCannotCompressBelowAMinuteAndReportsTheOverrun(t *testing.T) {
	c, ft, _ := newClock(t)
	_ = c.Start()
	_ = c.Pause()
	ft.advance(45 * time.Minute) // far more than the 20-minute buffer can absorb
	absorbed, overrun, err := c.Resume("settlement")
	if err != nil {
		t.Fatal(err)
	}
	if absorbed != 19*time.Minute || overrun != 26*time.Minute {
		t.Errorf("absorbed=%v overrun=%v, want 19m / 26m (the buffer keeps one minute)", absorbed, overrun)
	}
}

func TestResumeRejectsAnUnknownOrNotLaterBlock(t *testing.T) {
	c, ft, _ := newClock(t)
	_ = c.Start()
	ft.advance(30 * time.Minute)
	_ = c.Pause()
	ft.advance(time.Minute)
	if _, _, err := c.Resume("nonsense"); !errors.Is(err, ErrUnknownBlock) {
		t.Errorf("got %v", err)
	}
	if _, _, err := c.Resume("briefing"); err == nil {
		t.Error("compressing a block that is already over makes no sense")
	}
	if !c.Position().Paused {
		t.Error("a rejected Resume must leave the clock paused, not half-applied")
	}
	if _, _, err := c.Resume("settlement"); err != nil {
		t.Errorf("a valid Resume after the rejected ones must still work: %v", err)
	}
}

func collect(c *Clock) *[]Transition {
	var got []Transition
	c.OnTransition(func(tr Transition) { got = append(got, tr) })
	return &got
}

func TestTickEmitsEachBlockExactlyOnceInOrderAcrossTheWholeEvent(t *testing.T) {
	c, ft, rb := newClock(t)
	got := collect(c)
	_ = c.Start()
	for i := 0; i <= 305; i++ { // every minute, a little past the end
		c.Tick()
		ft.advance(time.Minute)
	}
	if len(*got) != len(rb.Event.Timeline) {
		t.Fatalf("got %d transitions, want %d", len(*got), len(rb.Event.Timeline))
	}
	for i, tr := range *got {
		if tr.Index != i || tr.Block.ID != rb.Event.Timeline[i].ID {
			t.Errorf("transition %d = %d/%s", i, tr.Index, tr.Block.ID)
		}
	}
}

func TestJumpingForwardStillDeliversTheSkippedFreezeSnapshotBlock(t *testing.T) {
	c, _, _ := newClock(t)
	got := collect(c)
	_ = c.Start()
	c.Tick()
	if err := c.JumpTo("p1_announce"); err != nil { // skips over p1_freeze
		t.Fatal(err)
	}
	c.Tick()
	var freezes []string
	for _, tr := range *got {
		if tr.Block.FreezeSnapshot != "" {
			freezes = append(freezes, tr.Block.FreezeSnapshot)
		}
	}
	if len(freezes) != 1 || freezes[0] != "phase1" {
		t.Errorf("the Phase-1 freeze must not be missed by a jump: %v", freezes)
	}
	n := len(*got)
	c.Tick()
	if len(*got) != n {
		t.Error("a second Tick must not re-emit")
	}
}

func TestNudgeShiftsTheClock(t *testing.T) {
	c, ft, _ := newClock(t)
	_ = c.Start()
	if err := c.Nudge(25 * time.Minute); err != nil {
		t.Fatal(err)
	}
	if !c.MarketOpen() || c.Position().Block.ID != "p1_trading" {
		t.Errorf("%+v", c.Position())
	}
	if err := c.Nudge(-25 * time.Minute); err != nil {
		t.Fatal(err)
	}
	ft.advance(time.Second)
	if c.MarketOpen() {
		t.Error("nudged back into the briefing")
	}
}

func TestSnapshotRestoreSurvivesARestartAndReplaysOnlyMissedTransitions(t *testing.T) {
	c, ft, rb := newClock(t)
	first := collect(c)
	_ = c.Start()
	ft.advance(25 * time.Minute)
	c.Tick() // delivered: briefing, login, p1_trading
	if len(*first) != 3 {
		t.Fatalf("got %d, want 3", len(*first))
	}
	_ = c.SetWindowOverride(1, bp(true))
	c.SetFrozen(true)
	snap := c.Snapshot()

	// Process dies; while it is down the clock keeps running past the Phase-1 freeze.
	ft.advance(40 * time.Minute)
	fresh := New(rb, ft.now, quiet)
	second := collect(fresh)
	if err := fresh.Restore(snap); err != nil {
		t.Fatal(err)
	}
	if !fresh.Overrides().Frozen || fresh.Overrides().Windows[1] == nil {
		t.Errorf("overrides lost across restart: %+v", fresh.Overrides())
	}
	if got := fresh.Position(); got.Block.ID != "p1_announce" && got.Block.ID != "p1_freeze" {
		t.Errorf("restored position = %s", got.Block.ID)
	}
	fresh.Tick()
	ids := []string{}
	for _, tr := range *second {
		ids = append(ids, tr.Block.ID)
	}
	if len(ids) == 0 || ids[0] != "p1_freeze" {
		t.Errorf("after restart the missed freeze block must be replayed first, got %v", ids)
	}
	for _, id := range ids {
		if id == "briefing" || id == "login" || id == "p1_trading" {
			t.Errorf("already-delivered block %s must not be replayed", id)
		}
	}
	if err := fresh.Restore(State{Durations: []time.Duration{1}}); err == nil {
		t.Error("a snapshot from a different timeline must be rejected")
	}
}

func TestAPanickingListenerDoesNotStopOthersOrTheClock(t *testing.T) {
	c, ft, _ := newClock(t)
	c.OnTransition(func(Transition) { panic("bad listener") })
	got := collect(c)
	_ = c.Start()
	ft.advance(16 * time.Minute)
	c.Tick()
	if len(*got) != 2 { // briefing + login
		t.Errorf("healthy listener saw %d transitions, want 2", len(*got))
	}
	c.Tick() // still alive
}

func TestWindowCountComesFromTheRulebookNotFromTheCode(t *testing.T) {
	c, _, _ := newClock(t)
	if got := len(c.Overrides().Windows); got != 4 {
		t.Fatalf("v1.1 has 4 windows, clock has %d override slots", got)
	}
	if err := c.SetWindowOverride(4, bp(true)); err == nil {
		t.Error("window 4 does not exist in v1.1")
	}
	// A snapshot with too few or too many override slots is normalised, never an out-of-range panic.
	for _, n := range []int{0, 2, 9} {
		s := c.Snapshot()
		s.Overrides.Windows = make([]*bool, n)
		if err := c.Restore(s); err != nil {
			t.Fatal(err)
		}
		if got := len(c.Overrides().Windows); got != 4 {
			t.Errorf("restored from %d slots -> %d, want 4", n, got)
		}
		_ = c.WindowOpen(3) // must not panic
	}
	// Overrides() hands out a copy: mutating it must not change the clock.
	o := c.Overrides()
	o.Windows[0] = bp(true)
	if c.Overrides().Windows[0] != nil {
		t.Error("Overrides() leaked the internal slice")
	}
}

func TestOrganiserSchedule(t *testing.T) {
	rb, err := rulebook.Default()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	c := New(rb, func() time.Time { return now }, nil)
	w0 := 0
	blocks := []rulebook.Block{
		{ID: "a", Label: "A", DurationMin: 10, Stage: rulebook.StagePhase1},
		{ID: "b", Label: "B", DurationMin: 20, Stage: rulebook.StagePhase1, MarketOpen: true, FreezeSnapshot: "phase1"},
		{ID: "c", Label: "C", DurationMin: 5, Stage: rulebook.StagePhase2, AllocationWindow: &w0},
	}
	if err := c.SetSchedule([]rulebook.Block{{ID: "a"}, {ID: "a"}}); err == nil {
		t.Fatal("a broken schedule was accepted")
	}
	if err := c.SetSchedule(blocks); err != nil {
		t.Fatal(err)
	}
	if c.Total() != 35*time.Minute || c.WindowCount() != 1 {
		t.Fatalf("total %v, windows %d", c.Total(), c.WindowCount())
	}

	var got []string
	c.OnTransition(func(tr Transition) { got = append(got, tr.Block.ID) })
	if err := c.StartAt("nope"); err == nil {
		t.Fatal("started at a block that does not exist")
	}
	if err := c.StartAt("b"); err != nil {
		t.Fatal(err)
	}
	c.Tick()
	if p := c.Position(); p.Block.ID != "b" || !c.MarketOpen() {
		t.Fatalf("position = %+v", p)
	}
	if len(got) != 1 || got[0] != "b" {
		t.Fatalf("announced %v, want only the block he started at", got)
	}

	// Pausing stops the clock and the market.
	if err := c.Pause(); err != nil || c.MarketOpen() {
		t.Fatalf("pause: %v, open %v", err, c.MarketOpen())
	}
	now = now.Add(time.Hour)
	if _, _, err := c.Resume(""); err != nil {
		t.Fatal(err)
	}
	if p := c.Position(); p.Block.ID != "b" {
		t.Fatalf("time passed while paused: %+v", p)
	}

	// The schedule survives a save and restore, and a reset keeps it while putting the clock back.
	c2 := New(rb, func() time.Time { return now }, nil)
	if err := c2.Restore(c.Snapshot()); err != nil {
		t.Fatal(err)
	}
	if c2.Total() != 35*time.Minute || c2.Position().Block.ID != "b" {
		t.Fatalf("restored: total %v block %v", c2.Total(), c2.Position().Block.ID)
	}
	c.Reset()
	if c.Position().Started || c.MarketOpen() || c.Total() != 35*time.Minute {
		t.Fatalf("after reset: %+v total %v", c.Position(), c.Total())
	}
	if err := c.StartAt(""); err != nil || c.Position().Block.ID != "a" {
		t.Fatalf("start after reset: %v %+v", err, c.Position())
	}
}
