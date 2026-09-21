package httpapi_test

import (
	"encoding/json"
	"testing"
	"time"

	"stockastic/api/internal/store"
)

func blk(id string, minutes int, stage string, open bool, window *int, snap string) map[string]any {
	return map[string]any{"id": id, "label": "Block " + id, "minutes": minutes, "stage": stage, "marketOpen": open, "allocationWindow": window, "freezeSnapshot": snap}
}

func ip(n int) *int { return &n }

func overviewStatus(e *env, adm string) (string, float64) {
	o := e.call("GET", "/api/admin/overview", adm, nil).Body
	c := o["clock"].(map[string]any)
	return c["status"].(string), c["blockIndex"].(float64)
}

// The organiser decides the schedule, where the event starts, when it pauses, who trades for a fund, and can
// reset everything. None of it follows the rulebook's timeline unless he loads it.
func TestOrganiserRunsTheEventHisWay(t *testing.T) {
	r := rb(t, 100)
	e := newEnv(t, store.NewMem(), r)
	adm := e.admin()
	var teams []team
	for _, n := range []string{"Alpha", "Beta", "Gamma", "Delta"} {
		tok, id := e.signup(n)
		teams = append(teams, team{n, tok, id})
		e.grant(adm, id, "ACME", 100)
	}
	alpha, beta, gamma := teams[0], teams[1], teams[2]

	// Only the organiser edits the schedule, and a broken one is refused with a reason.
	sched := map[string]any{"blocks": []any{
		blk("warm", 5, "phase1", false, nil, ""),
		blk("trade1", 30, "phase1", true, nil, ""),
		blk("win", 10, "phase2", false, ip(0), "phase1"),
		blk("trade2", 30, "phase2", true, nil, ""),
		blk("wrap", 5, "closing", false, nil, "final"),
	}}
	if r := e.call("PUT", "/api/admin/schedule", alpha.token, sched); r.Status != 403 {
		t.Fatalf("a team edited the schedule: %d", r.Status)
	}
	dup := map[string]any{"blocks": []any{blk("x", 5, "phase1", true, nil, ""), blk("x", 5, "phase1", true, nil, "")}}
	if r := e.call("PUT", "/api/admin/schedule", adm, dup); r.Status != 400 || r.Body["error"] != "invalid_schedule" {
		t.Fatalf("a schedule with a repeated block: %d %s", r.Status, r.Raw)
	}
	if r := e.call("PUT", "/api/admin/schedule", adm, sched); r.Status != 200 {
		t.Fatalf("set schedule: %d %s", r.Status, r.Raw)
	}
	ov := e.call("GET", "/api/admin/overview", adm, nil).Body
	if tl := ov["timeline"].([]any); len(tl) != 5 || tl[1].(map[string]any)["id"] != "trade1" {
		t.Fatalf("the console does not show the organiser's schedule: %v", ov["timeline"])
	}

	// Start in the middle: no need to go through the first blocks.
	if r := e.call("POST", "/api/admin/clock/start", adm, map[string]any{"blockId": "nope"}); r.Status != 400 {
		t.Fatalf("starting at a block that does not exist: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/clock/start", adm, map[string]any{"blockId": "trade1"}); r.Status != 200 {
		t.Fatalf("start at trade1: %d %s", r.Status, r.Raw)
	}
	if st, idx := overviewStatus(e, adm); st != "running" || idx != 1 {
		t.Fatalf("clock = %s at block %v, want running at block 1", st, idx)
	}
	if r := e.call("POST", "/api/trades", alpha.token, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("trading in the block he started at: %d %s", r.Status, r.Raw)
	}

	// Pause stops trading; resume brings it back.
	e.call("POST", "/api/admin/clock/pause", adm, map[string]any{})
	if r := e.call("POST", "/api/trades", alpha.token, trade("buy", "ACME", 1)); r.Status != 403 {
		t.Fatalf("a trade while paused: %d", r.Status)
	}
	e.call("POST", "/api/admin/clock/resume", adm, map[string]any{})
	if r := e.call("POST", "/api/trades", alpha.token, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("a trade after resume: %d", r.Status)
	}

	// The organiser picks the fund managers himself: Alpha and Beta are merged, and only Alpha trades.
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{alpha.id, alpha.id}}}); r.Status != 400 {
		t.Fatalf("a team paired with itself: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{alpha.id, beta.id}}}); r.Status != 200 {
		t.Fatalf("form a fund from chosen teams: %d %s", r.Status, r.Raw)
	}
	e.jump(adm, "win") // the window opens, and the "phase1" snapshot block passes
	if r := e.call("POST", "/api/funds/F1/allocate", gamma.token, map[string]any{"amount": 50_000}); r.Status != 200 {
		t.Fatalf("an investor putting money in: %d %s", r.Status, r.Raw)
	}
	e.jump(adm, "trade2")
	if r := e.call("POST", "/api/trades", alpha.token, trade("buy", "ACME", 10)); r.Status != 200 {
		t.Fatalf("the trader placing the fund's trade: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", beta.token, trade("buy", "ACME", 10)); r.Status != 403 || r.Body["error"] != "not_the_trader" {
		t.Fatalf("the other team placing a trade: %d %s", r.Status, r.Raw)
	}
	if my := e.call("GET", "/api/funds/mine", beta.token, nil).Body; my["canTrade"] != false || my["traderName"] != "Alpha" {
		t.Fatalf("the other team's fund desk: %v", my)
	}
	if my := e.call("GET", "/api/funds/mine", alpha.token, nil).Body; my["canTrade"] != true {
		t.Fatalf("the trader's fund desk: %v", my)
	}
	// He can hand the trading to the other team.
	if r := e.call("POST", "/api/admin/funds/F1/trader", adm, map[string]any{"accountId": gamma.id}); r.Status != 400 {
		t.Fatalf("a trader from outside the fund: %d", r.Status)
	}
	if r := e.call("POST", "/api/admin/funds/F1/trader", adm, map[string]any{"accountId": beta.id}); r.Status != 200 {
		t.Fatalf("switch trader: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", beta.token, trade("sell", "ACME", 5)); r.Status != 200 {
		t.Fatalf("the new trader: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", alpha.token, trade("sell", "ACME", 1)); r.Status != 403 {
		t.Fatalf("the old trader after the switch: %d", r.Status)
	}
	// Funds cannot be taken apart once someone has invested.
	if r := e.call("POST", "/api/admin/funds/dissolve", adm, map[string]any{}); r.Status != 400 || r.Body["error"] != "has_investors" {
		t.Fatalf("dissolving funds with investors: %d %s", r.Status, r.Raw)
	}

	before := e.call("GET", "/api/portfolio/me", gamma.token, nil).Body
	if len(before["fundPositions"].([]any)) != 1 {
		t.Fatalf("setup: %v", before)
	}

	// Reset everything, and it survives a restart.
	if r := e.call("POST", "/api/admin/event/reset", alpha.token, map[string]any{}); r.Status != 403 {
		t.Fatalf("a team reset the event: %d", r.Status)
	}
	if r := e.call("POST", "/api/admin/event/reset", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("reset: %d %s", r.Status, r.Raw)
	}
	check := func(e *env, when string) {
		t.Helper()
		if st, _ := overviewStatus(e, adm); st != "not_started" {
			t.Fatalf("%s: clock is %s, want not_started", when, st)
		}
		for _, tm := range teams {
			me := e.call("GET", "/api/auth/me", tm.token, nil).Body
			if num(me["cashBalance"]) != 1_000_000 || me["role"] != "investor" {
				t.Fatalf("%s: %s has cash %v as %v", when, tm.name, me["cashBalance"], me["role"])
			}
			pf := e.call("GET", "/api/portfolio/me", tm.token, nil).Body
			if len(pf["holdings"].([]any)) != 0 || len(pf["fundPositions"].([]any)) != 0 {
				t.Fatalf("%s: %s still holds %v", when, tm.name, pf)
			}
			if mine := list(e.call("GET", "/api/trades/mine", tm.token, nil).Raw); len(mine) != 0 {
				t.Fatalf("%s: %s still has trades", when, tm.name)
			}
		}
		if v := e.fundsView(gamma.token); v["formed"] == true {
			t.Fatalf("%s: funds survived: %v", when, v)
		}
		var af []any
		_ = json.Unmarshal(e.call("GET", "/api/admin/funds", adm, nil).Raw, &af)
		if len(af) != 0 {
			t.Fatalf("%s: %d funds survived", when, len(af))
		}
	}
	check(e, "after the reset")
	e2 := e.restart(r)
	check(e2, "after a restart")

	// The event can be run again from scratch, on a schedule that is still his.
	if tl := e2.call("GET", "/api/admin/overview", e2.admin(), nil).Body["timeline"].([]any); len(tl) != 5 {
		t.Fatalf("the schedule was lost by the reset: %v", tl)
	}
	adm2 := e2.admin()
	e2.call("POST", "/api/admin/clock/start", adm2, map[string]any{"blockId": "trade1"})
	if r := e2.call("POST", "/api/trades", alpha.token, trade("buy", "ACME", 3)); r.Status != 200 {
		t.Fatalf("trading after the reset: %d %s", r.Status, r.Raw)
	}
	if r := e2.call("POST", "/api/admin/schedule/template", adm2, map[string]any{}); r.Status != 200 {
		t.Fatalf("load the rulebook timeline: %d", r.Status)
	}
	if tl := e2.call("GET", "/api/admin/overview", adm2, nil).Body["timeline"].([]any); len(tl) != 17 {
		t.Fatalf("timeline after loading the template: %d blocks", len(tl))
	}
}

// A team that becomes a fund manager while it is signed in gets the fund managers' early news on the connection
// it already has, and gets it back when the funds are dissolved.
func TestRoleChangeReachesOpenSockets(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	a, aid := e.signup("Alpha")
	b, bid := e.signup("Beta")
	_ = b
	e.call("POST", "/api/admin/clock/start", adm, map[string]any{"blockId": "p2_t1"})
	time.Sleep(1300 * time.Millisecond)

	c := wsDial(t, e, a)
	defer c.Close()
	wsReady(t, c)
	frames := make(chan string, 64)
	go func() { // one reader for the whole test: a read that times out would end the connection
		for {
			var f struct {
				T string `json:"t"`
			}
			if err := c.ReadJSON(&f); err != nil {
				close(frames)
				return
			}
			frames <- f.T
		}
	}()
	gotNews := func(headline string, wait time.Duration) bool {
		e.call("POST", "/api/admin/news", adm, map[string]any{"kind": "news", "headline": headline})
		deadline := time.After(wait)
		for {
			select {
			case f, ok := <-frames:
				if !ok {
					return false
				}
				if f == "news" {
					return true
				}
			case <-deadline:
				return false
			}
		}
	}
	// An investor sees market news only after the fund managers' head start (60 seconds), so nothing arrives yet.
	if gotNews("first item", 1500*time.Millisecond) {
		t.Fatal("an investor got the news with no delay")
	}
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{aid, bid}}}); r.Status != 200 {
		t.Fatalf("form: %d %s", r.Status, r.Raw)
	}
	if !gotNews("second item", 3*time.Second) {
		t.Fatal("the new fund manager's open connection did not get the early news")
	}
	if r := e.call("POST", "/api/admin/funds/dissolve", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("dissolve: %d %s", r.Status, r.Raw)
	}
	if gotNews("third item", 1500*time.Millisecond) {
		t.Fatal("a team moved back to investor still gets the early news")
	}
	if me := e.call("GET", "/api/auth/me", a, nil).Body; me["role"] != "investor" {
		t.Fatalf("role after dissolving = %v", me["role"])
	}
}
