package httpapi_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"stockastic/api/internal/sim"
	"stockastic/api/internal/store"
)

func TestNoMoreThanTheLimitInOneCompany(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")
	px := e.price(tok, "ACME") // 101.50; 25% of 10,00,000 is 2,50,000, so 2,463 shares is the most

	r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 2464))
	if r.Status != 422 || r.Body["error"] != "too_concentrated" || num(r.Body["maxQty"]) != 2463 {
		t.Fatalf("one share over the limit: %d %s (limit at %v a share)", r.Status, r.Raw, px)
	}
	if !strings.Contains(r.Body["message"].(string), "25%") {
		t.Fatalf("the message does not name the limit: %v", r.Body["message"])
	}
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 2463)); r.Status != 200 {
		t.Fatalf("exactly the limit: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 422 || num(r.Body["maxQty"]) != 0 {
		t.Fatalf("a share past the limit: %d %s", r.Status, r.Raw)
	}
	// Other companies are separate, selling is always allowed, and the refused trades left nothing behind.
	if r := e.call("POST", "/api/trades", tok, trade("buy", "GLOBEX", 100)); r.Status != 200 {
		t.Fatalf("another company: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", tok, trade("sell", "ACME", 500)); r.Status != 200 {
		t.Fatalf("selling: %d %s", r.Status, r.Raw)
	}
	if mine := list(e.call("GET", "/api/trades/mine", tok, nil).Raw); len(mine) != 3 {
		t.Fatalf("trades = %d, want 3", len(mine))
	}
}

func TestOrganiserCanRemoveAndReadmitATeamAndCloseRegistration(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	e.openMarket(adm)
	tok, id := e.signup("Alice")
	e.signup("Bob")

	// Everyone connected is visible to the organiser.
	c := wsDial(t, e, tok)
	wsReady(t, c)
	var rows []map[string]any
	_ = json.Unmarshal(e.call("GET", "/api/admin/accounts", adm, nil).Raw, &rows)
	online := 0
	for _, a := range rows {
		if a["online"] == true {
			online++
			if a["id"] != id {
				t.Fatalf("the wrong team is shown online: %v", a)
			}
		}
	}
	if online != 1 {
		t.Fatalf("%d teams shown online, want 1", online)
	}

	// A private message reaches only that team's open page.
	if r := e.call("POST", "/api/admin/accounts/"+id+"/message", adm, map[string]any{"text": "Please come to the help desk"}); r.Status != 200 {
		t.Fatalf("message: %d %s", r.Status, r.Raw)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	for got := false; !got; {
		var f struct {
			T string `json:"t"`
		}
		if err := c.ReadJSON(&f); err != nil {
			t.Fatalf("the private message never arrived: %v", err)
		}
		got = f.T == "news"
	}

	// Removing a team: signed out at once, cannot come back, cannot trade.
	if r := e.call("POST", "/api/admin/accounts/"+id+"/eject", tok, map[string]any{}); r.Status != 403 {
		t.Fatalf("a team removed a team: %d", r.Status)
	}
	if r := e.call("POST", "/api/admin/accounts/"+id+"/eject", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("eject: %d %s", r.Status, r.Raw)
	}
	if code := wsClosedWith(c); code != 4401 {
		t.Fatalf("the removed team's page was closed with %d, want 4401", code)
	}
	if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 401 {
		t.Fatalf("the old token still works: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "password-123"}); r.Status == 200 {
		t.Fatal("a removed team logged in again")
	}
	// Readmitting lets them back.
	if r := e.call("POST", "/api/admin/accounts/"+id+"/readmit", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("readmit: %d %s", r.Status, r.Raw)
	}
	l := e.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "password-123"})
	if l.Status != 200 {
		t.Fatalf("login after readmit: %d %s", l.Status, l.Raw)
	}
	back := l.Body["token"].(string)
	if r := e.call("POST", "/api/trades", back, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("trading after readmit: %d %s", r.Status, r.Raw)
	}

	// Registration: closed by the organiser, refused, and still closed after a restart.
	if r := e.call("POST", "/api/admin/settings/signup", adm, map[string]any{"open": false}); r.Status != 200 {
		t.Fatalf("close registration: %d", r.Status)
	}
	signup := map[string]any{"displayName": "Carol", "email": "carol@test.local", "password": "password-123"}
	if r := e.call("POST", "/api/auth/signup", "", signup); r.Status != 403 {
		t.Fatalf("signup while closed: %d", r.Status)
	}
	if cfg := e.call("GET", "/api/config", back, nil); cfg.Body["signupOpen"] != false {
		t.Fatalf("config says signup is %v", cfg.Body["signupOpen"])
	}
	e2 := e.restart(r)
	if r := e2.call("POST", "/api/auth/signup", "", signup); r.Status != 403 {
		t.Fatalf("signup after a restart: %d, registration should still be closed", r.Status)
	}
	if r := e2.call("POST", "/api/admin/settings/signup", e2.admin(), map[string]any{"open": true}); r.Status != 200 {
		t.Fatal("reopen failed")
	}
	if r := e2.call("POST", "/api/auth/signup", "", signup); r.Status != 200 {
		t.Fatalf("signup after reopening: %d", r.Status)
	}
}

func TestOrganiserSeesEveryTradeAndCanExportThem(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	a, aid := e.signup("Alice")
	b, _ := e.signup("Bob")
	e.call("POST", "/api/trades", a, trade("buy", "ACME", 3))
	e.call("POST", "/api/trades", b, trade("buy", "GLOBEX", 2))
	e.call("POST", "/api/trades", a, trade("buy", "GLOBEX", 1))

	if r := e.call("GET", "/api/admin/trades", a, nil); r.Status != 403 {
		t.Fatalf("a team read the trade log: %d", r.Status)
	}
	all := list(e.call("GET", "/api/admin/trades", adm, nil).Raw)
	if len(all) != 3 || all[0].(map[string]any)["team"] != "Alice" {
		t.Fatalf("all trades = %v", all)
	}
	if mine := list(e.call("GET", "/api/admin/trades?account="+aid, adm, nil).Raw); len(mine) != 2 {
		t.Fatalf("Alice's trades = %d, want 2", len(mine))
	}
	if g := list(e.call("GET", "/api/admin/trades?symbol=GLOBEX", adm, nil).Raw); len(g) != 2 {
		t.Fatalf("GLOBEX trades = %d, want 2", len(g))
	}
	csvTrades := string(e.call("GET", "/api/admin/export/trades.csv", adm, nil).Raw)
	if !strings.HasPrefix(csvTrades, "time,team,") || strings.Count(csvTrades, "\n") != 4 {
		t.Fatalf("trades csv:\n%s", csvTrades)
	}
	csvAcc := string(e.call("GET", "/api/admin/export/accounts.csv", adm, nil).Raw)
	if !strings.Contains(csvAcc, "Alice") || !strings.Contains(csvAcc, "Bob") {
		t.Fatalf("accounts csv:\n%s", csvAcc)
	}
	if r := e.call("GET", "/api/admin/export/trades.csv", a, nil); r.Status != http.StatusForbidden {
		t.Fatalf("a team exported the trades: %d", r.Status)
	}
}

func TestOrganiserControlsTheAutomaticNews(t *testing.T) {
	sc := sim.Scenario{Seed: 1, TickSeconds: 1, NewsClock: "market", Events: []sim.Event{
		{ID: "n1", AtMinute: 100, Headline: "Original words", Type: "REAL", Category: "TEST"},
		{ID: "n2", AtMinute: 101, Headline: "Second item", Type: "FAKE", Category: "RUMOUR"},
	}}
	e := newEnvWith(t, store.NewMem(), rb(t, 100), sc)
	adm := e.admin()
	team, _ := e.signup("Mallory")
	items := func(e *env, adm string) map[string]map[string]any {
		var st struct {
			NewsManual bool             `json:"newsManual"`
			Items      []map[string]any `json:"items"`
		}
		_ = json.Unmarshal(e.call("GET", "/api/admin/sim", adm, nil).Raw, &st)
		out := map[string]map[string]any{"_": {"manual": st.NewsManual}}
		for _, it := range st.Items {
			out[it["id"].(string)] = it
		}
		return out
	}
	if r := e.call("POST", "/api/admin/sim/n1/skip", adm, map[string]any{"skip": true}); r.Status != 200 {
		t.Fatalf("hold: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/sim/n2/edit", adm, map[string]any{"headline": "New words", "atMinute": 50}); r.Status != 200 {
		t.Fatalf("edit: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/admin/sim/nope/skip", adm, map[string]any{"skip": true}); r.Status != 400 {
		t.Fatalf("hold an item that does not exist: %d", r.Status)
	}
	if r := e.call("POST", "/api/admin/sim/news-mode", adm, map[string]any{"manual": true}); r.Status != 200 {
		t.Fatalf("manual mode: %d", r.Status)
	}
	it := items(e, adm)
	if it["n1"]["skipped"] != true || it["n2"]["headline"] != "New words" || it["n2"]["edited"] != true || it["_"]["manual"] != true {
		t.Fatalf("status = %v", it)
	}
	for _, p := range []string{"/api/admin/sim/n1/skip", "/api/admin/sim/news-mode"} {
		if r := e.call("POST", p, team, map[string]any{"skip": true}); r.Status != 403 {
			t.Fatalf("a team used %s: %d", p, r.Status)
		}
	}
	// The choices survive a restart.
	e2 := e.restart(rb(t, 100))
	it = items(e2, e2.admin())
	if it["n1"]["skipped"] != true || it["n2"]["headline"] != "New words" || it["_"]["manual"] != true {
		t.Fatalf("status after a restart = %v", it)
	}
}
