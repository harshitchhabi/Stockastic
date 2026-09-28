package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"stockastic/api/internal/store"
)

func join(e *env, code, name string) resp {
	return e.call("POST", "/api/auth/join", "", map[string]any{"teamCode": code, "name": name,
		"email": name + "@member.local", "password": "password-123"})
}

func teamCode(t *testing.T, e *env, leaderTok string) string {
	t.Helper()
	v := e.call("GET", "/api/team", leaderTok, nil).Body
	code, _ := v["joinCode"].(string)
	if len(code) != 8 {
		t.Fatalf("the leader should see an 8-letter team code, got %v", v)
	}
	return code
}

// Every person has their own login and sees the team's dashboard; only the leader trades unless trading is
// handed to a teammate; the server refuses everyone else, not just the screen.
func TestTeammatesSeeTheTeamButOnlyTheTraderTrades(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	lead, teamID := e.signup("Falcon")
	code := teamCode(t, e, lead)

	j := join(e, code, "asha")
	if j.Status != 200 {
		t.Fatalf("join: %d %s", j.Status, j.Raw)
	}
	asha := j.Body["token"].(string)
	acc := j.Body["account"].(map[string]any)
	if acc["id"] != teamID || acc["isLeader"] != false || acc["canTrade"] != false || acc["loginName"] != "asha" {
		t.Fatalf("a teammate's account = %v", acc)
	}
	// A teammate never sees the team code; only the leader shares it.
	if v := e.call("GET", "/api/team", asha, nil).Body; v["joinCode"] != nil || v["isLeader"] != false {
		t.Fatalf("teammate's team view = %v", v)
	}
	// Signing in again with their own email and password works.
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "asha@member.local", "password": "password-123"}); r.Status != 200 {
		t.Fatalf("teammate sign-in: %d %s", r.Status, r.Raw)
	}

	e.openMarket(adm)
	sym := "ACME"
	if r := e.call("POST", "/api/trades", asha, trade("buy", sym, 1)); r.Status != 403 || r.Body["error"] != "not_team_trader" {
		t.Fatalf("a teammate traded: %d %s", r.Status, r.Raw)
	}
	for _, p := range []string{"/api/funds/F1/allocate", "/api/funds/F1/redeem", "/api/strategy-log"} {
		if r := e.call("POST", p, asha, map[string]any{"amount": 5000, "text": "a plan that is long enough"}); r.Status != 403 {
			t.Fatalf("a teammate could POST %s: %d %s", p, r.Status, r.Raw)
		}
	}
	if r := e.call("PUT", "/api/funds/mine/profile", asha, map[string]any{"name": "X"}); r.Status != 403 {
		t.Fatalf("a teammate could edit the fund profile: %d", r.Status)
	}
	if r := e.call("POST", "/api/trades", lead, trade("buy", sym, 1)); r.Status != 200 {
		t.Fatalf("the leader could not trade: %d %s", r.Status, r.Raw)
	}
	// Both see the same portfolio.
	if a, b := e.call("GET", "/api/portfolio/me", lead, nil).Body, e.call("GET", "/api/portfolio/me", asha, nil).Body; num(a["cashBalance"]) != num(b["cashBalance"]) {
		t.Fatalf("leader and teammate see different cash: %v vs %v", a["cashBalance"], b["cashBalance"])
	}
	// A teammate can still report a problem.
	if r := e.call("POST", "/api/disputes", asha, map[string]any{"category": "other", "summary": "the chart froze for a minute"}); r.Status != 200 {
		t.Fatalf("a teammate could not raise a dispute: %d %s", r.Status, r.Raw)
	}
	// A teammate cannot take over trading; the leader can hand it over.
	aid := acc["memberId"].(string)
	if r := e.call("POST", "/api/team/trader", asha, map[string]any{"memberId": aid}); r.Status != 400 {
		t.Fatalf("a teammate made themselves the trader: %d", r.Status)
	}
	if r := e.call("POST", "/api/team/trader", lead, map[string]any{"memberId": aid}); r.Status != 200 {
		t.Fatalf("handing over trading: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", asha, trade("buy", sym, 1)); r.Status != 200 {
		t.Fatalf("the new trader could not trade: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", lead, trade("buy", sym, 1)); r.Status != 403 {
		t.Fatalf("the leader still traded after handing over: %d", r.Status)
	}
	if me := e.call("GET", "/api/auth/me", lead, nil).Body; me["canTrade"] != false || me["traderName"] != "asha" {
		t.Fatalf("leader's view after handing over = %v", me)
	}
	// The teammate's live connection works and belongs to the team.
	c := wsDial(t, e, asha)
	wsReady(t, c)
	c.Close()

	// A restart keeps teammates and who trades.
	e2 := e.restart(r)
	l := e2.call("POST", "/api/auth/login", "", map[string]any{"email": "asha@member.local", "password": "password-123"})
	if l.Status != 200 || l.Body["account"].(map[string]any)["canTrade"] != true {
		t.Fatalf("after a restart: %d %s", l.Status, l.Raw)
	}
}

func TestJoiningRules(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	lead, teamID := e.signup("Comet")
	code := teamCode(t, e, lead)

	if r := join(e, "WRONGCOD", "zed"); r.Status != 400 || r.Body["error"] != "wrong_team_code" {
		t.Fatalf("wrong code: %d %s", r.Status, r.Raw)
	}
	// Codes are forgiving about case, spaces and dashes (read aloud, typed on a phone).
	if r := join(e, " "+code[:4]+"-"+code[4:]+" ", "one"); r.Status != 200 {
		t.Fatalf("join with a spaced code: %d %s", r.Status, r.Raw)
	}
	if r := join(e, code, "two"); r.Status != 200 {
		t.Fatalf("second teammate: %d %s", r.Status, r.Raw)
	}
	if r := join(e, code, "three"); r.Status != 409 || r.Body["error"] != "team_full" {
		t.Fatalf("a fourth person joined a team of 3: %d %s", r.Status, r.Raw)
	}
	// One mailbox, one person: a team's email cannot join as a teammate, a teammate's cannot register a team.
	if r := e.call("POST", "/api/auth/join", "", map[string]any{"teamCode": code, "name": "dup", "email": "comet@test.local", "password": "password-123"}); r.Body["error"] != "email_taken" {
		t.Fatalf("the leader's email joined as a teammate: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/auth/signup", "", map[string]any{"displayName": "Dup", "email": "One+x@member.local", "password": "password-123"}); r.Body["error"] != "email_taken" {
		t.Fatalf("a teammate's email registered a team: %d %s", r.Status, r.Raw)
	}

	// The leader can change the code: the old one stops working.
	nc := e.call("POST", "/api/team/code", lead, nil).Body["joinCode"]
	if nc == nil || nc == code {
		t.Fatalf("new code = %v", nc)
	}

	// Organiser: removing a teammate ends their login and frees the place; signing the team out ends everyone's.
	m := e.call("GET", "/api/admin/accounts/"+teamID+"/members", adm, nil).Body
	members := m["members"].([]any)
	if len(members) != 2 {
		t.Fatalf("members = %v", m)
	}
	first := members[0].(map[string]any)
	tokOne := e.call("POST", "/api/auth/login", "", map[string]any{"email": "one@member.local", "password": "password-123"}).Body["token"].(string)
	tokTwo := e.call("POST", "/api/auth/login", "", map[string]any{"email": "two@member.local", "password": "password-123"}).Body["token"].(string)
	if r := e.call("POST", "/api/admin/accounts/"+teamID+"/trader", adm, map[string]any{"memberId": first["id"]}); r.Status != 200 {
		t.Fatalf("organiser choosing the trader: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/accounts/"+teamID+"/members/"+first["id"].(string)+"/remove", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("remove: %d %s", r.Status, r.Raw)
	}
	if r := e.call("GET", "/api/auth/me", tokOne, nil); r.Status != 401 {
		t.Fatalf("a removed teammate is still signed in: %d", r.Status)
	}
	if r := e.call("GET", "/api/auth/me", tokTwo, nil); r.Status != 200 {
		t.Fatalf("removing one teammate signed out another: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "one@member.local", "password": "password-123"}); r.Status != 401 {
		t.Fatalf("a removed teammate could sign in again: %d %s", r.Status, r.Raw)
	}
	if me := e.call("GET", "/api/auth/me", lead, nil).Body; me["canTrade"] != true {
		t.Fatalf("removing the trader should hand trading back to the leader: %v", me)
	}
	if r := join(e, nc.(string), "three"); r.Status != 200 {
		t.Fatalf("the freed place could not be filled: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/accounts/"+teamID+"/sign-out", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("sign out team: %d", r.Status)
	}
	if r := e.call("GET", "/api/auth/me", tokTwo, nil); r.Status != 401 {
		t.Fatalf("signing the team out left a teammate signed in: %d", r.Status)
	}
	// Locking the team shuts its teammates out too, until it is unlocked.
	if r := e.call("POST", "/api/admin/accounts/"+teamID+"/lock", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("lock: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "two@member.local", "password": "password-123"}); r.Status == 200 {
		t.Fatalf("a teammate signed in to a locked team")
	}
	e.call("POST", "/api/admin/accounts/"+teamID+"/unlock", adm, map[string]any{})
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "two@member.local", "password": "password-123"}); r.Status != 200 {
		t.Fatalf("after unlocking: %d %s", r.Status, r.Raw)
	}
}

// Several people racing to join the same team at the same moment: exactly as many get in as there are places.
func TestTeamPlacesHoldUnderARace(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	lead, _ := e.signup("Nova")
	code := teamCode(t, e, lead)
	var wg sync.WaitGroup
	var mu sync.Mutex
	ok := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			st, _ := postNoFatal(e, "/api/auth/join", map[string]any{"teamCode": code, "name": fmt.Sprintf("racer%d", i),
				"email": fmt.Sprintf("racer%d@member.local", i), "password": "password-123"})
			if st == 200 {
				mu.Lock()
				ok++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if ok != 2 {
		t.Fatalf("%d people joined a team of 3 (leader + 2 places)", ok)
	}
}

// postNoFatal is safe to call from several goroutines at once.
func postNoFatal(e *env, path string, body any) (int, map[string]any) {
	b, _ := json.Marshal(body)
	res, err := http.Post(e.srv.URL+path, "application/json", bytes.NewReader(b))
	if err != nil {
		return 0, nil
	}
	defer res.Body.Close()
	var m map[string]any
	_ = json.NewDecoder(res.Body).Decode(&m)
	return res.StatusCode, m
}
