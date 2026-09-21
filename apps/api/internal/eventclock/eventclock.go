// Package eventclock is the event state machine: a schedule of blocks (phases, market open or closed,
// allocation windows, freeze snapshots) that the organiser can edit at any time, pause/resume with buffer
// compression, start-anywhere, reset, and admin overrides. The rulebook's timeline is only the starting template. Everything time-dependent asks it. It takes an injectable time
// source and an explicit Tick, so it is fully deterministic under test.
package eventclock

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"stockastic/api/internal/rulebook"
)

// minBlock is the shortest a compressed block may become.
const minBlock = time.Minute

var (
	ErrNotStarted     = errors.New("event_not_started")
	ErrAlreadyStarted = errors.New("event_already_started")
	ErrNotPaused      = errors.New("event_not_paused")
	ErrPaused         = errors.New("event_paused")
	ErrUnknownBlock   = errors.New("unknown_block")
)

// Position is where the event is right now.
type Position struct {
	Started bool
	Ended   bool
	Paused  bool
	// Index is the current block index; len(timeline) once ended; -1 before start.
	Index     int
	Block     rulebook.Block
	Elapsed   time.Duration
	Into      time.Duration
	Remaining time.Duration
}

// Transition is emitted once for every block the event enters, in order, even if a jump or a long
// outage skipped several — so a freeze-snapshot block can never be missed.
type Transition struct {
	Index int
	Block rulebook.Block
	At    time.Time
}

// Overrides are the organiser's live manual controls (Sec 23), layered over the schedule.
type Overrides struct {
	// MarketOpen forces the market open/closed regardless of the block; nil follows the schedule.
	MarketOpen *bool `json:"marketOpen,omitempty"`
	// Windows forces allocation window i open/closed; nil follows the schedule. Its length is the
	// rulebook's window count.
	Windows []*bool `json:"windows"`
	// Frozen is the force-freeze: the market is closed whatever else is set.
	Frozen bool `json:"frozen"`
}

// State is everything needed to restore the clock after a restart.
type State struct {
	StartedAt *time.Time      `json:"startedAt,omitempty"`
	Offset    time.Duration   `json:"offset"`
	PausedAt  *time.Time      `json:"pausedAt,omitempty"`
	Durations []time.Duration `json:"durations"`
	Overrides Overrides       `json:"overrides"`
	// LastNotified is the highest block index whose Transition has been delivered; after a restart
	// Tick replays anything after it.
	LastNotified int `json:"lastNotified"`
	// Blocks is the schedule as the organiser has set it (absent in state saved by older versions).
	Blocks []rulebook.Block `json:"blocks,omitempty"`
}

type Clock struct {
	rb  *rulebook.Rulebook
	now func() time.Time
	log *slog.Logger

	mu        sync.Mutex
	started   bool
	startedAt time.Time
	offset    time.Duration
	paused    bool
	pausedAt  time.Time
	durations []time.Duration
	ov        Overrides
	last      int
	listeners []func(Transition)
	blocks    []rulebook.Block
}

// New builds a clock from the rulebook. now defaults to time.Now.
func New(rb *rulebook.Rulebook, now func() time.Time, log *slog.Logger) *Clock {
	if now == nil {
		now = time.Now
	}
	if log == nil {
		log = slog.Default()
	}
	c := &Clock{rb: rb, now: now, log: log, last: -1}
	c.blocks = append([]rulebook.Block(nil), rb.Event.Timeline...)
	for _, b := range c.blocks {
		c.durations = append(c.durations, b.Duration())
	}
	c.ov.Windows = make([]*bool, windowCount(c.blocks))
	return c
}

// OnTransition registers a listener called (outside the clock's lock) for each block entered.
func (c *Clock) OnTransition(f func(Transition)) {
	c.mu.Lock()
	c.listeners = append(c.listeners, f)
	c.mu.Unlock()
}

func (c *Clock) elapsedLocked(at time.Time) time.Duration {
	if !c.started {
		return 0
	}
	ref := at
	if c.paused {
		ref = c.pausedAt
	}
	return ref.Sub(c.startedAt) + c.offset
}

func (c *Clock) indexLocked(elapsed time.Duration) int {
	if elapsed < 0 {
		elapsed = 0
	}
	var acc time.Duration
	for i, d := range c.durations {
		if elapsed < acc+d {
			return i
		}
		acc += d
	}
	return len(c.durations)
}

func (c *Clock) startOfLocked(idx int) (acc time.Duration) {
	for i := 0; i < idx && i < len(c.durations); i++ {
		acc += c.durations[i]
	}
	return acc
}

