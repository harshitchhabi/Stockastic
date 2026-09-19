// Package disputes implements the Sec 22 dispute throttle: each team may raise a configured number
// of disputes per phase with expedited handling; further disputes in the same phase go to the
// standard-priority queue unless the Help Desk independently judges them to be evidence of a genuine
// platform-wide issue. This stops repeated low-merit disputes being used to force a grace-period
// extension near an allocation-window close. Trades stay final: a dispute never reverses a trade
// itself — a confirmed platform-side error is corrected only through the admin adjustment path.
package disputes

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"stockastic/api/internal/ids"
	"stockastic/api/internal/rulebook"
)

type Phase uint8

const (
	Phase1 Phase = iota + 1
	Phase2
)

func (p Phase) String() string {
	if p == Phase1 {
		return "phase1"
	}
	return "phase2"
}

// PhaseOf maps an event stage to the dispute "phase". [ASSUMPTION] the rulebook says "per phase"
// without saying which phase the transition and closing blocks belong to; they count toward Phase 2.
func PhaseOf(s rulebook.Stage) Phase {
	if s == rulebook.StagePhase1 {
		return Phase1
	}
	return Phase2
}

type Queue string

const (
	QueueExpedited Queue = "expedited"
	QueueStandard  Queue = "standard"
)

// Category is the kind of dispute (Sec 22 "Specific categories").
type Category string

const (
	CategoryIncorrectTransaction Category = "incorrect_transaction"
	CategoryMissedAnnouncement   Category = "missed_announcement"
	CategoryFundAllocation       Category = "fund_allocation"
	CategoryNewsTiming           Category = "news_release_timing"
	CategoryRuleViolation        Category = "suspected_violation"
	CategoryFinalSettlement      Category = "final_settlement"
	CategoryOther                Category = "other"
)

type Ticket struct {
	ID       string
	Account  string // the team's platform account
	Phase    Phase
	Category Category
	Summary  string
	// Sequence is this team's n-th dispute in the phase (1-based).
	Sequence   int
	Queue      Queue
	RaisedAt   time.Time
	IncidentAt time.Time
	// Late is advisory: raised later than the recommended window after the incident. It never rejects.
	Late bool
	// DueBy is the expedited turnaround target; zero for the standard queue (no SLA).
	DueBy time.Time
	// PlatformWide is set by Help Desk triage and forces expedited handling.
	PlatformWide bool
}

var ErrUnknownTicket = errors.New("unknown_ticket")

type Config struct {
	ExpeditedPerPhase   int
	RaiseWithin         time.Duration
	ExpeditedTurnaround time.Duration
}

// FromRulebook builds the config from rulebook.disputes.
func FromRulebook(d rulebook.Disputes) Config {
	return Config{
		ExpeditedPerPhase:   d.ExpeditedPerPhase,
		RaiseWithin:         time.Duration(d.RaiseWithinMinutes) * time.Minute,
		ExpeditedTurnaround: time.Duration(d.ExpeditedTurnaroundMinutes) * time.Minute,
	}
}

type key struct {
	account string
	phase   Phase
}

type Tracker struct {
	cfg Config

	mu      sync.Mutex
	counts  map[key]int
	tickets map[string]*Ticket
}

func New(cfg Config) *Tracker {
	return &Tracker{cfg: cfg, counts: map[key]int{}, tickets: map[string]*Ticket{}}
}

// Raise files a dispute and classifies it: the first ExpeditedPerPhase per team per phase are
// expedited, every later one is standard-priority. incidentAt may be zero if unknown.
func (t *Tracker) Raise(account string, phase Phase, cat Category, summary string, incidentAt, now time.Time) Ticket {
	t.mu.Lock()
	defer t.mu.Unlock()
	k := key{account, phase}
	t.counts[k]++
	tk := &Ticket{
		ID: ids.New(), Account: account, Phase: phase, Category: cat, Summary: summary,
		Sequence: t.counts[k], RaisedAt: now, IncidentAt: incidentAt,
		Late: !incidentAt.IsZero() && now.Sub(incidentAt) > t.cfg.RaiseWithin,
	}
	t.classify(tk)
	t.tickets[tk.ID] = tk
	return *tk
}

func (t *Tracker) classify(tk *Ticket) {
	if tk.Sequence <= t.cfg.ExpeditedPerPhase || tk.PlatformWide {
		tk.Queue = QueueExpedited
		tk.DueBy = tk.RaisedAt.Add(t.cfg.ExpeditedTurnaround)
		return
	}
	tk.Queue, tk.DueBy = QueueStandard, time.Time{}
}

// Triage is the Help Desk's independent assessment. Marking a dispute platform-wide (evidence of a
// genuine issue affecting multiple participants) lifts it into the expedited queue even past the
// per-phase limit; clearing it returns it to its count-based queue. It still counts toward the team's
// total, since the throttle limits the team's filing rate, not the Help Desk's judgement.
func (t *Tracker) Triage(ticketID string, platformWide bool) (Ticket, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	tk, ok := t.tickets[ticketID]
	if !ok {
		return Ticket{}, fmt.Errorf("%w: %s", ErrUnknownTicket, ticketID)
	}
	tk.PlatformWide = platformWide
	t.classify(tk)
	return *tk, nil
}

// Count is how many disputes the team has raised in the phase.
func (t *Tracker) Count(account string, phase Phase) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.counts[key{account, phase}]
}

// Restore reinstates tickets from durable state after a restart.
func (t *Tracker) Restore(all []Ticket) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range all {
		tk := all[i]
		t.tickets[tk.ID] = &tk
		if k := (key{tk.Account, tk.Phase}); tk.Sequence > t.counts[k] {
			t.counts[k] = tk.Sequence
		}
	}
}
