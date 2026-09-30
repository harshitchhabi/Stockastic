package httpapi_test

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
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
	r.Market.MaxSingleStockPercent = 0 // this test spends a whole fund on one company; the limit has its own test
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

	// Both teams' whole portfolios (cash and shares) went into their fund. Fund 1 is Team40 (400 ACME) and Team21
	// (210 ACME): it holds 610 ACME and both teams' cash, and its unit price starts at exactly 100.
	acme := e.price(investor.token, "ACME")
	f1 := fundNamed(e.fundsView(investor.token), "F1")
	if want := 2*1_000_000.0 + 610*acme; math.Abs(num(f1["aum"])-want) > 0.01 || num(f1["nav"]) != 100 {
		t.Fatalf("fund 1 at formation: aum %v nav %v, want aum %v (both teams' portfolios) and nav 100", f1["aum"], f1["nav"], want)
	}
	if h := e.call("GET", "/api/funds/mine", teams[39].token, nil).Body["holdings"].([]any); len(h) != 1 || num(h[0].(map[string]any)["qty"]) != 610 {
		t.Fatalf("fund 1 should hold both teams' 610 ACME, holds %v", h)
	}

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
	if h := my["holdings"].([]any); len(h) != 1 || num(h[0].(map[string]any)["qty"]) != 610+950 {
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

	// Strategy logs (for the judges; prizes are decided by the organisers).
	e.jump(adm, "p2_t2")
	if r := e.call("POST", "/api/strategy-log", investor.token, map[string]any{"text": "Bought defensives before the bear run."}); r.Status != 200 {
		t.Fatalf("strategy log: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/strategy-log", mgr.token, map[string]any{"text": "x"}); r.Status != 400 {
		t.Fatalf("a fund manager writing a strategy log: %d", r.Status)
	}
	if lg := e.call("GET", "/api/admin/strategy-logs", adm, nil); lg.Status != 200 || len(lg.Body["entrants"].([]any)) != 1 {
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

// Taking the funds apart gives every team back exactly the cash and shares it brought, and survives a restart; once
// a fund has traded it is refused, because the portfolios could no longer be given back as they were.
func TestDissolvingGivesEachTeamItsPortfolioBack(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	r.Market.MaxSingleStockPercent = 0
	e := newEnv(t, wal, r)
	adm := e.admin()
	var teams []team
	for i := 0; i < 4; i++ {
		tok, id := e.signup(fmt.Sprintf("Dis%d", i))
		teams = append(teams, team{fmt.Sprintf("Dis%d", i), tok, id})
	}
	e.openMarket(adm)
	for i, tm := range teams {
		e.grant(adm, tm.id, "ACME", 10*(i+1))
	}
	if r := e.call("POST", "/api/trades", teams[0].token, trade("buy", "GLOBEX", 7)); r.Status != 200 {
		t.Fatalf("trade: %d %s", r.Status, r.Raw)
	}
	holdings := func(e *env, id string) (float64, string) {
		d := e.call("GET", "/api/admin/accounts/"+id, adm, nil).Body
		return num(d["wallet"].(map[string]any)["cash"]), fmt.Sprint(d["holdings"])
	}
	c0, h0 := holdings(e, teams[0].id)
	e.jump(adm, "p1_freeze")
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{teams[0].id, teams[1].id}, {teams[2].id, teams[3].id}}}); r.Status != 200 {
		t.Fatalf("form: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/funds/dissolve", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("dissolve: %d %s", r.Status, r.Raw)
	}
	e2 := e.restart(r)
	adm = e2.admin()
	if c, h := holdings(e2, teams[0].id); c != c0 || h != h0 {
		t.Fatalf("after taking the funds apart and a restart, team 0 has cash %v holdings %s; it had %v %s", c, h, c0, h0)
	}
	// Formed again, and this time a fund trades: taking them apart is then refused.
	if r := e2.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{teams[0].id, teams[1].id}, {teams[2].id, teams[3].id}}}); r.Status != 200 {
		t.Fatalf("form again: %d %s", r.Status, r.Raw)
	}
	e2.jump(adm, "p2_t1")
	lead := e2.call("POST", "/api/auth/login", "", map[string]any{"email": "dis0@test.local", "password": "password-123"}).Body["token"].(string)
	if r := e2.call("POST", "/api/trades", lead, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("the fund's trade: %d %s", r.Status, r.Raw)
	}
	if r := e2.call("POST", "/api/admin/funds/dissolve", adm, map[string]any{}); r.Status != 400 || r.Body["error"] != "funds_traded" {
		t.Fatalf("taking apart funds that have traded: %d %s", r.Status, r.Raw)
	}
}

