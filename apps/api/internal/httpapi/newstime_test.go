package httpapi_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"stockastic/api/internal/sim"
	"stockastic/api/internal/store"
	"stockastic/api/internal/universe"
)

// A scenario with a small price table (10 minutes of data) and four news items, loaded from files like the real one.
func newsTimeScenario(t *testing.T) sim.Scenario {
	t.Helper()
	dir := t.TempDir()
	var syms, prices []string
	for _, c := range universe.Default() {
		syms = append(syms, `"`+c.Symbol+`"`)
		prices = append(prices, "100")
	}
	var rows []string
	for i := 0; i < 60; i++ {
		rows = append(rows, "["+strings.Join(prices, ",")+"]")
	}
	table := `{"barSeconds":10,"symbols":[` + strings.Join(syms, ",") + `],"rows":[` + strings.Join(rows, ",") + `]}`
	scenario := `{"newsClock":"market","pricesFile":"prices.json","events":[
	 {"id":"n1","atMinute":2,"headline":"One","type":"REAL","category":"X","phase":"phase1"},
	 {"id":"n2","atMinute":5,"headline":"Two","type":"REAL","category":"X","phase":"phase2"},
	 {"id":"n3","atMinute":9,"headline":"Three","type":"FAKE","category":"RUMOUR","phase":"phase2"},
	 {"id":"n4","atMinute":3,"headline":"Four","type":"REAL","category":"X","phase":"phase2"}]}`
	if err := os.WriteFile(filepath.Join(dir, "prices.json"), []byte(table), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "s.json"), []byte(scenario), 0o600); err != nil {
		t.Fatal(err)
	}
	sc, err := sim.Load(filepath.Join(dir, "s.json"))
	if err != nil {
		t.Fatal(err)
	}
	return sc
}

func TestScheduleChecksAndTheBreakBetweenPhases(t *testing.T) {
	e := newEnvWith(t, store.NewMem(), rb(t, 100), newsTimeScenario(t))
	adm := e.admin()
	// Phase 1 trading (4 min), a 30 minute break with the market closed, then Phase 2 trading (3 min).
	sched := map[string]any{"blocks": []any{
		blk("p1", 4, "phase1", true, nil, "phase1"),
		blk("break", 30, "transition", false, nil, ""),
		blk("p2", 3, "phase2", true, nil, ""),
	}}
	if r := e.call("PUT", "/api/admin/schedule", adm, sched); r.Status != 200 {
		t.Fatalf("schedule: %d %s", r.Status, r.Raw)
	}

	var chk struct {
		OpenMinutes, DataMinutes, LastNewsMinute float64
		Breaks, Warnings                         []string
		Blocks                                   []struct {
			ID                            string
			ClockStartMin, MarketStartMin float64
		}
	}
	get := func() {
		chk.Breaks, chk.Warnings, chk.Blocks = nil, nil, nil
		if err := json.Unmarshal(e.call("GET", "/api/admin/schedule/check", adm, nil).Raw, &chk); err != nil {
			t.Fatal(err)
		}
	}
	get()
	if chk.OpenMinutes != 7 || chk.DataMinutes != 10 || chk.LastNewsMinute != 9 {
		t.Fatalf("check = %+v", chk)
	}
	if len(chk.Breaks) != 1 || !strings.Contains(chk.Breaks[0], "break") || !strings.Contains(chk.Breaks[0], "30") {
		t.Fatalf("the break was not reported: %v", chk.Breaks)
	}
	joined := strings.Join(chk.Warnings, "\n")
	for _, want := range []string{"7 minutes of open trading but the price data covers 10", "will never go out", "different phase"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing the warning %q in:\n%s", want, joined)
		}
	}
	if chk.Blocks[2].ClockStartMin != 34 || chk.Blocks[2].MarketStartMin != 4 {
		t.Fatalf("phase 2 starts at clock minute %v and market minute %v, want 34 and 4", chk.Blocks[2].ClockStartMin, chk.Blocks[2].MarketStartMin)
	}
	// Each news item shows when it will go out on the clock: n2 is at 5 market minutes = 4 in Phase 1 + 1 in Phase 2.
	var st struct {
		Items         []map[string]any
		MarketSeconds float64
	}
	simState := func() {
		st.Items = nil
		_ = json.Unmarshal(e.call("GET", "/api/admin/sim", adm, nil).Raw, &st)
	}
	simState()
	clockOf := map[string]any{}
	for _, it := range st.Items {
		clockOf[it["id"].(string)] = it["clockMinute"]
	}
	if clockOf["n1"] != float64(2) || clockOf["n2"] != float64(35) || clockOf["n3"] != nil {
		t.Fatalf("expected clock minutes = %v", clockOf)
	}
	// Fixing the schedule (7 -> 10 open minutes, phases right) clears the warnings.
	sched = map[string]any{"blocks": []any{
		blk("p1", 4, "phase1", true, nil, "phase1"),
		blk("break", 30, "transition", false, nil, ""),
		blk("p2", 6, "phase2", true, nil, ""),
	}}
	e.call("PUT", "/api/admin/schedule", adm, sched)
	get()
	if len(chk.Warnings) != 1 || !strings.Contains(chk.Warnings[0], "different phase") {
		t.Fatalf("after fixing the length one warning should remain (n4 is designed for Phase 2 but lands at minute 3 of Phase 1): %v", chk.Warnings)
	}

	// The break: market time and prices do not advance while it lasts, and carry on after it.
	if r := e.call("POST", "/api/admin/clock/start", adm, map[string]any{"blockId": "p1"}); r.Status != 200 {
		t.Fatalf("start: %d", r.Status)
	}
	time.Sleep(2300 * time.Millisecond)
	simState()
	inP1 := st.MarketSeconds
	if inP1 < 1 {
		t.Fatalf("market time did not advance in Phase 1: %v", inP1)
	}
	e.jump(adm, "break")
	simState()
	a := st.MarketSeconds
	time.Sleep(2500 * time.Millisecond)
	simState()
	if st.MarketSeconds != a {
		t.Fatalf("market time moved during the break: %v to %v", a, st.MarketSeconds)
	}
	e.jump(adm, "p2")
	time.Sleep(2300 * time.Millisecond)
	simState()
	if st.MarketSeconds <= a {
		t.Fatalf("market time did not carry on after the break: %v", st.MarketSeconds)
	}
}

