package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"stockastic/api/internal/store"
)

func wsDial(t *testing.T, e *env, token string) *websocket.Conn {
	t.Helper()
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.WriteJSON(map[string]any{"t": "auth", "d": map[string]any{"token": token}})
	return c
}

// wsReady reads frames until the socket says it is ready; it fails if the server closes it first.
func wsReady(t *testing.T, c *websocket.Conn) {
	t.Helper()
	for i := 0; i < 5; i++ {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var f struct{ T string }
		if err := c.ReadJSON(&f); err != nil {
			t.Fatalf("waiting for ready: %v", err)
		}
		if f.T == "ready" {
			return
		}
	}
	t.Fatal("never became ready")
}

// wsClosedWith reads until the server closes the socket and returns the close code (0 if it never closes).
func wsClosedWith(c *websocket.Conn) int {
	for i := 0; i < 20; i++ {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		if _, _, err := c.ReadMessage(); err != nil {
			if ce, ok := err.(*websocket.CloseError); ok {
				return ce.Code
			}
			return -1
		}
	}
	return 0
}

func row(e *env, adm, id string) map[string]any {
	e.t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal(e.call("GET", "/api/admin/accounts", adm, nil).Raw, &rows); err != nil {
		e.t.Fatal(err)
	}
	for _, r := range rows {
		if r["id"] == id {
			return r
		}
	}
	e.t.Fatalf("account %s not in the list", id)
	return nil
}

func eventually(t *testing.T, what string, f func() bool) {
	t.Helper()
	for i := 0; i < 60; i++ {
		if f() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestReasonsAreOptionalButKeptWhenGiven(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	_, id := e.signup("Alice")
	if r := e.call("POST", "/api/admin/accounts/"+id+"/warn", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("warn with no body reason: %d %s", r.Status, r.Raw)
	}
	e.call("POST", "/api/admin/announce", adm, map[string]any{"text": "Hello everyone", "reason": "kept if given"})
	log := string(e.call("GET", "/api/admin/audit", adm, nil).Raw)
	for _, want := range []string{"Formal warning", "Announced to everyone", "kept if given", "Organiser"} {
		if !strings.Contains(log, want) {
			t.Errorf("audit log is missing %q: %s", want, log)
		}
	}
}

func TestOnlineAndOfflineTeamsAreTracked(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	tok, id := e.signup("Alice")

	if r := row(e, adm, id); r["online"] != false || num(r["lastSeen"]) != 0 || num(r["sockets"]) != 0 {
		t.Fatalf("before connecting: %v", r)
	}
	c1 := wsDial(t, e, tok)
	wsReady(t, c1)
	if r := row(e, adm, id); r["online"] != true || num(r["sockets"]) != 1 {
		t.Fatalf("with one browser open: %v", r)
	}
	c2 := wsDial(t, e, tok) // a second browser tab
	wsReady(t, c2)
	if r := row(e, adm, id); num(r["sockets"]) != 2 {
		t.Fatalf("with two browsers open: %v", r)
	}
	_ = c1.Close()
	eventually(t, "one browser to close", func() bool { return num(row(e, adm, id)["sockets"]) == 1 })
	if r := row(e, adm, id); r["online"] != true {
		t.Fatal("a team with a browser still open was shown as offline")
	}
	_ = c2.Close()
	eventually(t, "the team to show offline", func() bool { return row(e, adm, id)["online"] == false })
	if r := row(e, adm, id); num(r["lastSeen"]) == 0 {
		t.Fatalf("no last-seen time after disconnecting: %v", r)
	}
}

func TestSigningATeamOutSendsItToSignInAndCancelsItsOldTokens(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	tok, id := e.signup("Alice")
	other, _ := e.signup("Bob")

	c := wsDial(t, e, tok)
	wsReady(t, c)
	if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 200 {
		t.Fatalf("setup: %d", r.Status)
	}

	if r := e.call("POST", "/api/admin/accounts/"+id+"/sign-out", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("sign out: %d %s", r.Status, r.Raw)
	}
	if code := wsClosedWith(c); code != 4401 {
		t.Fatalf("the open page was closed with code %d, want 4401 (sign in again)", code)
	}
	if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 401 {
		t.Fatalf("the old token still works: %d", r.Status)
	}
	if r := e.call("GET", "/api/auth/me", other, nil); r.Status != 200 {
		t.Fatalf("another team was signed out too: %d", r.Status)
	}
	// Signing in again works at once, even within the same second.
	fresh := e.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "password-123"})
	if fresh.Status != 200 {
		t.Fatalf("signing back in: %d %s", fresh.Status, fresh.Raw)
	}
	if r := e.call("GET", "/api/auth/me", fresh.Body["token"].(string), nil); r.Status != 200 {
		t.Fatalf("the new token was refused: %d", r.Status)
	}

	// The sign-out survives a restart: the old token stays dead.
	e.srv.Close()
	_ = e.a.Close(context.Background())
	e2 := newEnv(t, wal, r)
	if r := e2.call("GET", "/api/auth/me", tok, nil); r.Status != 401 {
		t.Fatalf("after a restart the old token works again: %d", r.Status)
	}
	if r := e2.call("GET", "/api/auth/me", fresh.Body["token"].(string), nil); r.Status != 200 {
		t.Fatalf("after a restart the new token was refused: %d", r.Status)
	}
}

