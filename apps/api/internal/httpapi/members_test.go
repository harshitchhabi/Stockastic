package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

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

// The watchlist belongs to the team: a star from one teammate shows for the others, other teams do not see it,
// unknown companies are dropped, and it survives a restart. The leader's own name is shown to teammates.
func TestTeamWatchlistAndLeaderName(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	lead := e.call("POST", "/api/auth/signup", "", map[string]any{"displayName": "Orbit", "email": "orbit@test.local", "password": "password-123", "yourName": "Ravi Kumar"})
	if lead.Status != 200 {
		t.Fatalf("signup: %d %s", lead.Status, lead.Raw)
	}
	lt := lead.Body["token"].(string)
	code := teamCode(t, e, lt)
	mate := join(e, code, "meera").Body["token"].(string)
	other, _ := e.signup("Other")

	if v := e.call("GET", "/api/team", mate, nil).Body; v["leaderName"] != "Ravi Kumar" {
		t.Fatalf("the teammate sees the leader as %v", v["leaderName"])
	}
	if me := e.call("GET", "/api/auth/me", mate, nil).Body; me["traderName"] != "Ravi Kumar" {
		t.Fatalf("trader name = %v", me["traderName"])
	}
	if r := e.call("PUT", "/api/watchlist", mate, map[string]any{"symbols": []string{"acme", "GLOBEX", "NOPE", "ACME"}}); r.Status != 200 {
		t.Fatalf("set watchlist: %d %s", r.Status, r.Raw)
	}
	got := fmt.Sprint(e.call("GET", "/api/watchlist", lt, nil).Body["symbols"])
	if got != "[ACME GLOBEX]" {
		t.Fatalf("the leader sees %s, want the teammate's stars [ACME GLOBEX]", got)
	}
	if o := fmt.Sprint(e.call("GET", "/api/watchlist", other, nil).Body["symbols"]); o != "[]" {
		t.Fatalf("another team sees %s", o)
	}
	e2 := e.restart(r)
	l2 := e2.call("POST", "/api/auth/login", "", map[string]any{"email": "orbit@test.local", "password": "password-123"}).Body["token"].(string)
	if got := fmt.Sprint(e2.call("GET", "/api/watchlist", l2, nil).Body["symbols"]); got != "[ACME GLOBEX]" {
		t.Fatalf("after a restart: %s", got)
	}
	// A teammate cannot rename the leader; the leader can.
	if r := e2.call("POST", "/api/team/leader-name", e2.call("POST", "/api/auth/login", "", map[string]any{"email": "meera@member.local", "password": "password-123"}).Body["token"].(string), map[string]any{"name": "X Y"}); r.Status != 400 {
		t.Fatalf("a teammate renamed the leader: %d", r.Status)
	}
	if r := e2.call("POST", "/api/team/leader-name", l2, map[string]any{"name": "Ravi K"}); r.Status != 200 {
		t.Fatalf("leader rename: %d %s", r.Status, r.Raw)
	}
}

// Each person's own connection shows them online, separately from their teammates.
func TestEachTeammateShowsOnlineSeparately(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	lead, _ := e.signup("Pulse")
	mate := join(e, teamCode(t, e, lead), "kiran").Body["token"].(string)
	online := func() (bool, bool) {
		v := e.call("GET", "/api/team", lead, nil).Body
		ms := v["members"].([]any)
		return v["leaderOnline"] == true, ms[0].(map[string]any)["online"] == true
	}
	if l, m := online(); l || m {
		t.Fatalf("nobody is connected yet, got leader %v, teammate %v", l, m)
	}
	c := wsDial(t, e, mate)
	wsReady(t, c)
	if l, m := online(); l || !m {
		t.Fatalf("only the teammate is connected, got leader %v, teammate %v", l, m)
	}
	c2 := wsDial(t, e, lead)
	wsReady(t, c2)
	if l, m := online(); !l || !m {
		t.Fatalf("both are connected, got leader %v, teammate %v", l, m)
	}
	c.Close()
	time.Sleep(300 * time.Millisecond)
	if l, m := online(); !l || m {
		t.Fatalf("the teammate left, got leader %v, teammate %v", l, m)
	}
	c2.Close()
}

