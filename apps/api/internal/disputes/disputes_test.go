package disputes

import (
	"errors"
	"testing"
	"time"

	"stockastic/api/internal/rulebook"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func tracker(t *testing.T) *Tracker {
	t.Helper()
	rb, err := rulebook.Default()
	if err != nil {
		t.Fatal(err)
	}
	return New(FromRulebook(rb.Disputes))
}

func raise(tr *Tracker, acct string, p Phase) Ticket {
	return tr.Raise(acct, p, CategoryIncorrectTransaction, "x", time.Time{}, t0)
}

func TestFirstThreeDisputesPerPhaseAreExpeditedThenStandard(t *testing.T) {
	tr := tracker(t)
	want := []Queue{QueueExpedited, QueueExpedited, QueueExpedited, QueueStandard, QueueStandard}
	for i, w := range want {
		tk := raise(tr, "team-a", Phase2)
		if tk.Sequence != i+1 || tk.Queue != w {
			t.Errorf("dispute %d: seq=%d queue=%s, want %s", i+1, tk.Sequence, tk.Queue, w)
		}
	}
}

func TestTheLimitIsPerTeamAndPerPhase(t *testing.T) {
	tr := tracker(t)
	for i := 0; i < 4; i++ {
		raise(tr, "team-a", Phase1)
	}
	if got := raise(tr, "team-b", Phase1); got.Queue != QueueExpedited || got.Sequence != 1 {
		t.Errorf("another team is unaffected: %+v", got)
	}
	if got := raise(tr, "team-a", Phase2); got.Queue != QueueExpedited || got.Sequence != 1 {
		t.Errorf("a new phase starts a fresh allowance: %+v", got)
	}
	if tr.Count("team-a", Phase1) != 4 || tr.Count("team-a", Phase2) != 1 {
		t.Error("counts")
	}
}

func TestExpeditedHasATurnaroundTargetAndStandardHasNone(t *testing.T) {
	tr := tracker(t)
	fast := raise(tr, "a", Phase2)
	if !fast.DueBy.Equal(t0.Add(15 * time.Minute)) {
		t.Errorf("DueBy = %v, want raised + 15m", fast.DueBy)
	}
	for i := 0; i < 3; i++ {
		raise(tr, "a", Phase2)
	}
	slow := raise(tr, "a", Phase2)
	if slow.Queue != QueueStandard || !slow.DueBy.IsZero() {
		t.Errorf("standard-priority has no expedited SLA: %+v", slow)
	}
}

func TestHelpDeskCanLiftAFourthDisputeThatIsGenuinelyPlatformWide(t *testing.T) {
	tr := tracker(t)
	for i := 0; i < 3; i++ {
		raise(tr, "a", Phase2)
	}
	fourth := raise(tr, "a", Phase2)
	if fourth.Queue != QueueStandard {
		t.Fatal("setup")
	}
	up, err := tr.Triage(fourth.ID, true)
	if err != nil || up.Queue != QueueExpedited || up.DueBy.IsZero() || !up.PlatformWide {
		t.Fatalf("triage: %+v %v", up, err)
	}
	if tr.Count("a", Phase2) != 4 {
		t.Error("triage must not reduce the team's count")
	}
	down, _ := tr.Triage(fourth.ID, false)
	if down.Queue != QueueStandard {
		t.Errorf("un-marking returns it to the count-based queue: %+v", down)
	}
	if _, err := tr.Triage("nope", true); !errors.Is(err, ErrUnknownTicket) {
		t.Errorf("got %v", err)
	}
}

func TestLateFilingIsFlaggedButNeverRejected(t *testing.T) {
	tr := tracker(t)
	onTime := tr.Raise("a", Phase1, CategoryOther, "", t0.Add(-9*time.Minute), t0)
	late := tr.Raise("a", Phase1, CategoryOther, "", t0.Add(-11*time.Minute), t0)
	unknown := tr.Raise("a", Phase1, CategoryOther, "", time.Time{}, t0)
	if onTime.Late || !late.Late || unknown.Late {
		t.Errorf("late flags: %v %v %v", onTime.Late, late.Late, unknown.Late)
	}
	if late.Queue != QueueExpedited {
		t.Error("a late dispute still gets its expedited slot")
	}
}

func TestStagesMapToPhases(t *testing.T) {
	cases := map[rulebook.Stage]Phase{
		rulebook.StagePhase1: Phase1, rulebook.StageTransition: Phase2,
		rulebook.StagePhase2: Phase2, rulebook.StageClosing: Phase2,
	}
	for s, want := range cases {
		if PhaseOf(s) != want {
			t.Errorf("PhaseOf(%s) = %v, want %v", s, PhaseOf(s), want)
		}
	}
}

func TestRestoreKeepsCountsSoARestartCannotResetTheThrottle(t *testing.T) {
	tr := tracker(t)
	var filed []Ticket
	for i := 0; i < 3; i++ {
		filed = append(filed, raise(tr, "a", Phase2))
	}
	fresh := tracker(t)
	fresh.Restore(filed)
	if got := raise(fresh, "a", Phase2); got.Sequence != 4 || got.Queue != QueueStandard {
		t.Errorf("after restart the 4th dispute must still be standard: %+v", got)
	}
}
