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
	"stockastic/api/internal/sim"
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

// rb returns the real rulebook, optionally with a different trades-per-minute limit.
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

// testScenario changes prices every second by nothing (zero volatility), so tests see steady, exact prices
// while the price simulation still runs and broadcasts.
func testScenario() sim.Scenario {
	return sim.Scenario{Seed: 1, TickSeconds: 1, Volatility: sim.Volatility{DefaultPct: 0}}
}

func newEnv(t *testing.T, wal store.Log, r *rulebook.Rulebook) *env {
	t.Helper()
	signer, err := auth.NewSigner([]byte(strings.Repeat("k", 40)), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.New(app.Config{
		Rulebook: r, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), WAL: wal, Universe: universe.Default(),
		Scenario: testScenario(), Signer: signer, AllowSignup: true, AllowedOrigins: []string{"http://localhost:3000"},
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

// restart stops the server and starts a new one over the same log, like a crash and reboot.
func (e *env) restart(r *rulebook.Rulebook) *env {
	e.srv.Close()
	_ = e.a.Close(context.Background())
	return newEnv(e.t, e.log, r)
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
		{"/api/admin/clock/start", map[string]any{}},
		{"/api/admin/clock/jump", map[string]any{"blockId": "p1_trading"}},
	} {
		if r := e.call("POST", step.path, adminTok, step.body); r.Status != 200 {
			e.t.Fatalf("%s: %d %s", step.path, r.Status, r.Raw)
		}
	}
}

func (e *env) grant(adminTok, account, symbol string, qty int) {
	e.t.Helper()
	r := e.call("POST", "/api/admin/grants", adminTok, map[string]any{"accountId": account, "symbol": symbol, "qty": qty, "price": 100})
	if r.Status != 200 {
		e.t.Fatalf("grant: %d %s", r.Status, r.Raw)
	}
}

// price is a company's current price in rupees.
func (e *env) price(tok, sym string) float64 {
	e.t.Helper()
	var cs []map[string]any
	if err := json.Unmarshal(e.call("GET", "/api/symbols", tok, nil).Raw, &cs); err != nil {
		e.t.Fatal(err)
	}
	for _, c := range cs {
		if c["symbol"] == sym {
			return num(c["lastPrice"])
		}
	}
	e.t.Fatalf("no company %s", sym)
	return 0
}

var seq int
var seqMu sync.Mutex

// trade builds a trade request with a fresh client id.
func trade(side, sym string, qty int) map[string]any {
	seqMu.Lock()
	seq++
	n := seq
	seqMu.Unlock()
	return map[string]any{"clientTradeId": fmt.Sprintf("t-%d", n), "symbol": sym, "side": side, "qty": qty}
}

func num(v any) float64 { f, _ := v.(float64); return f }

func list(b []byte) []any { var l []any; _ = json.Unmarshal(b, &l); return l }

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
	if r := e.call("GET", "/api/auth/me", tok, nil); num(r.Body["cashBalance"]) != 1_000_000 {
		t.Fatalf("starting cash = %v", r.Body["cashBalance"])
	}
}

func TestAdminRoutesAreForbiddenToTeams(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	tok, _ := e.signup("Alice")
	for _, p := range []string{"/api/admin/overview", "/api/admin/systems", "/api/admin/accounts", "/api/admin/audit", "/api/admin/sim"} {
		if r := e.call("GET", p, tok, nil); r.Status != 403 {
			t.Fatalf("%s as a team: %d, want 403", p, r.Status)
		}
		if r := e.call("GET", p, "", nil); r.Status != 401 {
			t.Fatalf("%s with no token: %d, want 401", p, r.Status)
		}
	}
	if r := e.call("POST", "/api/admin/control/freeze", tok, map[string]any{"frozen": true}); r.Status != 403 {
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
	alice, _ := e.signup("Alice")
	if r := e.call("POST", "/api/trades", alice, trade("buy", "ACME", 1)); r.Status != 403 || r.Body["error"] != "market_closed" {
		t.Fatalf("before the event: %d %s", r.Status, r.Raw)
	}
	e.openMarket(adm)
	if r := e.call("POST", "/api/trades", alice, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("during trading: %d %s", r.Status, r.Raw)
	}
}

func TestBuyingAndSellingHappenAtTheCurrentPriceWithExactMoney(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")
	px := e.price(tok, "ACME") // 101.50 in the placeholder universe

	r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 10))
	if r.Status != 200 {
		t.Fatalf("buy: %d %s", r.Status, r.Raw)
	}
	tr := r.Body["trade"].(map[string]any)
	if num(tr["price"]) != px || num(tr["qty"]) != 10 || num(tr["value"]) != px*10 || tr["side"] != "buy" {
		t.Fatalf("trade = %v, want 10 at %v", tr, px)
	}
	pf := e.call("GET", "/api/portfolio/me", tok, nil).Body
	if num(pf["cashBalance"]) != 1_000_000-px*10 {
		t.Fatalf("cash = %v, want %v", pf["cashBalance"], 1_000_000-px*10)
	}
	h := pf["holdings"].([]any)
	if len(h) != 1 || num(h[0].(map[string]any)["qty"]) != 10 || num(h[0].(map[string]any)["avgPrice"]) != px {
		t.Fatalf("holdings = %v", h)
	}
	if r := e.call("POST", "/api/trades", tok, trade("sell", "ACME", 4)); r.Status != 200 {
		t.Fatalf("sell: %d %s", r.Status, r.Raw)
	}
	pf = e.call("GET", "/api/portfolio/me", tok, nil).Body
	if num(pf["cashBalance"]) != 1_000_000-px*6 || num(pf["totalValue"]) != 1_000_000 {
		t.Fatalf("after selling 4 at the same price: cash %v value %v", pf["cashBalance"], pf["totalValue"])
	}
	if mine := list(e.call("GET", "/api/trades/mine", tok, nil).Raw); len(mine) != 2 {
		t.Fatalf("trade history = %d entries, want 2", len(mine))
	}
}