func (c *Clock) positionLocked(at time.Time) Position {
	p := Position{Started: c.started, Paused: c.paused, Index: -1}
	if !c.started {
		return p
	}
	p.Elapsed = c.elapsedLocked(at)
	p.Index = c.indexLocked(p.Elapsed)
	if p.Index >= len(c.durations) {
		p.Ended = true
		return p
	}
	p.Block = c.blocks[p.Index]
	p.Into = p.Elapsed - c.startOfLocked(p.Index)
	p.Remaining = c.durations[p.Index] - p.Into
	return p
}

// Position reports the current block and progress.
func (c *Clock) Position() Position {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.positionLocked(c.now())
}

// Start begins the event at T+00:00.
func (c *Clock) Start() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return ErrAlreadyStarted
	}
	c.started, c.startedAt = true, c.now()
	return nil
}

// Pause stops the clock. Sec 23: only for a platform-wide technical failure.
func (c *Clock) Pause() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return ErrNotStarted
	}
	if c.paused {
		return ErrPaused
	}
	c.paused, c.pausedAt = true, c.now()
	return nil
}

// Resume restarts a paused clock. The paused time is lost time; Sec 23 says it is recovered by
// compressing a later buffer block so the 5-hour cap holds. If compressBlockID is given, that block
// is shortened by up to the lost time (never below a minute). It returns how much was absorbed and
// how much overruns the cap because the block was too short to absorb it all.
func (c *Clock) Resume(compressBlockID string) (absorbed, overrun time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return 0, 0, ErrNotStarted
	}
	if !c.paused {
		return 0, 0, ErrNotPaused
	}
	// Validate before mutating: a failed Resume must leave the clock paused, not half-applied.
	idx := -1
	if compressBlockID != "" {
		if idx = c.blockIndexLocked(compressBlockID); idx < 0 {
			return 0, 0, fmt.Errorf("%w: %q", ErrUnknownBlock, compressBlockID)
		}
		if idx <= c.indexLocked(c.elapsedLocked(c.now())) { // paused, so this is the frozen position
			return 0, 0, fmt.Errorf("compress block %q is not later than the current block", compressBlockID)
		}
	}
	lost := c.now().Sub(c.pausedAt)
	c.paused = false
	// Shift the origin so elapsed time did not advance while paused.
	c.startedAt = c.startedAt.Add(lost)
	if idx < 0 {
		return 0, lost, nil
	}
	room := max(c.durations[idx]-minBlock, 0)
	absorbed = min(lost, room)
	c.durations[idx] -= absorbed
	return absorbed, lost - absorbed, nil
}

func (c *Clock) blockIndexLocked(id string) int {
	for i, b := range c.blocks {
		if b.ID == id {
			return i
		}
	}
	return -1
}

// JumpTo moves the clock to the start of a block (organiser schedule adjustment). Blocks skipped
// over still emit their Transitions on the next Tick.
func (c *Clock) JumpTo(blockID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return ErrNotStarted
	}
	idx := c.blockIndexLocked(blockID)
	if idx < 0 {
		return fmt.Errorf("%w: %q", ErrUnknownBlock, blockID)
	}
	c.offset += c.startOfLocked(idx) - c.elapsedLocked(c.now())
	return nil
}

// Nudge shifts the clock forward (positive) or back (negative) by d.
func (c *Clock) Nudge(d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return ErrNotStarted
	}
	c.offset += d
	return nil
}

// ScheduledBlock is a block with where it currently sits in the schedule.
type ScheduledBlock struct {
	Block    rulebook.Block
	Start    time.Duration
	Duration time.Duration
}

// Schedule is the current schedule: the rulebook's blocks with their live lengths, which the organiser
// may have changed.
func (c *Clock) Schedule() []ScheduledBlock {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]ScheduledBlock, len(c.durations))
	var at time.Duration
	for i, d := range c.durations {
		out[i] = ScheduledBlock{Block: c.blocks[i], Start: at, Duration: d}
		at += d
	}
	return out
}

// Total is the current length of the whole schedule.
func (c *Clock) Total() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.startOfLocked(len(c.durations))
}

// MaxBlock caps how long one block may be set to.
const MaxBlock = 24 * time.Hour

var ErrBlockInPast = errors.New("block_already_finished")

// SetBlockDuration changes how long a block lasts, so the event can run shorter or longer than planned.
// A block that has already finished cannot be changed, and the block in progress cannot be set shorter
// than the time already spent in it. Everything after it moves up or back to match.
func (c *Clock) SetBlockDuration(blockID string, d time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	idx := c.blockIndexLocked(blockID)
	if idx < 0 {
		return fmt.Errorf("%w: %q", ErrUnknownBlock, blockID)
	}
	if d < minBlock || d > MaxBlock {
		return fmt.Errorf("a block must last between %v and %v", minBlock, MaxBlock)
	}
	if c.started {
		p := c.positionLocked(c.now())
		if p.Ended || idx < p.Index {
			return ErrBlockInPast
		}
		if idx == p.Index && d < p.Into {
			return fmt.Errorf("this block has already run for %v, so it cannot be shorter than that", p.Into.Round(time.Second))
		}
	}
	c.durations[idx] = d
	return nil
}