// The rules players read: written from the rulebook until an organiser edits them; only organisers can edit; an
// edit survives a restart; empty text brings the default back. None of this changes the game itself.
func TestRulesShownToPlayers(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	tok, _ := e.signup("Reader")
	def := e.call("GET", "/api/rules", tok, nil).Body
	text, _ := def["text"].(string)
	for _, want := range []string{"# Trading", "₹10,00,000", "25%", "60 seconds", "at least 5%"} {
		if !strings.Contains(text, want) {
			t.Fatalf("the default rules do not mention %q:\n%s", want, text)
		}
	}
	if def["edited"] != false {
		t.Fatalf("edited = %v", def["edited"])
	}
	if r := e.call("POST", "/api/admin/rules", tok, map[string]any{"text": "# Mine"}); r.Status != 403 {
		t.Fatalf("a team edited the rules: %d", r.Status)
	}
	if r := e.call("POST", "/api/admin/rules", adm, map[string]any{"text": "# Our rules\n- Be kind."}); r.Status != 200 {
		t.Fatalf("organiser edit: %d %s", r.Status, r.Raw)
	}
	e2 := e.restart(r)
	tok2 := e2.call("POST", "/api/auth/login", "", map[string]any{"email": "reader@test.local", "password": "password-123"}).Body["token"].(string)
	if got := e2.call("GET", "/api/rules", tok2, nil).Body; got["text"] != "# Our rules\n- Be kind." || got["edited"] != true {
		t.Fatalf("after a restart the rules are %v", got)
	}
	if r := e2.call("POST", "/api/admin/rules", e2.admin(), map[string]any{"text": strings.Repeat("x", 30001)}); r.Status != 400 {
		t.Fatalf("an over-long text was accepted: %d", r.Status)
	}
	e2.call("POST", "/api/admin/rules", e2.admin(), map[string]any{"text": ""})
	if got := e2.call("GET", "/api/rules", tok2, nil).Body; got["edited"] != false || !strings.Contains(got["text"].(string), "# Trading") {
		t.Fatalf("empty text did not bring the default back: %v", got["edited"])
	}
}

// The live-connection limit is per person: a leader with many tabs replaces only their own oldest, and never pushes
// a teammate's screen off.
func TestOneTeammatesTabsNeverPushAnotherOff(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	e.a.Hub.SetLimits(0, 3)
	lead, _ := e.signup("Tabs")
	mate := join(e, teamCode(t, e, lead), "tara").Body["token"].(string)
	m := wsDial(t, e, mate)
	wsReady(t, m)
	var tabs []*websocket.Conn
	for i := 0; i < 6; i++ {
		c := wsDial(t, e, lead)
		wsReady(t, c)
		tabs = append(tabs, c)
	}
	time.Sleep(300 * time.Millisecond)
	if n := e.a.Hub.LoginsOnline(e.call("GET", "/api/auth/me", lead, nil).Body["id"].(string)); n[""] != 3 || n[teammateID(t, e, lead)] != 1 {
		t.Fatalf("connections per person after the leader opened 6 tabs (limit 3): %v", n)
	}
	// the teammate's socket still works
	_ = m.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err := m.WriteJSON(map[string]any{"t": "ping"}); err != nil {
		t.Fatalf("the teammate's screen was pushed off: %v", err)
	}
	if _, _, err := m.ReadMessage(); err != nil {
		t.Fatalf("the teammate's screen was pushed off: %v", err)
	}
	for _, c := range tabs {
		c.Close()
	}
	m.Close()
}

func teammateID(t *testing.T, e *env, leaderTok string) string {
	t.Helper()
	ms := e.call("GET", "/api/team", leaderTok, nil).Body["members"].([]any)
	return ms[0].(map[string]any)["id"].(string)
}

