package httpapi_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"stockastic/api/internal/store"
)

func (e *env) act(adm, method, path string, body map[string]any) resp {
	e.t.Helper()
	if _, ok := body["reason"]; !ok {
		body["reason"] = "organiser test action"
	}
	return e.call(method, path, adm, body)
}

func (e *env) team(adm, id string) map[string]any {
	e.t.Helper()
	r := e.call("GET", "/api/admin/accounts/"+id, adm, nil)
	if r.Status != 200 {
		e.t.Fatalf("team detail: %d %s", r.Status, r.Raw)
	}
	return r.Body
}

func TestOrganiserCanCorrectATeamsWalletAndItSurvivesARestart(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	e.openMarket(adm)
	tok, id := e.signup("Alice")

	wallet := func(e *env) map[string]any { return e.team(adm, id)["wallet"].(map[string]any) }

	// Credit and debit cash.
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/cash", map[string]any{"amount": 2500.50}); r.Status != 200 {
		t.Fatalf("credit: %d %s", r.Status, r.Raw)
	}
	if got := num(wallet(e)["cash"]); got != 1_002_500.50 {
		t.Fatalf("cash after credit = %v", got)
	}
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/cash", map[string]any{"amount": -500.25}); r.Status != 200 {
		t.Fatalf("debit: %d %s", r.Status, r.Raw)
	}
	// A debit cannot reach into cash held back for a working order.
	e.call("POST", "/api/orders", tok, order("buy", "ACME", 100, 9000)) // holds back 900,000
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/cash", map[string]any{"amount": -200000}); r.Status != 422 {
		t.Fatalf("a debit into reserved cash: %d %s", r.Status, r.Raw)
	}
	// Zero, and huge amounts, are refused.
	for _, amt := range []float64{0, 1e12} {
		if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/cash", map[string]any{"amount": amt}); r.Status != 400 {
			t.Fatalf("amount %v: %d", amt, r.Status)
		}
	}
	// A correction with no reason is refused.
	if r := e.call("POST", "/api/admin/accounts/"+id+"/cash", adm, map[string]any{"amount": 1}); r.Status != 400 {
		t.Fatalf("no reason: %d", r.Status)
	}

	// Give and take back shares.
	give := map[string]any{"direction": "give", "symbol": "GLOBEX", "qty": 30, "price": 200}
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/shares", give); r.Status != 200 {
		t.Fatalf("give: %d %s", r.Status, r.Raw)
	}
	take := map[string]any{"direction": "take", "symbol": "GLOBEX", "qty": 10}
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/shares", take); r.Status != 200 {
		t.Fatalf("take: %d %s", r.Status, r.Raw)
	}
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/shares", map[string]any{"direction": "take", "symbol": "GLOBEX", "qty": 21}); r.Status != 422 {
		t.Fatalf("taking more than held: %d %s", r.Status, r.Raw)
	}
	d := e.team(adm, id)
	h := d["holdings"].([]any)
	if len(h) != 1 || num(h[0].(map[string]any)["qty"]) != 20 || num(h[0].(map[string]any)["avgPrice"]) != 200 {
		t.Fatalf("holdings = %v", h)
	}
	if len(d["orders"].([]any)) != 1 {
		t.Fatalf("the team page does not list the working order: %v", d["orders"])
	}
	hist, _ := json.Marshal(d["history"])
	for _, want := range []string{"organiser test action", "Took back 10 x GLOBEX", "Organiser"} {
		if !strings.Contains(string(hist), want) {
			t.Errorf("team history is missing %q: %s", want, hist)
		}
	}

	before, _ := json.Marshal(e.team(adm, id)["wallet"])
	beforeHold, _ := json.Marshal(e.team(adm, id)["holdings"])

	// Restart over the same log: every correction is replayed in order.
	e.srv.Close()
	_ = e.a.Close(context.Background())
	e2 := newEnv(t, wal, r)
	adm2 := e2.admin()
	after, _ := json.Marshal(e2.team(adm2, id)["wallet"])
	afterHold, _ := json.Marshal(e2.team(adm2, id)["holdings"])
	if string(before) != string(after) {
		t.Errorf("wallet changed across the restart:\n before %s\n after  %s", before, after)
	}
	if string(beforeHold) != string(afterHold) {
		t.Errorf("holdings changed across the restart:\n before %s\n after  %s", beforeHold, afterHold)
	}
}

