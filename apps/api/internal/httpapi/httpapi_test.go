package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"stockastic/api/internal/app"
	"stockastic/api/internal/auth"
	"stockastic/api/internal/httpapi"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/store"
	"stockastic/api/internal/universe"
)

const adminEmail, adminPass = "admin@test.local", "correct-horse-battery"

type env struct {
	t   *testing.T
	a   *app.App
	srv *httptest.Server
	log store.Log
}

func rb(t *testing.T, tradesPerMinute int) *rulebook.Rulebook {
	r, err := rulebook.LoadOrDefault("")
	if err != nil {
		t.Fatal(err)
	}
	if tradesPerMinute > 0 {
		r.RateLimits.TradesPerWindow = tradesPerMinute
	}
	return r
}

func newEnv(t *testing.T, wal store.Log, r *rulebook.Rulebook) *env {
	t.Helper()
	signer, err := auth.NewSigner([]byte(strings.Repeat("k", 40)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{
		Rulebook: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), WAL: wal, Universe: universe.Default(),
		Signer: signer, AllowSignup: true, AllowedOrigins: []string{"http://localhost:3000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SeedAdmin(adminEmail, adminPass, "Organiser"); err != nil {
		t.Fatal(err)
	}
	if err := a.Start(); err != nil {
		t.Fatal(err)
	}
	h, err := httpapi.New(httpapi.Options{App: a, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), RulebookSource: "test"})
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, a: a, srv: httptest.NewServer(h), log: wal}
	t.Cleanup(func() { e.srv.Close(); _ = a.Close(context.Background()) })
	return e
}

type resp struct {
	Status int
	Body   map[string]any
	Raw    []byte
}

func (e *env) call(method, path, token string, body any) resp {
	e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	out := resp{Status: res.StatusCode, Raw: raw}
	_ = json.Unmarshal(raw, &out.Body)
	return out
}

func (e *env) signup(name string) (token, id string) {
	e.t.Helper()
	r := e.call("POST", "/api/auth/signup", "", map[string]any{"displayName": name, "email": strings.ToLower(name) + "@test.local", "password": "password-123"})
	if r.Status != 200 {
		e.t.Fatalf("signup %s: %d %s", name, r.Status, r.Raw)
	}
	acc := r.Body["account"].(map[string]any)
	return r.Body["token"].(string), acc["id"].(string)
}

func (e *env) admin() string {
	e.t.Helper()
	r := e.call("POST", "/api/auth/login", "", map[string]any{"email": adminEmail, "password": adminPass})
	if r.Status != 200 {
		e.t.Fatalf("admin login: %d %s", r.Status, r.Raw)
	}
	return r.Body["token"].(string)
}

func (e *env) openMarket(adminTok string) {
	e.t.Helper()
	for _, step := range []struct {
		path string
		body map[string]any
	}{
		{"/api/admin/clock/start", map[string]any{"reason": "test start"}},
		{"/api/admin/clock/jump", map[string]any{"reason": "test jump", "blockId": "p1_trading"}},
	} {
		if r := e.call("POST", step.path, adminTok, step.body); r.Status != 200 {
			e.t.Fatalf("%s: %d %s", step.path, r.Status, r.Raw)
		}
	}
}

func (e *env) grant(adminTok, account, symbol string, qty int) {
	e.t.Helper()
	r := e.call("POST", "/api/admin/grants", adminTok, map[string]any{"reason": "test inventory", "accountId": account, "symbol": symbol, "qty": qty, "price": 100})
	if r.Status != 200 {
		e.t.Fatalf("grant: %d %s", r.Status, r.Raw)
	}
}

var seq int
var seqMu sync.Mutex

func order(side, sym string, price float64, qty int) map[string]any {
	seqMu.Lock()
	seq++
	n := seq
	seqMu.Unlock()
	return map[string]any{"clientOrderId": fmt.Sprintf("c-%d", n), "symbol": sym, "side": side, "price": price, "qty": qty}
}

func num(v any) float64 { f, _ := v.(float64); return f }