// End finishes the event now: the clock moves to the end of the schedule, and every block skipped still
// delivers its transition on the next Tick.
func (c *Clock) End() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return ErrNotStarted
	}
	c.offset += c.startOfLocked(len(c.durations)) - c.elapsedLocked(c.now())
	return nil
}

func (c *Clock) SetFrozen(v bool)          { c.mu.Lock(); c.ov.Frozen = v; c.mu.Unlock() }
func (c *Clock) SetMarketOverride(v *bool) { c.mu.Lock(); c.ov.MarketOpen = v; c.mu.Unlock() }
func (c *Clock) SetWindowOverride(w int, v *bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w < 0 || w >= len(c.ov.Windows) {
		return fmt.Errorf("window %d out of range 0-%d", w, len(c.ov.Windows)-1)
	}
	c.ov.Windows[w] = v
	return nil
}

func (o Overrides) clone() Overrides {
	o.Windows = append([]*bool(nil), o.Windows...)
	return o
}

func (c *Clock) Overrides() Overrides {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ov.clone()
}

// MarketOpen is the single answer to "may orders be accepted right now?". Force-freeze always wins;
// otherwise a manual override wins over the schedule; a clock that is not running or is paused is closed.
func (c *Clock) MarketOpen() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ov.Frozen {
		return false
	}
	p := c.positionLocked(c.now())
	if !p.Started || p.Paused {
		return false
	}
	if c.ov.MarketOpen != nil {
		return *c.ov.MarketOpen
	}
	return !p.Ended && p.Block.MarketOpen
}

// WindowOpen reports whether allocation window w (0..WindowCount-1) is open. Windows have a hard close: when the
// block ends the window is closed, whether or not any fund's cap was filled.
func (c *Clock) WindowOpen(w int) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if w < 0 || w >= len(c.ov.Windows) {
		return false
	}
	p := c.positionLocked(c.now())
	if !p.Started || p.Paused {
		return false
	}
	if c.ov.Windows[w] != nil {
		return *c.ov.Windows[w]
	}
	return !p.Ended && p.Block.AllocationWindow != nil && *p.Block.AllocationWindow == w
}

// Snapshot captures the restorable state.
func (c *Clock) Snapshot() State {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := State{Offset: c.offset, Durations: append([]time.Duration(nil), c.durations...), Overrides: c.ov.clone(), LastNotified: c.last, Blocks: append([]rulebook.Block(nil), c.blocks...)}
	if c.started {
		t := c.startedAt
		s.StartedAt = &t
	}
	if c.paused {
		t := c.pausedAt
		s.PausedAt = &t
	}
	return s
}

// Restore reinstates a snapshot after a restart.
func (c *Clock) Restore(s State) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(s.Blocks) > 0 {
		if err := validateBlocks(s.Blocks); err != nil {
			return fmt.Errorf("eventclock: restored schedule: %w", err)
		}
		c.blocks = append([]rulebook.Block(nil), s.Blocks...)
		if len(s.Durations) != len(c.blocks) {
			s.Durations = nil
			for _, b := range c.blocks {
				s.Durations = append(s.Durations, b.Duration())
			}
		}
	} else if len(s.Durations) != len(c.blocks) {
		return fmt.Errorf("eventclock: restored state has %d block durations, the schedule has %d", len(s.Durations), len(c.blocks))
	}
	c.durations = append([]time.Duration(nil), s.Durations...)
	c.offset, c.last = s.Offset, s.LastNotified
	// Normalise the window overrides to the rulebook's count so a snapshot can never index out of range.
	c.ov = s.Overrides.clone()
	ws := make([]*bool, windowCount(c.blocks))
	copy(ws, c.ov.Windows)
	c.ov.Windows = ws
	c.started, c.paused = s.StartedAt != nil, s.PausedAt != nil
	if s.StartedAt != nil {
		c.startedAt = *s.StartedAt
	}
	if s.PausedAt != nil {
		c.pausedAt = *s.PausedAt
	}
	return nil
}

// Tick delivers a Transition for every block entered since the last Tick. It is safe to call as
// often as you like; the runner calls it about once a second. Listeners run outside the lock and a
// panicking listener cannot stop the others or the clock.
func (c *Clock) Tick() {
	c.mu.Lock()
	var due []Transition
	p := c.positionLocked(c.now())
	if p.Started {
		upto := p.Index // len(timeline) when ended: every block has been entered
		if upto >= len(c.durations) {
			upto = len(c.durations) - 1
		}
		for i := c.last + 1; i <= upto; i++ {
			due = append(due, Transition{Index: i, Block: c.blocks[i], At: c.now()})
		}
		if upto > c.last {
			c.last = upto
		}
	}
	ls := append([]func(Transition){}, c.listeners...)
	c.mu.Unlock()

	for _, t := range due {
		for _, l := range ls {
			c.deliver(l, t)
		}
	}
}