func TestSignOutEveryoneKeepsTheOrganiserIn(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	a, _ := e.signup("Alice")
	b, _ := e.signup("Bob")
	r := e.call("POST", "/api/admin/sign-out-all", adm, map[string]any{})
	if r.Status != 200 || num(r.Body["teams"]) != 2 {
		t.Fatalf("sign out all: %d %s", r.Status, r.Raw)
	}
	for name, tok := range map[string]string{"alice": a, "bob": b} {
		if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 401 {
			t.Errorf("%s is still signed in: %d", name, r.Status)
		}
	}
	if r := e.call("GET", "/api/admin/overview", adm, nil); r.Status != 200 {
		t.Fatalf("the organiser was signed out: %d", r.Status)
	}
}

func TestLockingAndUnlockingAnAccount(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	tok, id := e.signup("Alice")
	adminID := e.call("POST", "/api/auth/login", "", map[string]any{"email": adminEmail, "password": adminPass}).Body["account"].(map[string]any)["id"].(string)

	c := wsDial(t, e, tok)
	wsReady(t, c)
	if r := e.call("POST", "/api/admin/accounts/"+id+"/lock", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("lock: %d %s", r.Status, r.Raw)
	}
	if code := wsClosedWith(c); code != 4401 {
		t.Fatalf("a locked team's open page closed with %d, want 4401", code)
	}
	if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 401 {
		t.Fatalf("a locked team's token works: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "password-123"}); r.Status != 403 || r.Body["error"] != "account_locked" {
		t.Fatalf("a locked team logging in: %d %s", r.Status, r.Raw)
	}
	if got := row(e, adm, id); got["locked"] != true {
		t.Fatalf("the list does not show it as locked: %v", got)
	}
	if r := e.call("POST", "/api/admin/accounts/"+id+"/unlock", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("unlock: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "password-123"}); r.Status != 200 {
		t.Fatalf("logging in after unlock: %d %s", r.Status, r.Raw)
	}
	// Organisers cannot be locked out of their own console.
	if r := e.call("POST", "/api/admin/accounts/"+adminID+"/lock", adm, map[string]any{}); r.Status != 404 {
		t.Fatalf("an organiser account was lockable: %d", r.Status)
	}
}

func TestSettingCashAndHoldingsToExactAmounts(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	e.openMarket(adm)
	tok, id := e.signup("Alice")
	cash := func(e *env, adm string) float64 { return num(e.team(adm, id)["wallet"].(map[string]any)["cash"]) }
	shares := func(e *env, adm, sym string) float64 {
		for _, h := range e.team(adm, id)["holdings"].([]any) {
			if h.(map[string]any)["symbol"] == sym {
				return num(h.(map[string]any)["qty"])
			}
		}
		return 0
	}

	// Cash to an exact figure, up and down.
	for _, target := range []float64{250000.75, 2000000, 0} {
		if r := e.call("POST", "/api/admin/accounts/"+id+"/cash", adm, map[string]any{"setTo": target}); r.Status != 200 {
			t.Fatalf("set cash to %v: %d %s", target, r.Status, r.Raw)
		}
		if got := cash(e, adm); got != target {
			t.Fatalf("cash = %v, want exactly %v", got, target)
		}
	}
	if r := e.call("POST", "/api/admin/accounts/"+id+"/cash", adm, map[string]any{"setTo": -5}); r.Status != 400 {
		t.Fatalf("a negative balance was accepted: %d", r.Status)
	}
	// Cash cannot be set below what working orders hold back.
	e.call("POST", "/api/admin/accounts/"+id+"/cash", adm, map[string]any{"setTo": 100000})
	e.call("POST", "/api/orders", tok, order("buy", "ACME", 100, 500)) // holds back 50,000
	if r := e.call("POST", "/api/admin/accounts/"+id+"/cash", adm, map[string]any{"setTo": 10000}); r.Status != 422 {
		t.Fatalf("cash set below the held-back amount: %d %s", r.Status, r.Raw)
	}
	e.call("POST", "/api/admin/accounts/"+id+"/cash", adm, map[string]any{"setTo": 60000})

	// Holdings to an exact number, up, down and to nothing.
	for _, target := range []int{40, 100, 25, 0} {
		if r := e.call("POST", "/api/admin/accounts/"+id+"/shares", adm, map[string]any{"direction": "set", "symbol": "GLOBEX", "qty": target}); r.Status != 200 {
			t.Fatalf("set holding to %d: %d %s", target, r.Status, r.Raw)
		}
		if got := shares(e, adm, "GLOBEX"); got != float64(target) {
			t.Fatalf("holding = %v, want exactly %d", got, target)
		}
	}
	e.call("POST", "/api/admin/accounts/"+id+"/shares", adm, map[string]any{"direction": "set", "symbol": "GLOBEX", "qty": 70})

	before := cash(e, adm)
	beforeShares := shares(e, adm, "GLOBEX")
	e.srv.Close()
	_ = e.a.Close(context.Background())
	e2 := newEnv(t, wal, r)
	adm2 := e2.admin()
	if cash(e2, adm2) != before || shares(e2, adm2, "GLOBEX") != beforeShares {
		t.Fatalf("after a restart: cash %v shares %v, want %v and %v", cash(e2, adm2), shares(e2, adm2, "GLOBEX"), before, beforeShares)
	}
}

func TestTheEventCanBeMadeLongerShorterOrEndedEarly(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	tok, _ := e.signup("Alice")

	overview := func(e *env, adm string) (total float64, dur float64) {
		ov := e.call("GET", "/api/admin/overview", adm, nil).Body
		total = num(ov["clock"].(map[string]any)["totalMs"]) / 60000
		for _, b := range ov["timeline"].([]any) {
			if b.(map[string]any)["id"] == "p1_trading" {
				dur = num(b.(map[string]any)["durationMin"])
			}
		}
		return
	}
	if total, dur := overview(e, adm); total != 300 || dur != 40 {
		t.Fatalf("planned schedule: %v min total, trading %v min", total, dur)
	}

	// Longer: trading 40 -> 90 minutes.
	if r := e.call("POST", "/api/admin/clock/block-duration", adm, map[string]any{"blockId": "p1_trading", "minutes": 90}); r.Status != 200 {
		t.Fatalf("lengthen: %d %s", r.Status, r.Raw)
	}
	if total, dur := overview(e, adm); total != 350 || dur != 90 {
		t.Fatalf("after lengthening: %v min total, trading %v min", total, dur)
	}
	// Participants see the same lengths.
	cfg := e.call("GET", "/api/config", tok, nil).Body["event"].(map[string]any)
	if num(cfg["totalMinutes"]) != 350 {
		t.Fatalf("the public schedule says %v minutes", cfg["totalMinutes"])
	}
	// Shorter than planned works too, and bad values are refused.
	if r := e.call("POST", "/api/admin/clock/block-duration", adm, map[string]any{"blockId": "p1_freeze", "minutes": 3}); r.Status != 200 {
		t.Fatalf("shorten: %d %s", r.Status, r.Raw)
	}
	for name, body := range map[string]map[string]any{
		"zero minutes": {"blockId": "p1_trading", "minutes": 0},
		"a huge block": {"blockId": "p1_trading", "minutes": 99999},
		"unknown":      {"blockId": "nope", "minutes": 10},
	} {
		if r := e.call("POST", "/api/admin/clock/block-duration", adm, body); r.Status != 400 {
			t.Errorf("%s: %d %s", name, r.Status, r.Raw)
		}
	}

	// Run the event, then a finished block cannot be changed.
	e.openMarket(adm)
	if r := e.call("POST", "/api/admin/clock/block-duration", adm, map[string]any{"blockId": "briefing", "minutes": 60}); r.Status != 409 {
		t.Fatalf("changing a block that already finished: %d %s", r.Status, r.Raw)
	}
	// The changed lengths survive a restart.
	e.srv.Close()
	_ = e.a.Close(context.Background())
	e2 := newEnv(t, wal, r)
	adm2 := e2.admin()
	if total, dur := overview(e2, adm2); total != 343 || dur != 90 {
		t.Fatalf("after a restart: %v min total, trading %v min, want 343 and 90", total, dur)
	}

	// End the event now: the market closes and the status says so.
	tok2 := e2.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "password-123"}).Body["token"].(string)
	if r := e2.call("POST", "/api/orders", tok2, order("buy", "ACME", 90, 1)); r.Status != 200 {
		t.Fatalf("setup: the market should be open: %d %s", r.Status, r.Raw)
	}
	if r := e2.call("POST", "/api/admin/clock/end", adm2, map[string]any{}); r.Status != 200 {
		t.Fatalf("end: %d %s", r.Status, r.Raw)
	}
	if st := e2.call("GET", "/api/admin/overview", adm2, nil).Body["clock"].(map[string]any)["status"]; st != "ended" {
		t.Fatalf("status after ending = %v", st)
	}
	if r := e2.call("POST", "/api/orders", tok2, order("buy", "ACME", 90, 1)); r.Status != 403 || r.Body["error"] != "market_closed" {
		t.Fatalf("an order after the event ended: %d %s", r.Status, r.Raw)
	}
}
