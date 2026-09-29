package app

import (
	"errors"
	"fmt"
	"strings"

	"stockastic/api/internal/dto"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/money"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/store"
	"stockastic/api/internal/trading"
	"stockastic/api/internal/wsapi"
)

// ---- the organiser owns the schedule ----

// ScheduleBlock is one block as the schedule editor sends and receives it.
type ScheduleBlock struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	Minutes          int    `json:"minutes"`
	Stage            string `json:"stage"`
	MarketOpen       bool   `json:"marketOpen"`
	AllocationWindow *int   `json:"allocationWindow"`
	FreezeSnapshot   string `json:"freezeSnapshot"`
}

func toScheduleBlocks(bs []rulebook.Block) []ScheduleBlock {
	out := make([]ScheduleBlock, len(bs))
	for i, b := range bs {
		out[i] = ScheduleBlock{ID: b.ID, Label: b.Label, Minutes: b.DurationMin, Stage: string(b.Stage), MarketOpen: b.MarketOpen,
			AllocationWindow: b.AllocationWindow, FreezeSnapshot: b.FreezeSnapshot}
	}
	return out
}

// Schedule is the current schedule, and the rulebook's timeline that can be loaded as a starting point.
type Schedule struct {
	Blocks   []ScheduleBlock `json:"blocks"`
	Template []ScheduleBlock `json:"template"`
	Started  bool            `json:"started"`
}

func (a *App) Schedule() Schedule {
	return Schedule{Blocks: toScheduleBlocks(a.Clock.Blocks()), Template: toScheduleBlocks(a.Clock.Template()), Started: a.Clock.Position().Started}
}

