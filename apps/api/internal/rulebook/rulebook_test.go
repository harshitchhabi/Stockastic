package rulebook

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

const realPath = "rulebook.json"

func loadReal(t *testing.T) *Rulebook {
	t.Helper()
	rb, err := Default()
	if err != nil {
		t.Fatalf("the embedded rulebook.json must load: %v", err)
	}
	return rb
}

// mutate loads the real file as generic JSON, applies fn, and re-parses it.
func mutate(t *testing.T, fn func(m map[string]any)) error {
	t.Helper()
	raw, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	out, _ := json.Marshal(m)
	_, err = Parse(out)
	return err
}

func sub(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		m = m[k].(map[string]any)
	}
	return m
}

func TestRealRulebookMatchesSection17And3(t *testing.T) {
	rb := loadReal(t)
	if got := len(rb.Event.Timeline); got != 17 {
		t.Errorf("timeline blocks = %d, want 17", got)
	}
	if rb.Event.TotalMinutes != 300 || rb.TotalDuration() != 5*time.Hour {
		t.Errorf("total = %d min / %v, want 300 / 5h", rb.Event.TotalMinutes, rb.TotalDuration())
	}
	byStage := map[Stage]int{}
	for _, b := range rb.Event.Timeline {
		byStage[b.Stage] += b.DurationMin
	}
	want := map[Stage]int{StagePhase1: 78, StageTransition: 22, StagePhase2: 155, StageClosing: 45}
	for s, w := range want {
		if byStage[s] != w {
			t.Errorf("stage %s = %d min, want %d", s, byStage[s], w)
		}
	}
	if rb.Market.SymbolCount != 250 {
		t.Errorf("universe = %d, want 250", rb.Market.SymbolCount)
	}
	if rb.RateLimits.TradesPerWindow != 2 || rb.RateLimits.Window() != time.Minute || rb.RateLimits.Scope != "account" {
		t.Errorf("rate limit = %+v, want 2 per minute per account", rb.RateLimits)
	}
	if rb.Fund.LaunchNav != 100 || rb.Fees.PerformanceFeePercent != 5 || rb.Fees.ManagementFeePercent != 1.5 {
		t.Errorf("fund/fee constants wrong: %+v %+v", rb.Fund, rb.Fees)
	}
	if rb.News.Lead() != 60*time.Second {
		t.Errorf("news lead = %v, want 60s", rb.News.Lead())
	}
	if rb.Leaderboard.RefreshSeconds != 300 || rb.Leaderboard.ExposeHoldings {
		t.Errorf("leaderboard = %+v, want 5-minute refresh, holdings hidden", rb.Leaderboard)
	}
	if rb.Disputes.ExpeditedPerPhase != 3 || rb.Disputes.OverflowQueue != "standard" {
		t.Errorf("disputes = %+v", rb.Disputes)
	}
	if rb.Teams.FundManagerSeats != 60 || rb.Qualification.QualifyingTeams != 20 || rb.Qualification.FundCount != 10 {
		t.Errorf("teams/qualification wrong: %+v %+v", rb.Teams, rb.Qualification)
	}
	if got := rb.StartingCapital(); got != 100_000_000 {
		t.Errorf("starting capital = %d paise, want 10,00,000 rupees = 100,000,000", got)
	}
}

func TestBlockOffsetsAreCumulative(t *testing.T) {
	rb := loadReal(t)
	off := rb.BlockOffsets()
	if off[0] != 0 || off[1] != 15*time.Minute || off[2] != 20*time.Minute || off[3] != time.Hour {
		t.Errorf("offsets = %v", off[:4])
	}
	last := len(off) - 1
	if off[last]+rb.Event.Timeline[last].Duration() != rb.TotalDuration() {
		t.Errorf("last block does not end at the event cap")
	}
}