func TestRefusedTradesLeaveNothingBehind(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")
	cases := map[string]struct {
		body   map[string]any
		status int
	}{
		"nothing to sell":   {trade("sell", "ACME", 1), 422},
		"not enough cash":   {trade("buy", "STARK", 100000), 422},
		"unknown company":   {trade("buy", "NOPE", 1), 404},
		"zero quantity":     {trade("buy", "ACME", 0), 400},
		"negative quantity": {trade("buy", "ACME", -5), 400},
		"bad side":          {trade("hold", "ACME", 1), 400},
	}
	for name, c := range cases {
		if r := e.call("POST", "/api/trades", tok, c.body); r.Status != c.status {
			t.Errorf("%s: %d %s, want %d", name, r.Status, r.Raw, c.status)
		}
	}
	if r := e.call("GET", "/api/auth/me", tok, nil); num(r.Body["cashBalance"]) != 1_000_000 {
		t.Fatalf("a refused trade changed the cash: %v", r.Body["cashBalance"])
	}
	if mine := list(e.call("GET", "/api/trades/mine", tok, nil).Raw); len(mine) != 0 {
		t.Fatalf("refused trades appear in the history: %v", mine)
	}
	// Refused trades do not use up the two-per-minute allowance (the limit here is the real one).
	e2 := newEnv(t, store.NewMem(), rb(t, 0))
	adm2 := e2.admin()
	e2.openMarket(adm2)
	tok2, _ := e2.signup("Bob")
	for i := 0; i < 5; i++ {
		e2.call("POST", "/api/trades", tok2, trade("sell", "ACME", 1)) // refused: nothing to sell
	}
	if r := e2.call("POST", "/api/trades", tok2, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("a refused trade used up the allowance: %d %s", r.Status, r.Raw)
	}
}