func (c *Clock) deliver(l func(Transition), t Transition) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("eventclock: transition listener panicked", "block", t.Block.ID, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
		}
	}()
	l(t)
}

// Run ticks until ctx is cancelled. It recovers from panics so the clock goroutine never dies.
func (c *Clock) Run(ctx context.Context, every time.Duration) {
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			c.Tick()
		}
	}
}

// ---- the schedule is the organiser's ----

// MaxBlocks is the most blocks a schedule may have.
const MaxBlocks = 60

func windowCount(blocks []rulebook.Block) int {
	n := 0
	for _, b := range blocks {
		if b.AllocationWindow != nil && *b.AllocationWindow+1 > n {
			n = *b.AllocationWindow + 1
		}
	}
	return n
}

// WindowCount is how many allocation windows the current schedule has.
func (c *Clock) WindowCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return windowCount(c.blocks)
}

func validateBlocks(blocks []rulebook.Block) error {
	if len(blocks) == 0 || len(blocks) > MaxBlocks {
		return fmt.Errorf("a schedule needs between 1 and %d blocks", MaxBlocks)
	}
	ids, windows := map[string]bool{}, map[int]bool{}
	for i, b := range blocks {
		if b.ID == "" || len(b.ID) > 40 || ids[b.ID] {
			return fmt.Errorf("block %d has an empty, too long or repeated id %q", i+1, b.ID)
		}
		ids[b.ID] = true
		if b.Label == "" || len(b.Label) > 120 {
			return fmt.Errorf("block %q needs a name of up to 120 characters", b.ID)
		}
		if b.Duration() < minBlock || b.Duration() > MaxBlock {
			return fmt.Errorf("block %q must last between 1 minute and 24 hours", b.ID)
		}
		switch b.Stage {
		case rulebook.StagePhase1, rulebook.StageTransition, rulebook.StagePhase2, rulebook.StageClosing:
		default:
			return fmt.Errorf("block %q has an unknown stage %q", b.ID, b.Stage)
		}
		if w := b.AllocationWindow; w != nil {
			if *w < 0 || *w > 20 || windows[*w] {
				return fmt.Errorf("block %q has an allocation window number that is repeated or out of range", b.ID)
			}
			windows[*w] = true
		}
		if len(b.FreezeSnapshot) > 30 {
			return fmt.Errorf("block %q has a snapshot name that is too long", b.ID)
		}
	}
	return nil
}

// Blocks is the current schedule, with each block's live length in DurationMin.
func (c *Clock) Blocks() []rulebook.Block {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]rulebook.Block, len(c.blocks))
	for i, b := range c.blocks {
		b.DurationMin = int(c.durations[i].Round(time.Minute).Minutes())
		out[i] = b
	}
	return out
}

// Template is the rulebook's timeline, the starting point the organiser can load and then change.
func (c *Clock) Template() []rulebook.Block {
	return append([]rulebook.Block(nil), c.rb.Event.Timeline...)
}

// SetSchedule replaces the whole schedule. The clock keeps its elapsed time, so the event stays where it is in
// time; blocks that are now behind it are not announced again, and window overrides are kept by number.
func (c *Clock) SetSchedule(blocks []rulebook.Block) error {
	if err := validateBlocks(blocks); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.blocks = append([]rulebook.Block(nil), blocks...)
	c.durations = c.durations[:0]
	for _, b := range c.blocks {
		c.durations = append(c.durations, b.Duration())
	}
	ws := make([]*bool, windowCount(c.blocks))
	copy(ws, c.ov.Windows)
	c.ov.Windows = ws
	if c.started {
		c.last = min(c.indexLocked(c.elapsedLocked(c.now())), len(c.blocks)-1)
	} else {
		c.last = -1
	}
	return nil
}

// StartAt begins the event at the start of a chosen block instead of the first one. Blocks before it are
// treated as already done and are not announced.
func (c *Clock) StartAt(blockID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return ErrAlreadyStarted
	}
	idx := 0
	if blockID != "" {
		if idx = c.blockIndexLocked(blockID); idx < 0 {
			return fmt.Errorf("%w: %q", ErrUnknownBlock, blockID)
		}
	}
	c.started, c.paused, c.startedAt = true, false, c.now()
	c.offset = c.startOfLocked(idx)
	c.last = idx - 1
	return nil
}

// Reset puts the clock back to before the start: not started, not paused, no manual overrides. The schedule
// is kept.
func (c *Clock) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started, c.paused, c.offset, c.last = false, false, 0, -1
	c.ov = Overrides{Windows: make([]*bool, windowCount(c.blocks))}
}
