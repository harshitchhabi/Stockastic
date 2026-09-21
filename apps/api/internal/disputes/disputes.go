// Package disputes files disputes the way the rulebook describes them (Section 22): a team raises the
// issue, it is logged, and the organising committee decides. The rulebook recommends raising it within 10
// minutes of the incident and deciding within 15; both are targets, so a late dispute is flagged and never
// refused, and there is no cap on how many a team may raise.
package disputes

import (
	"time"

	"stockastic/api/internal/ids"
	"stockastic/api/internal/rulebook"
)

// Category is the kind of dispute (Section 22, "Specific categories").
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

// Valid reports whether c is a category the platform accepts.
func (c Category) Valid() bool {
	switch c {
	case CategoryIncorrectTransaction, CategoryMissedAnnouncement, CategoryFundAllocation,
		CategoryNewsTiming, CategoryRuleViolation, CategoryFinalSettlement, CategoryOther:
		return true
	}
	return false
}

type Ticket struct {
	ID       string
	Account  string // the team's platform account
	Category Category
	Summary  string
	RaisedAt time.Time
	// IncidentAt is when the team says it happened.
	IncidentAt time.Time
	// Late is advisory: raised later than the recommended window after the incident. It never rejects.
	Late bool
	// DueBy is when the committee should have decided by (the recommended turnaround).
	DueBy time.Time
}

// Config holds the two recommended times from the rulebook.
type Config struct {
	RaiseWithin    time.Duration
	DecisionTarget time.Duration
}

// FromRulebook builds the config from rulebook.disputes.
func FromRulebook(d rulebook.Disputes) Config {
	return Config{
		RaiseWithin:    time.Duration(d.RaiseWithinMinutes) * time.Minute,
		DecisionTarget: time.Duration(d.DecisionTargetMinutes) * time.Minute,
	}
}

// Raise files a dispute. incidentAt may be zero if the team did not say when it happened.
func (c Config) Raise(account string, cat Category, summary string, incidentAt, now time.Time) Ticket {
	return Ticket{
		ID: ids.New(), Account: account, Category: cat, Summary: summary,
		RaisedAt: now, IncidentAt: incidentAt,
		Late:  !incidentAt.IsZero() && now.Sub(incidentAt) > c.RaiseWithin,
		DueBy: now.Add(c.DecisionTarget),
	}
}