func TestARetryReturnsTheOriginalTradeAndDoesNotUseATrade(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 0)) // the real limit: 2 trades a minute
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")

	one := trade("buy", "ACME", 1)
	first := e.call("POST", "/api/trades", tok, one)
	if first.Status != 200 {
		t.Fatalf("first: %d %s", first.Status, first.Raw)
	}
	for i := 0; i < 5; i++ {
		again := e.call("POST", "/api/trades", tok, one)
		if again.Status != 200 || again.Body["deduped"] != true || again.Body["trade"].(map[string]any)["id"] != first.Body["trade"].(map[string]any)["id"] {
			t.Fatalf("retry %d: %d %s", i, again.Status, again.Raw)
		}
	}
	if mine := list(e.call("GET", "/api/trades/mine", tok, nil).Raw); len(mine) != 1 {
		t.Fatalf("history = %d, want 1 (retries are not new trades)", len(mine))
	}
	other := map[string]any{"clientTradeId": one["clientTradeId"], "symbol": "ACME", "side": "buy", "qty": 2}
	if r := e.call("POST", "/api/trades", tok, other); r.Status != 409 {
		t.Fatalf("reused id: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("second trade: %d %s", r.Status, r.Raw)
	}
	r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1))
	if r.Status != 429 || r.Body["error"] != "rate_limited" {
		t.Fatalf("third trade in a minute: %d %s", r.Status, r.Raw)
	}
}

func TestAPriceThePersonDidNotSeeIsRefused(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")
	px := e.price(tok, "ACME")

	wrong := trade("buy", "ACME", 1)
	wrong["expectedPrice"] = px + 5
	r := e.call("POST", "/api/trades", tok, wrong)
	if r.Status != 409 || r.Body["error"] != "price_changed" || num(r.Body["currentPrice"]) != px {
		t.Fatalf("a stale price: %d %s", r.Status, r.Raw)
	}
	if mine := list(e.call("GET", "/api/trades/mine", tok, nil).Raw); len(mine) != 0 {
		t.Fatal("a trade at a price nobody agreed to was made")
	}
	right := trade("buy", "ACME", 1)
	right["expectedPrice"] = px
	if r := e.call("POST", "/api/trades", tok, right); r.Status != 200 {
		t.Fatalf("the price the person saw: %d %s", r.Status, r.Raw)
	}
}

func TestFreezeStopsTradesAndResumeRestoresThem(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")

	// The console asks only for confirmation, so an action needs no reason.
	if r := e.call("POST", "/api/admin/control/freeze", adm, map[string]any{"frozen": true}); r.Status != 200 {
		t.Fatalf("freeze: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 423 {
		t.Fatalf("frozen: %d %s", r.Status, r.Raw)
	}
	if r := e.call("GET", "/api/config", tok, nil); r.Body["tradingFrozen"] != true {
		t.Fatalf("config does not say frozen: %s", r.Raw)
	}
	e.call("POST", "/api/admin/control/freeze", adm, map[string]any{"frozen": false, "reason": "drill over"})
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("after resume: %d %s", r.Status, r.Raw)
	}
	log := string(e.call("GET", "/api/admin/audit", adm, nil).Raw)
	for _, want := range []string{"Froze trading", "Resumed trading", "drill over", "Organiser"} {
		if !strings.Contains(log, want) {
			t.Errorf("audit log is missing %q: %s", want, log)
		}
	}
}

func TestADisqualifiedTeamCannotTrade(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, id := e.signup("Cheater")
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("setup: %d", r.Status)
	}
	if r := e.call("POST", "/api/admin/accounts/"+id+"/disqualify", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("disqualify: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 403 {
		t.Fatalf("disqualified team traded: %d", r.Status)
	}
	if mine := list(e.call("GET", "/api/trades/mine", tok, nil).Raw); len(mine) != 1 {
		t.Fatal("trades are final: disqualifying must not erase them")
	}
}