func TestAuthentication(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	tok, _ := e.signup("Alice")

	if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 200 || r.Body["displayName"] != "Alice" {
		t.Fatalf("me: %d %s", r.Status, r.Raw)
	}
	if r := e.call("GET", "/api/auth/me", "", nil); r.Status != 401 {
		t.Fatalf("no token: %d", r.Status)
	}
	if r := e.call("GET", "/api/auth/me", "garbage.token.value", nil); r.Status != 401 {
		t.Fatalf("bad token: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "alice@test.local", "password": "wrong-password"}); r.Status != 401 {
		t.Fatalf("wrong password: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/login", "", map[string]any{"email": "ALICE@test.local", "password": "password-123"}); r.Status != 200 {
		t.Fatalf("login is not case-insensitive on email: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/signup", "", map[string]any{"displayName": "Alice2", "email": "alice@test.local", "password": "password-123"}); r.Status != 409 {
		t.Fatalf("duplicate email: %d", r.Status)
	}
	for name, body := range map[string]map[string]any{
		"short password": {"displayName": "Bob", "email": "b@test.local", "password": "short"},
		"bad email":      {"displayName": "Bob", "email": "nope", "password": "password-123"},
		"no name":        {"displayName": "", "email": "b@test.local", "password": "password-123"},
	} {
		if r := e.call("POST", "/api/auth/signup", "", body); r.Status != 400 {
			t.Fatalf("%s: %d", name, r.Status)
		}
	}
	// A starting balance from the rulebook, in rupees.
	if r := e.call("GET", "/api/auth/me", tok, nil); num(r.Body["cashBalance"]) != 1_000_000 {
		t.Fatalf("starting cash = %v", r.Body["cashBalance"])
	}
}

func TestAdminRoutesAreForbiddenToTeams(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	tok, _ := e.signup("Alice")
	for _, p := range []string{"/api/admin/overview", "/api/admin/systems", "/api/admin/accounts", "/api/admin/audit"} {
		if r := e.call("GET", p, tok, nil); r.Status != 403 {
			t.Fatalf("%s as a team: %d, want 403", p, r.Status)
		}
		if r := e.call("GET", p, "", nil); r.Status != 401 {
			t.Fatalf("%s with no token: %d, want 401", p, r.Status)
		}
	}
	if r := e.call("POST", "/api/admin/control/freeze", tok, map[string]any{"reason": "sneaky", "frozen": true}); r.Status != 403 {
		t.Fatalf("a team froze trading: %d", r.Status)
	}
}

func TestStandingsAreHiddenFromTeams(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	tok, _ := e.signup("Alice")
	if r := e.call("GET", "/api/leaderboard", tok, nil); r.Status != 403 {
		t.Fatalf("standings visible to a team: %d", r.Status)
	}
	if r := e.call("GET", "/api/leaderboard", e.admin(), nil); r.Status != 200 {
		t.Fatalf("standings for the organiser: %d", r.Status)
	}
	if r := e.call("GET", "/api/config", tok, nil); r.Body["leaderboard"].(map[string]any)["visibleToParticipants"] != false {
		t.Fatalf("config = %s", r.Raw)
	}
}

func TestMarketClosedUntilTheEventRuns(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	alice, aid := e.signup("Alice")
	e.grant(adm, aid, "ACME", 100)
	if r := e.call("POST", "/api/orders", alice, order("sell", "ACME", 100, 1)); r.Status != 403 || r.Body["error"] != "market_closed" {
		t.Fatalf("before the event: %d %s", r.Status, r.Raw)
	}
	e.openMarket(adm)
	if r := e.call("POST", "/api/orders", alice, order("sell", "ACME", 100, 1)); r.Status != 200 {
		t.Fatalf("during trading: %d %s", r.Status, r.Raw)
	}
}

func TestTradeBetweenTwoTeams(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	seller, sid := e.signup("Seller")
	buyer, bid := e.signup("Buyer")
	e.grant(adm, sid, "ACME", 50)

	if r := e.call("POST", "/api/orders", seller, order("sell", "ACME", 100.50, 20)); r.Status != 200 {
		t.Fatalf("sell: %d %s", r.Status, r.Raw)
	}
	r := e.call("POST", "/api/orders", buyer, order("buy", "ACME", 101, 12))
	if r.Status != 200 {
		t.Fatalf("buy: %d %s", r.Status, r.Raw)
	}
	fills := r.Body["fills"].([]any)
	if len(fills) != 1 || num(fills[0].(map[string]any)["price"]) != 100.50 || num(fills[0].(map[string]any)["qty"]) != 12 {
		t.Fatalf("fills = %v (a trade happens at the resting order's price)", fills)
	}

	// Money is exact: 12 x 100.50 = 1206.00.
	bp := e.call("GET", "/api/portfolio/me", buyer, nil)
	if num(bp.Body["cashBalance"]) != 1_000_000-1206 {
		t.Fatalf("buyer cash = %v", bp.Body["cashBalance"])
	}
	h := bp.Body["holdings"].([]any)
	if len(h) != 1 || num(h[0].(map[string]any)["qty"]) != 12 || num(h[0].(map[string]any)["avgPrice"]) != 100.50 {
		t.Fatalf("buyer holdings = %v", h)
	}
	sp := e.call("GET", "/api/portfolio/me", seller, nil)
	if num(sp.Body["cashBalance"]) != 1_000_000+1206 {
		t.Fatalf("seller cash = %v", sp.Body["cashBalance"])
	}

	// Depth shows what is left, without saying whose it is.
	d := e.call("GET", "/api/symbols/ACME/depth", buyer, nil)
	asks := d.Body["asks"].([]any)
	if len(asks) != 1 || num(asks[0].(map[string]any)["qty"]) != 8 {
		t.Fatalf("depth = %s", d.Raw)
	}
	if strings.Contains(string(d.Raw), sid) || strings.Contains(string(d.Raw), bid) {
		t.Fatal("the order book leaked an account id")
	}
	// The last price and the day's open are reported per company.
	for _, c := range e.call("GET", "/api/symbols", buyer, nil).Raw[:0] {
		_ = c
	}
	var syms []map[string]any
	_ = json.Unmarshal(e.call("GET", "/api/symbols", buyer, nil).Raw, &syms)
	for _, s := range syms {
		if s["symbol"] == "ACME" && (num(s["lastPrice"]) != 100.50 || num(s["openPrice"]) != 101.50) {
			t.Fatalf("ACME = %v", s)
		}
	}
	// The seller's remaining order is listed as pending for the seller only.
	if p := e.call("GET", "/api/orders/pending", seller, nil); len(bytesToList(p.Raw)) != 1 {
		t.Fatalf("seller pending = %s", p.Raw)
	}
	if p := e.call("GET", "/api/orders/pending", buyer, nil); len(bytesToList(p.Raw)) != 0 {
		t.Fatalf("buyer pending = %s", p.Raw)
	}
}

func bytesToList(b []byte) []any { var l []any; _ = json.Unmarshal(b, &l); return l }

func TestMarketOrderAndImmediateOrCancel(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	seller, sid := e.signup("Seller")
	buyer, _ := e.signup("Buyer")
	e.grant(adm, sid, "ACME", 50)
	e.call("POST", "/api/orders", seller, order("sell", "ACME", 100, 5))

	m := order("buy", "ACME", 200, 8)
	m["type"] = "market"
	r := e.call("POST", "/api/orders", buyer, m)
	if r.Status != 200 {
		t.Fatalf("market: %d %s", r.Status, r.Raw)
	}
	o := r.Body["order"].(map[string]any)
	if num(o["remainingQty"]) != 3 || o["status"] != "cancelled" {
		t.Fatalf("market order = %v (unfilled part is cancelled, never rested)", o)
	}
	if d := e.call("GET", "/api/symbols/ACME/depth", buyer, nil); len(d.Body["bids"].([]any)) != 0 {
		t.Fatal("a market order rested in the book")
	}
	bad := order("buy", "ACME", 100, 1)
	bad["type"] = "market"
	bad["timeInForce"] = "gtc"
	if r := e.call("POST", "/api/orders", buyer, bad); r.Status != 400 {
		t.Fatalf("a resting market order was accepted: %d", r.Status)
	}
}

func TestOrderValidationAndCover(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")

	cases := map[string]struct {
		body   map[string]any
		status int
	}{
		"no shares to sell":  {order("sell", "ACME", 100, 1), 422},
		"not enough cash":    {order("buy", "ACME", 1_000_000, 10), 422},
		"unknown symbol":     {order("buy", "NOPE", 100, 1), 404},
		"zero quantity":      {order("buy", "ACME", 100, 0), 400},
		"negative price":     {order("buy", "ACME", -5, 1), 400},
		"fractional paisa 0": {order("buy", "ACME", 0, 1), 400},
		"bad side":           {order("hold", "ACME", 100, 1), 400},
	}
	for name, c := range cases {
		if r := e.call("POST", "/api/orders", tok, c.body); r.Status != c.status {
			t.Errorf("%s: %d %s, want %d", name, r.Status, r.Raw, c.status)
		}
	}
	// A rejected order must not have used up the account's trades (the limit here is generous, so check the ledger).
	if r := e.call("GET", "/api/orders/pending", tok, nil); len(bytesToList(r.Raw)) != 0 {
		t.Fatal("a rejected order is listed as working")
	}
}

func TestRetryReturnsTheOriginalOrderAndDoesNotUseATrade(t *testing.T) {
	// The rulebook allows 2 trades per minute per account. A retry of the same order is not a new trade.
	e := newEnv(t, store.NewMem(), rb(t, 0))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")

	o := order("buy", "ACME", 100, 1)
	first := e.call("POST", "/api/orders", tok, o)
	if first.Status != 200 {
		t.Fatalf("first: %d %s", first.Status, first.Raw)
	}
	for i := 0; i < 5; i++ {
		again := e.call("POST", "/api/orders", tok, o)
		if again.Status != 200 || again.Body["deduped"] != true {
			t.Fatalf("retry %d: %d %s", i, again.Status, again.Raw)
		}
		if again.Body["order"].(map[string]any)["id"] != first.Body["order"].(map[string]any)["id"] {
			t.Fatal("a retry produced a different order")
		}
	}
	if p := e.call("GET", "/api/orders/pending", tok, nil); len(bytesToList(p.Raw)) != 1 {
		t.Fatalf("pending = %s", p.Raw)
	}
	// The same id for a different order is refused, not silently merged.
	other := map[string]any{"clientOrderId": o["clientOrderId"], "symbol": "ACME", "side": "buy", "price": 101.0, "qty": 1}
	if r := e.call("POST", "/api/orders", tok, other); r.Status != 409 {
		t.Fatalf("reused id: %d %s", r.Status, r.Raw)
	}
	// The second genuine trade fits, the third does not.
	if r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 99, 1)); r.Status != 200 {
		t.Fatalf("second trade: %d %s", r.Status, r.Raw)
	}
	r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 98, 1))
	if r.Status != 429 || r.Body["error"] != "rate_limited" {
		t.Fatalf("third trade: %d %s", r.Status, r.Raw)
	}
}