func TestShiftAndCatchUpTheNews(t *testing.T) {
	e := newEnvWith(t, store.NewMem(), rb(t, 100), newsTimeScenario(t))
	adm := e.admin()
	team, _ := e.signup("Mallory")
	items := func() map[string]map[string]any {
		var st struct{ Items []map[string]any }
		_ = json.Unmarshal(e.call("GET", "/api/admin/sim", adm, nil).Raw, &st)
		out := map[string]map[string]any{}
		for _, it := range st.Items {
			out[it["id"].(string)] = it
		}
		return out
	}
	for _, p := range []string{"/api/admin/sim/shift", "/api/admin/sim/release-overdue", "/api/admin/schedule/check"} {
		if r := e.call("POST", p, team, map[string]any{"minutes": 1}); r.Status != 403 && r.Status != 404 && r.Status != 405 {
			t.Fatalf("a team used %s: %d", p, r.Status)
		}
	}
	if r := e.call("POST", "/api/admin/sim/shift", adm, map[string]any{"minutes": 1.5}); r.Status != 200 {
		t.Fatalf("shift: %d %s", r.Status, r.Raw)
	}
	it := items()
	if it["n1"]["atMinute"] != 3.5 || it["n3"]["atMinute"] != 10.5 || it["n1"]["edited"] != true {
		t.Fatalf("after moving everything 1.5 minutes later: %v", it)
	}
	if r := e.call("POST", "/api/admin/sim/shift", adm, map[string]any{"minutes": -100}); r.Status != 200 {
		t.Fatal("shift earlier")
	}
	if it = items(); it["n1"]["atMinute"] != float64(0) {
		t.Fatalf("an item cannot go before minute 0: %v", it["n1"]["atMinute"])
	}
	if r := e.call("POST", "/api/admin/sim/shift", adm, map[string]any{"minutes": 5000}); r.Status != 400 {
		t.Fatalf("an absurd shift: %d", r.Status)
	}

	// Automatic news is off; the organiser holds one item, then catches everything else up in one go.
	e.call("POST", "/api/admin/sim/news-mode", adm, map[string]any{"manual": true})
	e.call("POST", "/api/admin/sim/n3/skip", adm, map[string]any{"skip": true})
	e.openMarket(adm)
	time.Sleep(1200 * time.Millisecond)
	r := e.call("POST", "/api/admin/sim/release-overdue", adm, map[string]any{})
	if r.Status != 200 || num(r.Body["released"]) != 3 {
		t.Fatalf("release overdue: %d %s (want 3: n3 is held)", r.Status, r.Raw)
	}
	it = items()
	if it["n1"]["fired"] != true || it["n2"]["fired"] != true || it["n4"]["fired"] != true || it["n3"]["fired"] == true {
		t.Fatalf("after catching up: %v", it)
	}
	if r := e.call("POST", "/api/admin/sim/release-overdue", adm, map[string]any{}); num(r.Body["released"]) != 0 {
		t.Fatalf("a second catch-up released %v", r.Body["released"])
	}
}
