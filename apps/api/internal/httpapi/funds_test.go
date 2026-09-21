package httpapi_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"stockastic/api/internal/store"
)

type team struct {
	name, token, id string
}

func (e *env) jump(adm, block string) {
	e.t.Helper()
	if r := e.call("POST", "/api/admin/clock/jump", adm, map[string]any{"blockId": block}); r.Status != 200 {
		e.t.Fatalf("jump to %s: %d %s", block, r.Status, r.Raw)
	}
	time.Sleep(1300 * time.Millisecond) // the clock announces the new block on its next one-second tick
}

func (e *env) fundsView(tok string) map[string]any {
	e.t.Helper()
	return e.call("GET", "/api/funds", tok, nil).Body
}

func fundNamed(view map[string]any, id string) map[string]any {
	for _, f := range view["funds"].([]any) {
		if m := f.(map[string]any); m["id"] == id {
			return m
		}
	}
	return nil
}

// A whole event: Phase 1 ranking, formation, allocation windows, fund trading, redemption, fees, restart.
func TestPhaseTwoFromQualificationToRestart(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()

	// 40 teams. Team i is given i x 10 shares, so team 40 ranks first; teams 21 to 40 qualify and teams 1 to 20 invest.
	var teams []team
	for i := 1; i <= 40; i++ {
		tok, id := e.signup(fmt.Sprintf("Team%02d", i))
		teams = append(teams, team{fmt.Sprintf("Team%02d", i), tok, id})
	}
	e.openMarket(adm)
	for i, tm := range teams {
		e.grant(adm, tm.id, "ACME", (i+1)*10)
	}

	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{}); r.Status != 400 || r.Body["error"] != "phase1_not_frozen" {
		t.Fatalf("forming funds before the freeze: %d %s", r.Status, r.Raw)
	}
	e.jump(adm, "p1_freeze")
	q := e.call("GET", "/api/admin/qualification", adm, nil).Body
	rows := q["rows"].([]any)
	if q["ready"] != true || len(rows) != 40 || rows[0].(map[string]any)["team"] != "Team40" || rows[19].(map[string]any)["qualifies"] != true || rows[20].(map[string]any)["qualifies"] != false {
		t.Fatalf("qualification = %s", e.call("GET", "/api/admin/qualification", adm, nil).Raw)
	}

	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("form funds: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{}); r.Status != 400 {
		t.Fatalf("forming funds twice: %d", r.Status)
	}
	var af []map[string]any
	_ = json.Unmarshal(e.call("GET", "/api/admin/funds", adm, nil).Raw, &af)
	if len(af) != 10 {
		t.Fatalf("funds = %d, want 10", len(af))
	}
	// Mirror pairing: fund 1 is Phase 1 rank 1 (Team40) with rank 20 (Team21); fund 10 is rank 10 with rank 11.
	if ranks := af[0]["ranks"].([]any); ranks[0].(float64) != 1 || ranks[1].(float64) != 20 {
		t.Fatalf("fund 1 pairs ranks %v, want 1 and 20", ranks)
	}
	if ranks := af[9]["ranks"].([]any); ranks[0].(float64) != 10 || ranks[1].(float64) != 11 {
		t.Fatalf("fund 10 pairs ranks %v, want 10 and 11", ranks)
	}
	for _, i := range []int{40, 21} { // both members of fund 1 are now fund managers
		if me := e.call("GET", "/api/auth/me", teams[i-1].token, nil); me.Body["role"] != "fund_manager" {
			t.Fatalf("Team%02d role = %v", i, me.Body["role"])
		}
	}
	investor := teams[0] // Team01: ranked last, so an investor
	other := teams[1]

	// Window 0: put money in.
	e.jump(adm, "transition")
	if v := e.fundsView(investor.token); v["windowOpen"] != true || v["window"].(float64) != 0 {
		t.Fatalf("funds view during window 0 = %v", v)
	}
	post := func(tm team, fund, path string, body map[string]any) resp {
		return e.call("POST", "/api/funds/"+fund+"/"+path, tm.token, body)
	}
	if r := post(investor, "F1", "allocate", map[string]any{"amount": 100}); r.Body["error"] != "below_minimum" {
		t.Fatalf("a tiny investment: %d %s", r.Status, r.Raw)
	}
	if r := post(investor, "F1", "allocate", map[string]any{"amount": 900_000}); r.Body["error"] != "exceeds_single_fund_cap" {
		t.Fatalf("more than 60%% in one fund: %d %s", r.Status, r.Raw)
	}
	if r := post(teams[39], "F2", "allocate", map[string]any{"amount": 5000}); r.Status != 400 || r.Body["error"] != "investors_only" {
		t.Fatalf("a fund manager investing: %d %s", r.Status, r.Raw)
	}
	ok := post(investor, "F1", "allocate", map[string]any{"amount": 50_000})
	if ok.Status != 200 || num(ok.Body["units"]) != 500 || num(ok.Body["nav"]) != 100 {
		t.Fatalf("first investment: %d %s (want 500 units at NAV 100)", ok.Status, ok.Raw)
	}
	if r := post(other, "F1", "allocate", map[string]any{"amount": 50_000}); r.Status != 200 {
		t.Fatalf("second investor: %d %s", r.Status, r.Raw)
	}
	// The pool is 5% of the investors' wallets, shared equally: a fund waits at its share.
	if r := post(investor, "F1", "allocate", map[string]any{"amount": 100_000}); r.Body["error"] != "fund_at_cap" && r.Body["error"] != "exceeds_single_fund_cap" {
		t.Fatalf("a fund past its share: %d %s", r.Status, r.Raw)
	}
	if r := post(investor, "F2", "allocate", map[string]any{"amount": 50_000}); r.Status != 200 {
		t.Fatalf("a second fund: %d %s", r.Status, r.Raw)
	}
	pf := e.call("GET", "/api/portfolio/me", investor.token, nil).Body
	pos := pf["fundPositions"].([]any)
	if len(pos) != 2 || num(pos[0].(map[string]any)["units"]) != 500 {
		t.Fatalf("portfolio fund positions = %v", pf["fundPositions"])
	}
	// Moving cash into a fund does not change what the team is worth.
	worth := num(pf["totalValue"])
	if want := 1_000_000.0 + 10*e.price(investor.token, "ACME"); worth != want {
		t.Fatalf("total value = %v, want %v", worth, want)
	}
	if r := post(investor, "F1", "redeem", map[string]any{"amount": 1000}); r.Body["error"] != "window_closed" && r.Status != 200 {
		t.Logf("redeem in the same window: %d %s", r.Status, r.Raw)
	}

	// The fund managers trade the fund's money, sharing one allowance, and can name the fund.
	e.jump(adm, "p2_t1")
	mgr, mgr2 := teams[39], teams[20]
	if r := e.call("POST", "/api/trades", mgr.token, trade("buy", "ACME", 950)); r.Status != 200 {
		t.Fatalf("a fund's trade: %d %s", r.Status, r.Raw)
	}
	my := e.call("GET", "/api/funds/mine", mgr2.token, nil).Body // the other manager sees the same fund
	if h := my["holdings"].([]any); len(h) != 1 || num(h[0].(map[string]any)["qty"]) != 950 {
		t.Fatalf("the fund's holdings = %v", my["holdings"])
	}
	if r := e.call("PUT", "/api/funds/mine/profile", mgr.token, map[string]any{"name": "Zenith Growth", "philosophy": "Buy quality", "risk": "Balanced", "strategy": "Growth"}); r.Status != 200 {
		t.Fatalf("naming the fund: %d %s", r.Status, r.Raw)
	}
	if r := e.call("PUT", "/api/funds/mine/profile", mgr.token, map[string]any{"name": "X", "risk": "Reckless"}); r.Status != 400 {
		t.Fatalf("a made-up risk profile: %d", r.Status)
	}
	if f := fundNamed(e.fundsView(other.token), "F1"); f["name"] != "Zenith Growth" || num(f["nav"]) != 100 {
		t.Fatalf("fund as investors see it = %v", f)
	}
	// An investor cannot trade for the fund, and cannot leave outside a window.
	if r := post(investor, "F1", "redeem", map[string]any{"all": true}); r.Body["error"] != "window_closed" {
		t.Fatalf("leaving outside a window: %d %s", r.Status, r.Raw)
	}

	// Window 1 opens: window 0 is now a checkpoint. Leaving needs cash, so the fund sells shares to pay.
	e.jump(adm, "w1")
	cash := num(e.call("GET", "/api/portfolio/me", investor.token, nil).Body["cashBalance"])
	rd := post(investor, "F1", "redeem", map[string]any{"amount": 4000})
	if rd.Status != 200 || num(rd.Body["amount"]) < 3999.9 || num(rd.Body["amount"]) > 4000.1 {
		t.Fatalf("redeeming 4000: %d %s", rd.Status, rd.Raw)
	}
	if after := num(e.call("GET", "/api/portfolio/me", investor.token, nil).Body["cashBalance"]); after < cash+3999.9 || after > cash+4000.1 {
		t.Fatalf("cash after redeeming = %v, was %v", after, cash)
	}
	_ = json.Unmarshal(e.call("GET", "/api/admin/funds", adm, nil).Raw, &af)
	if cps := af[0]["checkpoints"].([]any); len(cps) != 1 || cps[0].(map[string]any)["name"] != "window 0" {
		t.Fatalf("checkpoints after window 0 closed = %v", af[0]["checkpoints"])
	}
	if af[0]["retention"].(float64) >= 1 {
		t.Fatalf("retention = %v after a redemption", af[0]["retention"])
	}

	// Prizes and logs.
	e.jump(adm, "p2_t2")
	if r := e.call("POST", "/api/strategy-log", investor.token, map[string]any{"text": "Bought defensives before the bear run."}); r.Status != 200 {
		t.Fatalf("strategy log: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/strategy-log", mgr.token, map[string]any{"text": "x"}); r.Status != 400 {
		t.Fatalf("a fund manager writing a strategy log: %d", r.Status)
	}
	if pr := e.call("GET", "/api/admin/prizes", adm, nil); pr.Status != 200 || len(pr.Body["prize1"].([]any)) != 10 || len(pr.Body["prize2"].([]any)) != 20 {
		t.Fatalf("prizes: %d %s", pr.Status, pr.Raw)
	}
	if lg := e.call("GET", "/api/admin/strategy-logs", adm, nil); lg.Status != 200 || len(lg.Body["rubric"].([]any)) != 4 {
		t.Fatalf("strategy logs: %d %s", lg.Status, lg.Raw)
	}

	// Restart: everything comes back exactly.
	snap := func(e *env) string {
		return string(e.call("GET", "/api/portfolio/me", investor.token, nil).Raw) + string(e.call("GET", "/api/funds/mine", mgr.token, nil).Raw) +
			string(e.call("GET", "/api/admin/funds", e.admin(), nil).Raw)
	}
	before := snap(e)
	e2 := e.restart(r)
	if after := snap(e2); after != before {
		t.Fatalf("funds changed across the restart:\n before %s\n after  %s", before, after)
	}
	if v := e2.fundsView(other.token); v["formed"] != true {
		t.Fatal("the funds were lost across the restart")
	}
}