// Each fund chooses its own management fee from 1% to 2%; investors see it; it cannot change once investors are in.
func TestFundsChooseTheirFee(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	var teams []team
	for i := 0; i < 5; i++ {
		tok, id := e.signup(fmt.Sprintf("Fee%d", i))
		teams = append(teams, team{fmt.Sprintf("Fee%d", i), tok, id})
	}
	e.openMarket(adm)
	e.jump(adm, "p1_freeze")
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{teams[0].id, teams[1].id}, {teams[2].id, teams[3].id}}}); r.Status != 200 {
		t.Fatalf("form: %d %s", r.Status, r.Raw)
	}
	mgr, inv := teams[0].token, teams[4].token
	profile := func(fee float64) resp {
		return e.call("PUT", "/api/funds/mine/profile", mgr, map[string]any{"name": "Kestrel Capital", "risk": "Balanced", "strategy": "Value", "managementFeePercent": fee})
	}
	if f := fundNamed(e.fundsView(inv), "F1"); num(f["feePercent"]) != 1.5 {
		t.Fatalf("before choosing, the fee is %v, want the rulebook's 1.5", f["feePercent"])
	}
	// the fund's other team sees the fund but only the trading team sets its profile and fee
	if r := e.call("PUT", "/api/funds/mine/profile", teams[1].token, map[string]any{"name": "Other Capital", "risk": "Balanced", "strategy": "Value", "managementFeePercent": 1.1}); r.Status != 400 || r.Body["error"] != "not_fund_trader" {
		t.Fatalf("the fund's other team changing the profile: %d %s", r.Status, r.Raw)
	}
	if r := profile(2.5); r.Status != 400 || r.Body["error"] != "invalid_fee" {
		t.Fatalf("a 2.5%% fee: %d %s", r.Status, r.Raw)
	}
	if r := profile(1.8); r.Status != 200 {
		t.Fatalf("a 1.8%% fee: %d %s", r.Status, r.Raw)
	}
	if f := fundNamed(e.fundsView(inv), "F1"); num(f["feePercent"]) != 1.8 {
		t.Fatalf("investors see a fee of %v, want 1.8", f["feePercent"])
	}
	e.jump(adm, "transition")
	time.Sleep(5 * time.Second) // the profile can change at most every few seconds
	if r := e.call("POST", "/api/funds/F1/allocate", inv, map[string]any{"amount": 25_000}); r.Status != 200 {
		t.Fatalf("invest: %d %s", r.Status, r.Raw)
	}
	// the fund's managers see who invested and how much
	desk := e.call("GET", "/api/funds/mine", mgr, nil).Body
	inv0 := desk["investors"].([]any)
	if len(inv0) != 1 || inv0[0].(map[string]any)["team"] != "Fee4" || num(inv0[0].(map[string]any)["contributed"]) != 25_000 {
		t.Fatalf("the fund's investors as its managers see them: %v", desk["investors"])
	}
	if _, ok := desk["feesEarned"]; !ok {
		t.Fatalf("the desk does not show the fees earned: %v", desk)
	}
	if r := profile(1.2); r.Status != 400 || r.Body["error"] != "fee_locked" {
		t.Fatalf("changing the fee after investors came in: %d %s", r.Status, r.Raw)
	}
	if r := profile(1.8); r.Status != 200 {
		t.Fatalf("keeping the same fee while editing the rest: %d %s", r.Status, r.Raw)
	}
}