func TestPricesAreSetByTheSimulationAndPushedToEveryBrowser(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	tok, _ := e.signup("Alice")
	e.openMarket(adm)

	c := wsDial(t, e, tok)
	defer c.Close()
	wsReady(t, c)
	var got struct {
		Prices []struct {
			Symbol string
			Price  float64
		}
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) && len(got.Prices) == 0 {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var f struct {
			T string          `json:"t"`
			D json.RawMessage `json:"d"`
		}
		if err := c.ReadJSON(&f); err != nil {
			t.Fatalf("waiting for a price update: %v", err)
		}
		if f.T == "prices" {
			_ = json.Unmarshal(f.D, &got)
		}
	}
	if len(got.Prices) != len(universe.Default()) {
		t.Fatalf("a price update carried %d companies, want %d", len(got.Prices), len(universe.Default()))
	}

	// A trade is confirmed to the team that made it, over its own socket.
	e.call("POST", "/api/trades", tok, trade("buy", "ACME", 2))
	sawTrade := false
	for i := 0; i < 20 && !sawTrade; i++ {
		_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
		var f struct{ T string }
		if err := c.ReadJSON(&f); err != nil {
			break
		}
		sawTrade = f.T == "trade"
	}
	if !sawTrade {
		t.Fatal("the team was not told about its own trade")
	}

	// A bad token is refused with the close code the web app treats as "sign in again".
	bad := wsDial(t, e, "not-a-token")
	defer bad.Close()
	if code := wsClosedWith(bad); code != 4401 {
		t.Fatalf("close code for a bad token = %d, want 4401", code)
	}
}

func TestStateSurvivesARestart(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()
	e.openMarket(adm)
	tok, id := e.signup("Alice")
	held := trade("buy", "ACME", 7)
	e.call("POST", "/api/trades", tok, held)
	e.call("POST", "/api/trades", tok, trade("buy", "GLOBEX", 3))
	e.call("POST", "/api/trades", tok, trade("sell", "ACME", 2))
	e.grant(adm, id, "WONKA", 50)

	snap := func(e *env) string {
		return string(e.call("GET", "/api/portfolio/me", tok, nil).Raw) + string(e.call("GET", "/api/trades/mine", tok, nil).Raw)
	}
	before := snap(e)
	e2 := e.restart(r)
	if after := snap(e2); after != before {
		t.Fatalf("state changed across the restart:\n before %s\n after  %s", before, after)
	}
	// A retry of a trade sent before the "crash" returns the original instead of trading again.
	if r := e2.call("POST", "/api/trades", tok, held); r.Status != 200 || r.Body["deduped"] != true {
		t.Fatalf("retry after restart: %d %s", r.Status, r.Raw)
	}
	// The event clock kept its place, so the market is still open, and prices are still there.
	if r := e2.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("market state lost across the restart: %d %s", r.Status, r.Raw)
	}
	if e2.price(tok, "ACME") <= 0 {
		t.Fatal("prices were lost across the restart")
	}
}

func TestATradeThatCannotBeSavedNeverHappens(t *testing.T) {
	wal := store.NewMem()
	e := newEnv(t, wal, rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")

	wal.Fail = fmt.Errorf("disk full")
	r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 5))
	wal.Fail = nil
	if r.Status != 503 {
		t.Fatalf("a trade during a storage failure: %d %s", r.Status, r.Raw)
	}
	if a := e.call("GET", "/api/auth/me", tok, nil); num(a.Body["cashBalance"]) != 1_000_000 {
		t.Fatalf("cash = %v, an unsaved trade must not change it", a.Body["cashBalance"])
	}
	if mine := list(e.call("GET", "/api/trades/mine", tok, nil).Raw); len(mine) != 0 {
		t.Fatal("an unsaved trade is in the history")
	}
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 5)); r.Status != 200 {
		t.Fatalf("recovery after the storage failure: %d %s", r.Status, r.Raw)
	}
}

func TestOrganiserScreensReturnData(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.signup("Alice")
	for _, p := range []string{"overview", "systems", "accounts", "news", "disputes", "audit", "rulebook", "standings", "sim"} {
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
	if r := e.call("POST", "/api/admin/news", adm, map[string]any{"kind": "news", "headline": "Markets open"}); r.Status != 200 {
		t.Fatalf("news: %d %s", r.Status, r.Raw)
	}
	if nd := e.call("GET", "/api/admin/news", adm, nil); len(nd.Body["items"].([]any)) != 1 {
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
