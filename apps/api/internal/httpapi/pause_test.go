package httpapi_test

import (
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"stockastic/api/internal/sim"
	"stockastic/api/internal/store"
)

// A break: the organisers pause the whole event, in Phase 1 or Phase 2. While paused, prices do not move, nothing
// can be bought, sold or moved into or out of funds, the event cannot move to the next step, and every screen is
// told. Resuming carries on from exactly where it stopped.
func TestPausingTheEventForABreak(t *testing.T) {
	moving := sim.Scenario{Seed: 3, TickSeconds: 1, Volatility: sim.Volatility{DefaultPct: 1}}
	e := newEnvWith(t, store.NewMem(), rb(t, 100), moving)
	adm := e.admin()
	var teams []team
	for i := 0; i < 5; i++ {
		tok, id := e.signup(fmt.Sprintf("Brk%d", i))
		teams = append(teams, team{fmt.Sprintf("Brk%d", i), tok, id})
	}
	inv := teams[4].token
	var syms []struct {
		Symbol    string  `json:"symbol"`
		LastPrice float64 `json:"lastPrice"`
	}
	_ = json.Unmarshal(e.call("GET", "/api/symbols", inv, nil).Raw, &syms)
	prices := func() map[string]float64 {
		var cs []struct {
			Symbol    string  `json:"symbol"`
			LastPrice float64 `json:"lastPrice"`
		}
		_ = json.Unmarshal(e.call("GET", "/api/symbols", inv, nil).Raw, &cs)
		out := map[string]float64{}
		for _, c := range cs {
			out[c.Symbol] = c.LastPrice
		}
		return out
	}
	control := func() map[string]any { return e.call("GET", "/api/admin/overview", adm, nil).Body }
	post := func(path string, body map[string]any) resp { return e.call("POST", path, adm, body) }
	buy := func(id string) resp {
		return e.call("POST", "/api/trades", inv, map[string]any{"clientTradeId": id, "symbol": syms[0].Symbol, "side": "buy", "qty": 1})
	}

	// ---- Phase 1 ----
	if r := post("/api/admin/clock/next", map[string]any{}); r.Status != 200 {
		t.Fatalf("start: %d %s", r.Status, r.Raw)
	}
	if r := buy("b1"); r.Status != 200 {
		t.Fatalf("a trade before the break: %d %s", r.Status, r.Raw)
	}
	if r := post("/api/admin/clock/pause", map[string]any{}); r.Status != 200 {
		t.Fatalf("pause: %d %s", r.Status, r.Raw)
	}
	if st := control()["clock"].(map[string]any)["status"]; st != "paused" {
		t.Fatalf("clock status while paused = %v", st)
	}
	before := prices()
	time.Sleep(2500 * time.Millisecond) // the market would move every second
	after := prices()
	for s, p := range before {
		if after[s] != p {
			t.Fatalf("%s moved during the break: %v to %v", s, p, after[s])
		}
	}
	if r := buy("b2"); r.Status != 423 || r.Body["error"] != "event_paused" {
		t.Fatalf("a trade during the break: %d %s", r.Status, r.Raw)
	}
	if r := post("/api/admin/clock/next", map[string]any{}); r.Status != 400 || r.Body["error"] != "event_paused" {
		t.Fatalf("next step during the break: %d %s", r.Status, r.Raw)
	}
	if r := post("/api/admin/clock/resume", map[string]any{}); r.Status != 200 {
		t.Fatalf("resume: %d %s", r.Status, r.Raw)
	}
	time.Sleep(2500 * time.Millisecond)
	moved := 0
	now := prices()
	for s, p := range after {
		if now[s] != p {
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("prices did not start moving again after the break")
	}
	if r := buy("b3"); r.Status != 200 && r.Status != 429 {
		t.Fatalf("a trade after the break: %d %s", r.Status, r.Raw)
	}

	// ---- standings: only during Phase 1 trading ----
	if st := e.call("GET", "/api/leaderboard", inv, nil).Status; st != 200 {
		t.Fatalf("players during Phase 1 trading: standings %d", st)
	}
	post("/api/admin/clock/next", map[string]any{}) // Phase 1 closed
	if st := e.call("GET", "/api/leaderboard", inv, nil).Status; st != 403 {
		t.Fatalf("players after Phase 1 closed: standings %d, want 403 (organisers only)", st)
	}
	if st := e.call("GET", "/api/admin/standings", adm, nil).Status; st != 200 {
		t.Fatalf("the organisers' standings: %d", st)
	}

	// ---- Phase 2: a break during an allocation window, and a withdrawal comes out of the fund ----
	if r := post("/api/admin/qualification/run", map[string]any{"pairs": [][]string{{teams[0].id, teams[1].id}, {teams[2].id, teams[3].id}}}); r.Status != 200 {
		t.Fatalf("form: %d %s", r.Status, r.Raw)
	}
	post("/api/admin/clock/next", map[string]any{}) // window 0
	// Invest what the page says a fund can take now (the market moved, so the exact share is not a round number).
	room := func(fund string) float64 {
		for _, f := range e.fundsView(inv)["funds"].([]any) {
			if m := f.(map[string]any); m["id"] == fund {
				return math.Floor(num(m["room"]))
			}
		}
		return 0
	}
	invest := func(fund string, most float64) {
		t.Helper()
		amount := math.Min(most, room(fund))
		if r := e.call("POST", "/api/funds/"+fund+"/allocate", inv, map[string]any{"amount": amount}); r.Status != 200 {
			t.Fatalf("invest %v in %s: %d %s", amount, fund, r.Status, r.Raw)
		}
	}
	invest("F1", 25_000)
	post("/api/admin/clock/pause", map[string]any{})
	if r := e.call("POST", "/api/funds/F1/allocate", inv, map[string]any{"amount": 10_000}); r.Status != 423 || r.Body["error"] != "event_paused" {
		t.Fatalf("investing during a break: %d %s", r.Status, r.Raw)
	}
	post("/api/admin/clock/resume", map[string]any{})
	// enough in funds that taking some out still leaves the required 5%
	invest("F2", 25_000)
	invest("F1", 20_000)
	fundCash := func() float64 { return num(e.call("GET", "/api/funds/mine", teams[0].token, nil).Body["cash"]) }
	myCash := func() float64 { return num(e.call("GET", "/api/portfolio/me", inv, nil).Body["cashBalance"]) }
	f0, m0 := fundCash(), myCash()
	r := e.call("POST", "/api/funds/F1/redeem", inv, map[string]any{"amount": 10_000})
	if r.Status != 200 {
		t.Fatalf("withdraw: %d %s", r.Status, r.Raw)
	}
	got := num(r.Body["amount"])
	if d := f0 - fundCash(); d < got-0.01 || d > got+0.01 {
		t.Fatalf("the fund's cash fell by %v, want the %v withdrawn", d, got)
	}
	if d := myCash() - m0; d < got-0.01 || d > got+0.01 {
		t.Fatalf("the investor's cash rose by %v, want the %v withdrawn", d, got)
	}
}