func TestPublicTimelineNeverLeaksTheRegimeBlockOrInternalLabels(t *testing.T) {
	rb := loadReal(t)
	raw, _ := json.Marshal(rb.PublicTimeline())
	s := strings.ToLower(string(raw))
	for _, banned := range []string{"regime", "organiser", "regimeevent"} {
		if strings.Contains(s, banned) {
			t.Errorf("participant-facing timeline contains %q: %s", banned, raw)
		}
	}
	internal := 0
	for _, b := range rb.Event.Timeline {
		if b.RegimeEvent {
			internal++
		}
	}
	if internal != 1 {
		t.Errorf("the organiser view must still mark exactly one regime block, got %d", internal)
	}
}

func TestUnfinalisedValuesAreTaggedAndStayInData(t *testing.T) {
	rb := loadReal(t)
	for path, want := range map[string]Status{
		"fees.managementFeePercent":           StatusRecommended,
		"qualification.tieBreak":              StatusTBF,
		"prizes.prize3.rubric":                StatusTBF,
		"technical.minimumDeviceRequirements": StatusTBF,
		"fund.seedCapital":                    StatusAssumption,
	} {
		p, ok := rb.Status(path)
		if !ok || p.Status != want {
			t.Errorf("%s: status = %+v (tagged=%v), want %s", path, p, ok, want)
		}
	}
	if _, ok := rb.Status("event.totalMinutes"); ok {
		t.Error("event.totalMinutes is fixed by the rulebook and must have no provenance entry")
	}
	for _, c := range rb.Prizes.Prize3.Rubric {
		if c.Weight != nil || c.MaxScore != nil {
			t.Errorf("Prize 3 rubric %q must stay unset (TBF), got weight=%v max=%v", c.Criterion, c.Weight, c.MaxScore)
		}
	}
	if rb.Technical.MinimumDeviceRequirements != nil {
		t.Error("device requirements are TBF and must stay null")
	}
}

