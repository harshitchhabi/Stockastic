package disputes

import (
	"testing"
	"time"

	"stockastic/api/internal/rulebook"
)

func config(t *testing.T) Config {
	t.Helper()
	rb, err := rulebook.Default()
	if err != nil {
		t.Fatal(err)
	}
	return FromRulebook(rb.Disputes)
}

func TestConfigComesFromTheRulebook(t *testing.T) {
	c := config(t)
	if c.RaiseWithin != 10*time.Minute || c.DecisionTarget != 15*time.Minute {
		t.Fatalf("config = %+v, want 10 and 15 minutes from Section 22", c)
	}
}

func TestEveryDisputeGetsTheDecisionTarget(t *testing.T) {
	c := config(t)
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	tk := c.Raise("team-a", CategoryIncorrectTransaction, "wrong fill", now.Add(-2*time.Minute), now)
	if !tk.DueBy.Equal(now.Add(15*time.Minute)) || tk.ID == "" || tk.Account != "team-a" || tk.Late {
		t.Fatalf("ticket = %+v", tk)
	}
	// There is no cap on how many a team may raise: the tenth is treated like the first.
	for i := 0; i < 10; i++ {
		got := c.Raise("team-a", CategoryOther, "again", time.Time{}, now)
		if !got.DueBy.Equal(now.Add(15 * time.Minute)) {
			t.Fatalf("dispute %d has DueBy %v", i+1, got.DueBy)
		}
	}
}

func TestALateDisputeIsFlaggedButNeverRefused(t *testing.T) {
	c := config(t)
	now := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	if tk := c.Raise("t", CategoryOther, "s", now.Add(-11*time.Minute), now); !tk.Late {
		t.Error("raised 11 minutes after the incident should be flagged late")
	}
	if tk := c.Raise("t", CategoryOther, "s", now.Add(-10*time.Minute), now); tk.Late {
		t.Error("raised exactly 10 minutes after is still on time")
	}
	if tk := c.Raise("t", CategoryOther, "s", time.Time{}, now); tk.Late {
		t.Error("no incident time means nothing to compare, so not late")
	}
}

func TestCategories(t *testing.T) {
	for _, c := range []Category{CategoryIncorrectTransaction, CategoryMissedAnnouncement, CategoryFundAllocation, CategoryNewsTiming, CategoryRuleViolation, CategoryFinalSettlement, CategoryOther} {
		if !c.Valid() {
			t.Errorf("%s should be valid", c)
		}
	}
	if Category("made_up").Valid() || Category("").Valid() {
		t.Error("an unknown category was accepted")
	}
}