func TestCancelOnlyYourOwnOrder(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	alice, _ := e.signup("Alice")
	bob, _ := e.signup("Bob")
	r := e.call("POST", "/api/orders", alice, order("buy", "ACME", 90, 5))
	id := r.Body["order"].(map[string]any)["id"].(string)

	if c := e.call("DELETE", "/api/orders/ACME/"+id, bob, nil); c.Status != 403 {
		t.Fatalf("bob cancelled alice's order: %d %s", c.Status, c.Raw)
	}
	if c := e.call("DELETE", "/api/orders/ACME/"+id, alice, nil); c.Status != 200 {
		t.Fatalf("alice cancel: %d %s", c.Status, c.Raw)
	}
	if c := e.call("DELETE", "/api/orders/ACME/"+id, alice, nil); c.Status != 409 {
		t.Fatalf("second cancel: %d", c.Status)
	}
	// Cash held back for the order is released.
	if a := e.call("GET", "/api/auth/me", alice, nil); num(a.Body["cashBalance"]) != 1_000_000 {
		t.Fatalf("cash after cancel = %v", a.Body["cashBalance"])
	}
}

func TestFreezeStopsOrdersAndResumeRestoresThem(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")

	// The console asks only for confirmation, so an action needs no reason.
	if r := e.call("POST", "/api/admin/control/freeze", adm, map[string]any{"frozen": true}); r.Status != 200 {
		t.Fatalf("an action without a reason: %d %s", r.Status, r.Raw)
	}
	e.call("POST", "/api/admin/control/freeze", adm, map[string]any{"reason": "fault drill", "frozen": true})
	if r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 100, 1)); r.Status != 423 {
		t.Fatalf("frozen: %d %s", r.Status, r.Raw)
	}
	if r := e.call("GET", "/api/config", tok, nil); r.Body["tradingFrozen"] != true {
		t.Fatalf("config does not say frozen: %s", r.Raw)
	}
	e.call("POST", "/api/admin/control/freeze", adm, map[string]any{"reason": "drill over", "frozen": false})
	if r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 100, 1)); r.Status != 200 {
		t.Fatalf("after resume: %d %s", r.Status, r.Raw)
	}
	// Both actions are in the audit log with their reasons and the organiser's name.
	log := string(e.call("GET", "/api/admin/audit", adm, nil).Raw)
	for _, want := range []string{"fault drill", "drill over", "Froze trading", "Organiser"} {
		if !strings.Contains(log, want) {
			t.Errorf("audit log is missing %q: %s", want, log)
		}
	}
}