func TestCancelOrdersReinstateResetPasswordAndRole(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, id := e.signup("Alice")
	e.call("POST", "/api/orders", tok, order("buy", "ACME", 90, 5))
	e.call("POST", "/api/orders", tok, order("buy", "GLOBEX", 90, 5))
	one := e.call("GET", "/api/orders/pending", tok, nil)
	first := bytesToList(one.Raw)[0].(map[string]any)["id"].(string)

	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/cancel-orders", map[string]any{"orderId": first}); r.Status != 200 {
		t.Fatalf("cancel one: %d %s", r.Status, r.Raw)
	}
	if n := len(bytesToList(e.call("GET", "/api/orders/pending", tok, nil).Raw)); n != 1 {
		t.Fatalf("after cancelling one, %d working", n)
	}
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/cancel-orders", map[string]any{"orderId": "no-such-order"}); r.Status != 404 {
		t.Fatalf("cancelling an unknown order: %d", r.Status)
	}
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/cancel-orders", map[string]any{}); r.Status != 200 {
		t.Fatalf("cancel all: %d %s", r.Status, r.Raw)
	}
	if n := len(bytesToList(e.call("GET", "/api/orders/pending", tok, nil).Raw)); n != 0 {
		t.Fatalf("after cancelling all, %d working", n)
	}
	if a := e.call("GET", "/api/auth/me", tok, nil); num(a.Body["cashBalance"]) != 1_000_000 {
		t.Fatalf("cash held back was not released: %v", a.Body["cashBalance"])
	}

	// Disqualify, then reinstate.
	e.act(adm, "POST", "/api/admin/accounts/"+id+"/disqualify", map[string]any{})
	if r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 90, 1)); r.Status != 403 {
		t.Fatalf("disqualified: %d", r.Status)
	}
	e.act(adm, "POST", "/api/admin/accounts/"+id+"/reinstate", map[string]any{})
	if r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 90, 1)); r.Status != 200 {
		t.Fatalf("reinstated: %d %s", r.Status, r.Raw)
	}

	// Reset the password.
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/reset-password", map[string]any{"password": "short"}); r.Status != 400 {
		t.Fatalf("a short password was accepted: %d", r.Status)
	}
	e.act(adm, "POST", "/api/admin/accounts/"+id+"/reset-password", map[string]any{"password": "brand-new-password"})
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "password-123"}); r.Status != 401 {
		t.Fatalf("the old password still works: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "brand-new-password"}); r.Status != 200 {
		t.Fatalf("the new password does not work: %d", r.Status)
	}
	if strings.Contains(string(e.call("GET", "/api/admin/audit", adm, nil).Raw), "brand-new-password") {
		t.Fatal("a password was written to the audit log")
	}

	// Role, both directions.
	e.act(adm, "POST", "/api/admin/accounts/"+id+"/role", map[string]any{"role": "fund_manager"})
	if r := e.call("GET", "/api/auth/me", tok, nil); r.Body["role"] != "fund_manager" {
		t.Fatalf("role = %v", r.Body["role"])
	}
	e.act(adm, "POST", "/api/admin/accounts/"+id+"/role", map[string]any{"role": "investor"})
	if r := e.act(adm, "POST", "/api/admin/accounts/"+id+"/role", map[string]any{"role": "emperor"}); r.Status != 400 {
		t.Fatalf("a made-up role: %d", r.Status)
	}
	if r := e.act(adm, "POST", "/api/admin/accounts/nobody/cash", map[string]any{"amount": 5}); r.Status != 404 {
		t.Fatalf("an unknown team: %d", r.Status)
	}
}