func TestLoaderRejectsBrokenRulebooks(t *testing.T) {
	cases := map[string]struct {
		fn      func(m map[string]any)
		wantErr string
	}{
		"unknown top-level key": {func(m map[string]any) { m["surprise"] = 1 }, "unknown field"},
		"unknown nested key":    {func(m map[string]any) { sub(m, "fees")["oops"] = 1 }, "unknown field"},
		"allocation windows out of order": {func(m map[string]any) {
			tl := sub(m, "event")["timeline"].([]any)
			tl[8].(map[string]any)["allocationWindow"] = 2
			tl[10].(map[string]any)["allocationWindow"] = 1
		}, "allocation windows must be numbered"},
		"prize 1 weights not summing to 1":    {func(m map[string]any) { sub(m, "prizes", "prize1")["retention"] = 0.5 }, "prize1 weights"},
		"prize 4 weights not summing to 1":    {func(m map[string]any) { sub(m, "prizes", "prize4")["diversification"] = 0.9 }, "prize4 weights"},
		"qualifiers not twice the fund count": {func(m map[string]any) { sub(m, "qualification")["qualifyingTeams"] = 21 }, "2 x fundCount"},
		"seats not fundCount x team size":     {func(m map[string]any) { sub(m, "teams")["fundManagerSeats"] = 59 }, "fundManagerSeats"},
		"duplicate tie-break step": {func(m map[string]any) {
			sub(m, "qualification")["tieBreak"] = []any{"coin_toss", "coin_toss"}
		}, "duplicate tie-break"},
		"provenance points at a key that does not exist": {func(m map[string]any) {
			sub(m, "provenance")["fees.nonsense"] = map[string]any{"status": "tbf", "section": "12"}
		}, "does not exist"},
		"fee percent out of range": {func(m map[string]any) { sub(m, "fees")["performanceFeePercent"] = 120 }, "outside 0-100"},
		"two regime blocks": {func(m map[string]any) {
			tl := sub(m, "event")["timeline"].([]any)
			tl[11].(map[string]any)["regimeEvent"] = true
		}, "regimeEvent"},
		"missing final freeze": {func(m map[string]any) {
			tl := sub(m, "event")["timeline"].([]any)
			delete(tl[14].(map[string]any), "freezeSnapshot")
		}, "freezeSnapshot"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			err := mutate(t, c.fn)
			if err == nil {
				t.Fatal("expected the loader to reject this rulebook")
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not mention %q", err, c.wantErr)
			}
		})
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load("does/not/exist.json"); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestEmbeddedDefaultIsTheFileOnDisk(t *testing.T) {
	onDisk, err := os.ReadFile(realPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(onDisk) != string(embedded) {
		t.Fatal("the embedded rulebook is stale relative to rulebook.json")
	}
	a, err := Default()
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrDefault("")
	if err != nil || a.Version != b.Version {
		t.Fatalf("LoadOrDefault(\"\") must equal Default: %v", err)
	}
	c, err := LoadOrDefault(realPath)
	if err != nil || c.Event.TotalMinutes != 300 {
		t.Fatalf("LoadOrDefault(path) must load the override file: %v", err)
	}
	if _, err := LoadOrDefault("nope.json"); err == nil {
		t.Error("an override path that does not exist must be an error, never a silent fallback to the embedded rules")
	}
}

func TestPublicConfigNeverLeaksOrganiserOnlyFields(t *testing.T) {
	rb := loadReal(t)
	raw, err := json.Marshal(rb.Public())
	if err != nil {
		t.Fatal(err)
	}
	s := strings.ToLower(string(raw))
	for _, banned := range []string{"regime", "organiser", "\"roles\"", "minimumdevicerequirements", "\"source\""} {
		if strings.Contains(s, banned) {
			t.Errorf("participant-facing config contains %q", banned)
		}
	}
	for _, b := range rb.Event.Timeline {
		if b.Label != b.PublicLabel && strings.Contains(string(raw), b.Label) {
			t.Errorf("internal label leaked: %q", b.Label)
		}
	}
	// The tie-break order must be published to participants before Phase 1 (Sec 5).
	if len(rb.Public().Qualification.TieBreak) == 0 {
		t.Error("tie-break order must be visible to participants")
	}
}

func TestEveryRulebookSectionIsConsciouslyPublicOrPrivate(t *testing.T) {
	// A new top-level field must be added to PublicConfig OR listed here as organiser-only, so a
	// confidential value can never become public just by being added to the rulebook.
	organiserOnly := map[string]bool{"Source": true, "Roles": true, "Technical": true, "Provenance": true}
	pub := reflect.TypeOf(PublicConfig{})
	have := map[string]bool{}
	for i := 0; i < pub.NumField(); i++ {
		have[pub.Field(i).Name] = true
	}
	rt := reflect.TypeOf(Rulebook{})
	for i := 0; i < rt.NumField(); i++ {
		name := rt.Field(i).Name
		if !have[name] && !organiserOnly[name] {
			t.Errorf("Rulebook.%s is neither in PublicConfig nor listed as organiser-only", name)
		}
	}
}

func TestTheScheduleMayBeShorterOrLongerThanFiveHours(t *testing.T) {
	for name, minutes := range map[string]int{"longer": 40, "shorter": 5} {
		raw, err := os.ReadFile(realPath)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(err)
		}
		tl := sub(m, "event")["timeline"].([]any)
		tl[0].(map[string]any)["durationMin"] = minutes // the first block was 15 minutes
		out, _ := json.Marshal(m)
		rb, err := Parse(out)
		if err != nil {
			t.Fatalf("%s schedule was refused: %v", name, err)
		}
		want := 300 - 15 + minutes
		if got := int(rb.TotalDuration() / time.Minute); got != want {
			t.Errorf("%s: TotalDuration = %d min, want %d (the sum of the blocks)", name, got, want)
		}
		if got := rb.Public().Event.TotalMinutes; got != want {
			t.Errorf("%s: the public total = %d, want %d", name, got, want)
		}
	}
}