// SetSchedule replaces the schedule with the organiser's blocks. It works before the event and during it.
func (a *App) SetSchedule(actor User, blocks []ScheduleBlock) error {
	rb := make([]rulebook.Block, len(blocks))
	for i, b := range blocks {
		rb[i] = rulebook.Block{ID: strings.TrimSpace(b.ID), Label: strings.TrimSpace(b.Label), PublicLabel: strings.TrimSpace(b.Label), DurationMin: b.Minutes,
			Stage: rulebook.Stage(b.Stage), MarketOpen: b.MarketOpen, AllocationWindow: b.AllocationWindow, FreezeSnapshot: strings.TrimSpace(b.FreezeSnapshot)}
	}
	return a.Do(actor, fmt.Sprintf("Changed the schedule (%d blocks)", len(blocks)), "clock", "", func() error {
		if err := a.Clock.SetSchedule(rb); err != nil {
			return bad("invalid_schedule", err.Error())
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

// LoadTemplateSchedule replaces the schedule with the rulebook's timeline.
func (a *App) LoadTemplateSchedule(actor User) error {
	return a.Do(actor, "Loaded the rulebook's timeline as the schedule", "clock", "", func() error {
		if err := a.Clock.SetSchedule(a.Clock.Template()); err != nil {
			return bad("invalid_schedule", err.Error())
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

// ClockStartAt starts the event at a block of the organiser's choice ("" starts at the first one).
func (a *App) ClockStartAt(actor User, blockID string) error {
	return a.Do(actor, "Started the event at "+orFirst(blockID), "clock", "", func() error {
		if err := a.Clock.StartAt(blockID); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		// Deliver the block's start now rather than on the next tick.
		a.Clock.Tick()
		return nil
	})
}

func orFirst(id string) string {
	if id == "" {
		return "the first block"
	}
	return "block " + id
}

// ---- snapshots on demand ----

// TakeSnapshot freezes every team's value now under a name ("phase1" ranks the teams into funds; "final" settles
// the prizes), so the organiser does not depend on the schedule having a freeze block.
func (a *App) TakeSnapshot(actor User, name string) error {
	name = strings.TrimSpace(name)
	if name != "phase1" && name != "final" {
		return bad("invalid_snapshot", "A snapshot is either phase1 or final.")
	}
	return a.Do(actor, "Froze the standings as "+name, "event", "", func() error {
		if _, done := a.Snapshot(name); done {
			return bad("snapshot_exists", "That snapshot was already taken. Reset the event to take it again.")
		}
		a.takeSnapshot(name)
		if name == "final" {
			a.takeCheckpoint("final")
		}
		if _, done := a.Snapshot(name); !done {
			return errors.New("the snapshot could not be saved")
		}
		return nil
	})
}

// ---- reset ----

// resetState puts everything that belongs to one run of the event back to its start: prices, holdings and
// cash, trades, funds, snapshots, news, disputes and the clock. Accounts, the audit log and the schedule stay.
// It runs live when the organiser resets, and again on restart when the log reaches the reset record.
func (a *App) resetState() {
	a.Ledger.Reset()
	a.Exec.Reset()
	a.Market.Reset(a.cfg.Universe, a.now())
	a.Sim.Reset()
	a.News.Reset()
	a.Limiter.Reset()
	a.Funds.Reset()
	a.Clock.Reset()

	a.statMu.Lock()
	a.allTrades = nil
	a.recent, a.p1Trades, a.peaks, a.snapshots = map[string][]trading.Trade{}, map[string]int{}, map[string]money.Paise{}, map[string]FreezeSnapshot{}
	a.statMu.Unlock()
	a.ticketMu.Lock()
	a.tickets = map[string]*TicketRec{}
	a.ticketMu.Unlock()
	a.annMu.Lock()
	a.announcements = nil
	a.annMu.Unlock()
	a.pauseMu.Lock()
	a.paused = map[string]bool{}
	a.pauseMu.Unlock()
	a.lbMu.Lock()
	a.lbCache = nil
	a.lbMu.Unlock()
	a.simState = nil

	for _, u := range a.users.all() {
		if u.IsAdmin {
			continue
		}
		_ = a.Ledger.Open(ledger.Account{ID: u.ID, Name: u.DisplayName, Kind: ledger.KindTeam}, a.RB.StartingCapital())
	}
}

// resetRecord is what a reset writes to the log. Everything also deletes every team and teammate.
type resetRecord struct {
	At         int64 `json:"at"`
	Everything bool  `json:"everything,omitempty"`
}

// dropTeams deletes every team account, teammate and watchlist, keeping only the organisers. It returns the
// accounts removed.
func (a *App) dropTeams() []string {
	gone := a.users.removeTeams()
	a.members.clear()
	a.watchMu.Lock()
	a.watch = map[string][]string{}
	a.watchMu.Unlock()
	a.presMu.Lock()
	a.pres = map[string]presence{}
	a.presMu.Unlock()
	return gone
}

// ResetEvent starts the whole event over: every team is back to its starting cash with no shares, prices are
// back to their opening values, the clock is before the start, and funds, trades, news and disputes are gone.
// Teams keep their accounts and passwords; warnings and disqualifications are cleared and everyone is an
// investor again. The audit log is kept.
//
// With everything, every team and teammate is deleted too and everyone is signed out: only the organisers'
// logins, the audit log and the organisers' settings (rules text, sign-up code and list) remain, and people
// register again from the start.
func (a *App) ResetEvent(actor User, everything bool) error {
	what := "Reset the whole event"
	if everything {
		what = "Started completely fresh: deleted every team"
	}
	return a.Do(actor, what, "event", "", func() error {
		a.evMu.Lock()
		defer a.evMu.Unlock()
		a.fundMu.Lock()
		defer a.fundMu.Unlock()
		if err := a.cfg.Disk.Check(); err != nil {
			return err
		}
		if err := a.wal.Append(store.KindReset, resetRecord{At: a.now().UnixMilli(), Everything: everything}); err != nil {
			return err
		}
		var gone []string
		if everything {
			gone = a.dropTeams()
		}
		a.resetState()
		for _, u := range a.users.all() {
			if u.IsAdmin {
				continue
			}
			if _, err := a.updateUser(u.ID, func(x *User) error {
				x.Role, x.Status, x.Warnings = RoleInvestor, StatusActive, 0
				return nil
			}); err != nil {
				a.log.Error("could not clear a team after the reset", "account", u.ID, "err", err)
			}
		}
		a.persistClock()
		a.broadcastControl()
		a.Hub.ToAll("eventReset", map[string]any{"at": dto.MS(a.now())})
		for _, id := range gone {
			a.Hub.DisconnectAccount(id, wsapi.CloseUnauthenticated, "the event was started fresh")
		}
		return nil
	})
}

// ---- step by step ----

// ClockNext moves the event to its next step: from not started to the first step, and then one step at a time. The
// organisers run the event this way; no step ends by itself. Window 0 cannot open before the funds are formed,
// because investors would have nowhere to put their money.
//
// to, when given, is the step the organiser pressed the button for. If the event has already moved on (another
// organiser pressed it first, or the same press arrived twice) nothing happens, so a step can never be skipped.
func (a *App) ClockNext(actor User, reason, to string) error {
	a.stepMu.Lock()
	defer a.stepMu.Unlock()
	p := a.Clock.Position()
	blocks := a.Clock.Blocks()
	if to != "" {
		want := ""
		switch {
		case !p.Started && len(blocks) > 0:
			want = blocks[0].ID
		case p.Started && !p.Ended && p.Index+1 < len(blocks):
			want = blocks[p.Index+1].ID
		}
		if want != to {
			return bad("step_changed", "The event has already moved on (perhaps another organiser pressed Next step). Check the steps and try again.")
		}
	}
	if !p.Started {
		return a.ClockStartAt(actor, "")
	}
	if p.Ended {
		return bad("event_closed", "The event has already closed.")
	}
	next := p.Index + 1
	if next >= len(blocks) {
		return bad("no_next_step", "This is the last step.")
	}
	b := blocks[next]
	if b.AllocationWindow != nil && !a.Funds.Formed() {
		return bad("funds_not_formed", "Form the funds first (Funds page), then open the allocation window.")
	}
	return a.Do(actor, "Moved to the next step: "+b.Label, "clock", reason, func() error {
		if err := a.Clock.JumpTo(b.ID); err != nil {
			return err
		}
		a.persistClock()
		a.Clock.Tick() // the step's start (a results freeze, say) happens now, not on the next tick
		a.broadcastControl()
		return nil
	})
}

// StandingsVisible says whether players may see the standings now: during Phase 1 only (from the start until the
// funds' first allocation window opens), unless the rulebook publishes them throughout.
func (a *App) StandingsVisible() bool {
	return a.RB.Leaderboard.VisibleToParticipants || a.stage() == rulebook.StagePhase1
}