// Signing out cancels that one sign-in on the server: a copy of its token stops working at once, for pages and for
// the live connection, and stays cancelled after a restart. The same person's other device and their teammates stay
// signed in, and signing in again works.
func TestSigningOutCancelsThatSignInOnly(t *testing.T) {
	r := rb(t, 100)
	e := newEnv(t, store.NewMem(), r)
	laptop, _ := e.signup("Session")
	code := teamCode(t, e, laptop)
	mate := join(e, code, "sam").Body["token"].(string)
	phone := e.call("POST", "/api/auth/login", "", map[string]any{"email": "session@test.local", "password": "password-123"}).Body["token"].(string)

	if r := e.call("POST", "/api/auth/logout", laptop, nil); r.Status != 200 {
		t.Fatalf("sign out: %d %s", r.Status, r.Raw)
	}
	if r := e.call("GET", "/api/auth/me", laptop, nil); r.Status != 401 {
		t.Fatalf("the signed-out token still works: %d", r.Status)
	}
	if !wsRefused(t, e, laptop) {
		t.Fatal("the signed-out token opened a live connection")
	}
	for name, tok := range map[string]string{"the same person's phone": phone, "a teammate": mate} {
		if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 200 {
			t.Fatalf("%s was signed out too: %d", name, r.Status)
		}
	}
	e2 := e.restart(r)
	if r := e2.call("GET", "/api/auth/me", laptop, nil); r.Status != 401 {
		t.Fatalf("after a restart the signed-out token works again: %d", r.Status)
	}
	if r := e2.call("GET", "/api/auth/me", phone, nil); r.Status != 200 {
		t.Fatalf("after a restart the phone was signed out: %d", r.Status)
	}
	again := e2.call("POST", "/api/auth/login", "", map[string]any{"email": "session@test.local", "password": "password-123"})
	if again.Status != 200 || e2.call("GET", "/api/auth/me", again.Body["token"].(string), nil).Status != 200 {
		t.Fatalf("signing in again: %d %s", again.Status, again.Raw)
	}
}

// wsRefused reports whether the server refuses a live connection with this token.
func wsRefused(t *testing.T, e *env, token string) bool {
	t.Helper()
	c, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", nil)
	if err != nil {
		return true
	}
	defer c.Close()
	_ = c.WriteJSON(map[string]any{"t": "auth", "d": map[string]any{"token": token}})
	for i := 0; i < 5; i++ {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var f struct{ T string }
		if err := c.ReadJSON(&f); err != nil {
			return true // closed on us
		}
		if f.T == "ready" {
			return false
		}
	}
	return true
}

// Anyone in a team can report a problem at the Help Desk, the organisers see it, and their decision reaches the team.
func TestTeammatesReportProblemsAtTheHelpDesk(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	lead, _ := e.signup("Helpers")
	mate := join(e, teamCode(t, e, lead), "hana").Body["token"].(string)
	r := e.call("POST", "/api/disputes", mate, map[string]any{"category": "incorrect_transaction", "summary": "My buy of 10 shares shows 1 share."})
	if r.Status != 200 {
		t.Fatalf("a teammate reporting a problem: %d %s", r.Status, r.Raw)
	}
	id := r.Body["id"].(string)
	if r := e.call("POST", "/api/disputes", mate, map[string]any{"category": "other", "summary": "short"}); r.Status != 400 {
		t.Fatalf("a report of 5 characters: %d", r.Status)
	}
	if list := e.call("GET", "/api/admin/disputes", adm, nil).Raw; !strings.Contains(string(list), "My buy of 10 shares") {
		t.Fatalf("the organisers do not see the report: %s", list)
	}
	if r := e.call("POST", "/api/admin/disputes/"+id+"/resolve", adm, map[string]any{"reason": "Checked: the trade was 1 share. No change."}); r.Status != 200 {
		t.Fatalf("resolve: %d %s", r.Status, r.Raw)
	}
	mine := e.call("GET", "/api/disputes/mine", lead, nil).Raw
	if !strings.Contains(string(mine), "My buy of 10 shares") || strings.Contains(string(mine), `"status":"open"`) {
		t.Fatalf("the leader's view of the team's reports after the decision: %s", mine)
	}
}