func TestDisqualifiedTeamCannotTradeAndItsOrdersAreCancelled(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, id := e.signup("Cheater")
	e.call("POST", "/api/orders", tok, order("buy", "ACME", 90, 5))
	if p := e.call("GET", "/api/orders/pending", tok, nil); len(bytesToList(p.Raw)) != 1 {
		t.Fatal("setup: no working order")
	}
	if r := e.call("POST", "/api/admin/accounts/"+id+"/disqualify", adm, map[string]any{"reason": "rule 12 breach"}); r.Status != 200 {
		t.Fatalf("disqualify: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 90, 1)); r.Status != 403 {
		t.Fatalf("disqualified team traded: %d", r.Status)
	}
	if d := e.call("GET", "/api/symbols/ACME/depth", adm, nil); len(d.Body["bids"].([]any)) != 0 {
		t.Fatal("a disqualified team's order is still resting in the book")
	}
}

func TestLiveUpdatesOverWebSocket(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	seller, sid := e.signup("Seller")
	buyer, _ := e.signup("Buyer")
	e.grant(adm, sid, "ACME", 20)

	dial := func(token string) *websocket.Conn {
		url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
		c, _, err := websocket.DefaultDialer.Dial(url, nil)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.WriteJSON(map[string]any{"t": "auth", "d": map[string]any{"token": token}})
		return c
	}
	read := func(c *websocket.Conn) (string, json.RawMessage) {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var f struct {
			T string          `json:"t"`
			D json.RawMessage `json:"d"`
		}
		if err := c.ReadJSON(&f); err != nil {
			t.Fatal(err)
		}
		return f.T, f.D
	}
	waitFor := func(c *websocket.Conn, want string) json.RawMessage {
		for i := 0; i < 10; i++ {
			if typ, d := read(c); typ == want {
				return d
			}
		}
		t.Fatalf("never received %q", want)
		return nil
	}

	bc := dial(buyer)
	defer bc.Close()
	waitFor(bc, "ready")
	waitFor(bc, "controlState")
	_ = bc.WriteJSON(map[string]any{"t": "subscribe:symbol", "d": "ACME"})
	_ = bc.WriteJSON(map[string]any{"t": "ping"})
	waitFor(bc, "pong")

	e.call("POST", "/api/orders", seller, order("sell", "ACME", 100, 10))
	var depth struct {
		Asks []struct{ Qty int } `json:"asks"`
	}
	_ = json.Unmarshal(waitFor(bc, "bookUpdate"), &depth)
	if len(depth.Asks) != 1 || depth.Asks[0].Qty != 10 {
		t.Fatalf("bookUpdate = %+v", depth)
	}

	e.call("POST", "/api/orders", buyer, order("buy", "ACME", 100, 4))
	trade := string(waitFor(bc, "trade"))
	fill := string(waitFor(bc, "fill"))
	if strings.Contains(trade, "AccountId") || strings.Contains(strings.ToLower(trade), "accountid") {
		t.Fatalf("the public trade feed names accounts: %s", trade)
	}
	if !strings.Contains(fill, "takerAccountId") {
		t.Fatalf("the private fill is missing detail: %s", fill)
	}

	// A bad token is refused with the close code the web app treats as "sign in again".
	bad := dial("not-a-token")
	defer bad.Close()
	typ, _ := read(bad)
	if typ != "error" {
		t.Fatalf("first frame for a bad token = %q", typ)
	}
	_ = bad.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := bad.ReadMessage(); !websocket.IsCloseError(err, 4401) {
		t.Fatalf("close = %v, want 4401", err)
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	e.openMarket(adm)
	seller, sid := e.signup("Seller")
	buyer, _ := e.signup("Buyer")
	e.grant(adm, sid, "ACME", 50)
	e.call("POST", "/api/orders", seller, order("sell", "ACME", 100, 20))
	held := order("buy", "ACME", 100, 5)
	e.call("POST", "/api/orders", buyer, held)
	e.call("POST", "/api/orders", buyer, order("buy", "ACME", 60, 3)) // rests

	before := func(e *env) (string, string, string) {
		return string(e.call("GET", "/api/portfolio/me", buyer, nil).Raw),
			string(e.call("GET", "/api/symbols/ACME/depth", buyer, nil).Raw),
			string(e.call("GET", "/api/orders/pending", buyer, nil).Raw)
	}
	pf, dp, pd := before(e)

	// "Crash": stop everything and start a fresh process over the same durable log.
	e.srv.Close()
	_ = e.a.Close(context.Background())
	e2 := newEnv(t, wal, r)

	pf2, dp2, pd2 := before(e2)
	if pf != pf2 {
		t.Errorf("portfolio changed across the restart:\n before %s\n after  %s", pf, pf2)
	}
	if dp != dp2 {
		t.Errorf("order book changed across the restart:\n before %s\n after  %s", dp, dp2)
	}
	if pd != pd2 {
		t.Errorf("working orders changed across the restart:\n before %s\n after  %s", pd, pd2)
	}
	// The same team can still log in, and a retry of a pre-crash order dedupes instead of double-trading.
	if r := e2.call("POST", "/api/orders", buyer, held); r.Status != 200 || r.Body["deduped"] != true {
		t.Fatalf("retry after restart: %d %s", r.Status, r.Raw)
	}
	if e2.call("GET", "/api/portfolio/me", buyer, nil).Status != 200 {
		t.Fatal("the token stopped working after a restart")
	}
	// The event clock kept its place: the market is still open.
	if r := e2.call("POST", "/api/orders", buyer, order("buy", "ACME", 55, 1)); r.Status != 200 {
		t.Fatalf("market state lost across the restart: %d %s", r.Status, r.Raw)
	}
}

func TestAJournalFailureNeverLeavesAPhantomOrder(t *testing.T) {
	wal := store.NewMem()
	e := newEnv(t, wal, rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")

	wal.Fail = fmt.Errorf("disk full")
	r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 100, 5))
	wal.Fail = nil
	if r.Status != 503 {
		t.Fatalf("order during a storage failure: %d %s", r.Status, r.Raw)
	}
	if p := e.call("GET", "/api/orders/pending", tok, nil); len(bytesToList(p.Raw)) != 0 {
		t.Fatalf("an unsaved order is working: %s", p.Raw)
	}
	if d := e.call("GET", "/api/symbols/ACME/depth", tok, nil); len(d.Body["bids"].([]any)) != 0 {
		t.Fatal("an unsaved order is in the book")
	}
	// The cash reserved for it came back, so the team can still spend all of it.
	if a := e.call("GET", "/api/auth/me", tok, nil); num(a.Body["cashBalance"]) != 1_000_000 {
		t.Fatalf("cash = %v", a.Body["cashBalance"])
	}
	if r := e.call("POST", "/api/orders", tok, order("buy", "ACME", 100, 5)); r.Status != 200 {
		t.Fatalf("recovery after the storage failure: %d %s", r.Status, r.Raw)
	}
}

func TestOrganiserScreensReturnData(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.signup("Alice")
	for _, p := range []string{"overview", "systems", "accounts", "news", "disputes", "audit", "rulebook", "standings"} {
		if r := e.call("GET", "/api/admin/"+p, adm, nil); r.Status != 200 {
			t.Errorf("%s: %d %s", p, r.Status, r.Raw)
		}
	}
	ov := e.call("GET", "/api/admin/overview", adm, nil)
	if ov.Body["clock"].(map[string]any)["status"] != "not_started" || len(ov.Body["timeline"].([]any)) != 17 {
		t.Fatalf("overview = %s", ov.Raw)
	}
	if n := len(ov.Body["control"].(map[string]any)["windowsOpen"].([]any)); n != 4 {
		t.Fatalf("window switches = %d, want 4 (from the rulebook)", n)
	}
	// News: fund managers immediately, the public after the lead time.
	if r := e.call("POST", "/api/admin/news", adm, map[string]any{"reason": "opening note", "kind": "news", "headline": "Markets open"}); r.Status != 200 {
		t.Fatalf("news: %d %s", r.Status, r.Raw)
	}
	nd := e.call("GET", "/api/admin/news", adm, nil)
	if len(nd.Body["items"].([]any)) != 1 {
		t.Fatalf("news desk = %s", nd.Raw)
	}
}

func TestWebSocketRefusesForeignBrowserOrigins(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	url := "ws" + strings.TrimPrefix(e.srv.URL, "http") + "/ws"
	try := func(origin string) error {
		h := http.Header{}
		if origin != "" {
			h.Set("Origin", origin)
		}
		c, _, err := websocket.DefaultDialer.Dial(url, h)
		if err == nil {
			c.Close()
		}
		return err
	}
	if err := try("https://evil.example"); err == nil {
		t.Fatal("a page on another site could open the socket")
	}
	if err := try("http://localhost:3000"); err != nil {
		t.Fatalf("the allowed dev origin was refused: %v", err)
	}
	if err := try(""); err != nil {
		t.Fatalf("a non-browser client was refused: %v", err)
	}
}