// The organisers run the event step by step: nothing moves by itself, Window 0 waits for the funds, and players see
// the standings in Phase 1 only.
func TestTheEventMovesOnlyWhenTheOrganiserSaysSo(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	var teams []team
	for i := 0; i < 4; i++ {
		tok, id := e.signup(fmt.Sprintf("Step%d", i))
		teams = append(teams, team{fmt.Sprintf("Step%d", i), tok, id})
	}
	player := teams[3].token
	next := func() resp { return e.call("POST", "/api/admin/clock/next", adm, map[string]any{}) }
	step := func() map[string]any {
		return e.call("GET", "/api/admin/overview", adm, nil).Body["clock"].(map[string]any)
	}
	standings := func() int { return e.call("GET", "/api/leaderboard", player, nil).Status }

	if standings() != 200 {
		t.Fatal("players should see the standings before and during Phase 1")
	}
	if r := next(); r.Status != 200 {
		t.Fatalf("start: %d %s", r.Status, r.Raw)
	}
	time.Sleep(1200 * time.Millisecond)
	if !e.a.Clock.MarketOpen() || step()["blockIndex"].(float64) != 0 {
		t.Fatal("the first step must be Phase 1 with the market open")
	}
	e.a.Clock.Tick()
	time.Sleep(1500 * time.Millisecond)
	if !e.a.Clock.MarketOpen() {
		t.Fatal("a step must not end by itself")
	}
	if r := next(); r.Status != 200 { // Phase 1 closed: results frozen
		t.Fatalf("to Phase 1 closed: %d %s", r.Status, r.Raw)
	}
	if e.a.Clock.MarketOpen() {
		t.Fatal("the market must close when Phase 1 ends")
	}
	if q := e.call("GET", "/api/admin/qualification", adm, nil).Body; q["ready"] != true {
		t.Fatalf("the Phase 1 results must be frozen at this step: %v", q)
	}
	if standings() != 403 {
		t.Fatal("once Phase 1 closes only the organisers see the standings: they announce the results")
	}
	if r := next(); r.Status != 400 || r.Body["error"] != "funds_not_formed" {
		t.Fatalf("opening window 0 before the funds exist: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{teams[0].id, teams[1].id}}}); r.Status != 200 {
		t.Fatalf("form: %d %s", r.Status, r.Raw)
	}
	if r := next(); r.Status != 200 {
		t.Fatalf("to window 0: %d %s", r.Status, r.Raw)
	}
	if !e.a.Clock.WindowOpen(0) || e.a.Clock.MarketOpen() {
		t.Fatal("window 0 must be open and the market closed")
	}
	if standings() != 403 {
		t.Fatal("players must not see the standings in Phase 2")
	}
	cs := e.a.ControlState()
	if cs.Stage != "transition" || cs.OpenWindow != 0 {
		t.Fatalf("what players are told: %+v", cs)
	}
	if r := next(); r.Status != 200 || !e.a.Clock.MarketOpen() {
		t.Fatalf("to Phase 2 trading: %d %s", r.Status, r.Raw)
	}
}

// Next step names the step it is for: if two organisers press it together, or one press arrives twice, the event
// moves one step, never two.
func TestNextStepCannotSkipAStep(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	step := func() float64 {
		return num(e.call("GET", "/api/admin/overview", adm, nil).Body["clock"].(map[string]any)["blockIndex"])
	}
	if r := e.call("POST", "/api/admin/clock/next", adm, map[string]any{"blockId": "p1_trading"}); r.Status != 200 {
		t.Fatalf("start: %d %s", r.Status, r.Raw)
	}
	var wg sync.WaitGroup
	var ok atomic.Int64
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req, _ := http.NewRequest("POST", e.srv.URL+"/api/admin/clock/next", strings.NewReader(`{"blockId":"p1_freeze"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+adm)
			if res, err := http.DefaultClient.Do(req); err == nil {
				if res.StatusCode == 200 {
					ok.Add(1)
				}
				res.Body.Close()
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || step() != 1 {
		t.Fatalf("10 presses of Next step for the same step: %d went through, now on step %v (want 1 and step index 1)", ok.Load(), step())
	}
	if r := e.call("POST", "/api/admin/clock/next", adm, map[string]any{"blockId": "p1_freeze"}); r.Status != 400 || r.Body["error"] != "step_changed" {
		t.Fatalf("a stale press: %d %s", r.Status, r.Raw)
	}
}

// When the last allocation window opens everyone is told it is the last one, and the Funds page knows it; after the
// final close it knows the event has closed.
func TestTheLastWindowIsAnnounced(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	var teams []team
	for i := 0; i < 5; i++ {
		tok, id := e.signup(fmt.Sprintf("Last%d", i))
		teams = append(teams, team{fmt.Sprintf("Last%d", i), tok, id})
	}
	inv := teams[4].token
	next := func() {
		if r := e.call("POST", "/api/admin/clock/next", adm, map[string]any{}); r.Status != 200 {
			t.Fatalf("next: %d %s", r.Status, r.Raw)
		}
	}
	next()
	next()
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{teams[0].id, teams[1].id}, {teams[2].id, teams[3].id}}}); r.Status != 200 {
		t.Fatalf("form: %d %s", r.Status, r.Raw)
	}
	lastNotice := func() bool {
		return strings.Contains(string(e.call("GET", "/api/news", inv, nil).Raw), "it is the last one")
	}
	for i := 0; i < 6; i++ { // window 0, trading, window 1, trading, window 2, trading
		next()
		if lastNotice() {
			t.Fatalf("a last-window notice went out at step %d, before the last window", i+3)
		}
	}
	next() // window 3
	v := e.fundsView(inv)
	if !lastNotice() || v["windowOpen"] != true || num(v["window"]) != num(v["lastWindow"]) {
		t.Fatalf("the last window: notice %v, view %v", lastNotice(), v)
	}
	next()
	next() // final close
	if v := e.fundsView(inv); v["closed"] != true {
		t.Fatalf("after the final close the Funds page does not know the event has closed: %v", v["closed"])
	}
}