func TestPausingOneCompanyAndAnnouncing(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")
	held := e.call("POST", "/api/orders", tok, order("buy", "ACME", 90, 2))
	id := held.Body["order"].(map[string]any)["id"].(string)

	if r := e.act(adm, "POST", "/api/admin/control/symbols/ACME", map[string]any{"paused": true}); r.Status != 200 {
		t.Fatalf("pause: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 90, 1)); r.Status != 403 || r.Body["error"] != "trading_paused" {
		t.Fatalf("an order in a paused company: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/orders", tok, order("buy", "GLOBEX", 90, 1)); r.Status != 200 {
		t.Fatalf("another company should still trade: %d %s", r.Status, r.Raw)
	}
	if r := e.call("DELETE", "/api/orders/ACME/"+id, tok, nil); r.Status != 200 {
		t.Fatalf("cancelling in a paused company must still work: %d %s", r.Status, r.Raw)
	}
	if ov := e.call("GET", "/api/admin/overview", adm, nil); len(ov.Body["control"].(map[string]any)["pausedSymbols"].([]any)) != 1 {
		t.Fatalf("overview = %s", ov.Raw)
	}
	if r := e.act(adm, "POST", "/api/admin/control/symbols/NOPE", map[string]any{"paused": true}); r.Status != 404 {
		t.Fatalf("pausing an unknown company: %d", r.Status)
	}

	// An announcement reaches participants, and is kept.
	if r := e.act(adm, "POST", "/api/admin/announce", map[string]any{"text": "Window 1 opens in five minutes"}); r.Status != 200 {
		t.Fatalf("announce: %d %s", r.Status, r.Raw)
	}
	if r := e.act(adm, "POST", "/api/admin/announce", map[string]any{"text": "x"}); r.Status != 400 {
		t.Fatalf("a one-letter announcement: %d", r.Status)
	}
	feed := string(e.call("GET", "/api/news", tok, nil).Raw)
	if !strings.Contains(feed, "Window 1 opens in five minutes") || !strings.Contains(feed, `"kind":"notice"`) {
		t.Fatalf("news feed = %s", feed)
	}

	// Both survive a restart.
	e.srv.Close()
	_ = e.a.Close(context.Background())
	e2 := newEnv(t, wal, r)
	tok2 := e2.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "password-123"}).Body["token"].(string)
	if r := e2.call("POST", "/api/orders", tok2, order("buy", "ACME", 90, 1)); r.Status != 403 {
		t.Fatalf("the pause was lost across the restart: %d", r.Status)
	}
	if !strings.Contains(string(e2.call("GET", "/api/news", tok2, nil).Raw), "Window 1 opens in five minutes") {
		t.Fatal("the announcement was lost across the restart")
	}
	e2.act(e2.admin(), "POST", "/api/admin/control/symbols/ACME", map[string]any{"paused": false})
	if r := e2.call("POST", "/api/orders", tok2, order("buy", "ACME", 90, 1)); r.Status != 200 {
		t.Fatalf("after resuming the company: %d %s", r.Status, r.Raw)
	}
}

func TestAdjustmentsReplayInTheOrderTheyHappened(t *testing.T) {
	// A grant that comes after a sale changes average cost differently than one that comes before it.
	// Replaying grants first and trades second would make holdings drift after a restart.
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	e.openMarket(adm)
	seller, sid := e.signup("Seller")
	buyer, _ := e.signup("Buyer")

	e.grant(adm, sid, "ACME", 100) // 100 shares valued at 100 each
	e.call("POST", "/api/orders", seller, order("sell", "ACME", 100, 60))
	e.call("POST", "/api/orders", buyer, order("buy", "ACME", 100, 60)) // seller now holds 40
	e.act(adm, "POST", "/api/admin/grants", map[string]any{"accountId": sid, "symbol": "ACME", "qty": 40, "price": 300})

	hold := func(e *env, adm string) string {
		b, _ := json.Marshal(e.team(adm, sid)["holdings"])
		return string(b)
	}
	before := hold(e, adm)
	e.srv.Close()
	_ = e.a.Close(context.Background())
	e2 := newEnv(t, wal, r)
	if after := hold(e2, e2.admin()); after != before {
		t.Fatalf("average cost drifted across the restart:\n before %s\n after  %s", before, after)
	}
}
