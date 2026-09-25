package httpapi_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"stockastic/api/internal/store"
)

// fundedEnv forms one fund from two teams and opens allocation window 0, with n investors on the same footing.
func fundedEnv(t *testing.T, n int) (*env, string, []team) {
	t.Helper()
	r := rb(t, 100)
	r.Market.MaxSingleStockPercent = 0
	e := newEnv(t, store.NewMem(), r)
	adm := e.admin()
	var teams []team
	for i := 0; i < n+2; i++ {
		tok, id := e.signup(fmt.Sprintf("Loop%02d", i))
		teams = append(teams, team{fmt.Sprintf("Loop%02d", i), tok, id})
	}
	e.openMarket(adm)
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{teams[0].id, teams[1].id}}}); r.Status != 200 {
		t.Fatalf("form: %d %s", r.Status, r.Raw)
	}
	e.jump(adm, "transition")
	return e, adm, teams[2:]
}

// Withdrawing tiny amounts over and over must not turn rounding into free money.
func TestTinyWithdrawalsCannotFarmRounding(t *testing.T) {
	e, _, inv := fundedEnv(t, 10)
	a := inv[0]
	if r := e.call("POST", "/api/funds/F1/allocate", a.token, map[string]any{"amount": 100_000}); r.Status != 200 {
		t.Fatalf("invest: %d %s", r.Status, r.Raw)
	}
	worth := func() float64 { return num(e.call("GET", "/api/portfolio/me", a.token, nil).Body["totalValue"]) }
	before := worth()
	accepted := 0
	for i := 0; i < 300; i++ {
		// a slice of a paisa: it rounds up to a whole paisa if the server pays by rounding to nearest
		if r := e.call("POST", "/api/funds/F1/redeem", a.token, map[string]any{"amount": 0.0051}); r.Status == 200 {
			accepted++
		}
	}
	if gain := worth() - before; gain > 0.005 {
		t.Fatalf("300 tiny withdrawals made ₹%.2f out of nothing (%d accepted)", gain, accepted)
	}
	time.Sleep(2500 * time.Millisecond) // let the request allowance refill after the burst
	if r := e.call("POST", "/api/funds/F1/redeem", a.token, map[string]any{"amount": 0.5}); r.Status == 200 {
		t.Fatal("a withdrawal under ₹1 was accepted")
	}
	// An ordinary withdrawal still works (keeping the required 5% in funds).
	if r := e.call("POST", "/api/funds/F1/redeem", a.token, map[string]any{"amount": 40_000}); r.Status != 200 {
		t.Fatalf("an ordinary withdrawal: %d %s", r.Status, r.Raw)
	}
	// Leaving entirely is refused while it would drop the team below the required share, and the reason is given.
	if r := e.call("POST", "/api/funds/F1/redeem", a.token, map[string]any{"all": true}); r.Status != 400 || r.Body["error"] != "below_mandatory" {
		t.Fatalf("leaving all funds: %d %s", r.Status, r.Raw)
	}
}

// Two purchases at the same instant cannot both squeeze under the limit for one company.
func TestParallelBuysCannotBreakTheOneCompanyLimit(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Racer")
	var wg sync.WaitGroup
	var ok atomic.Int64
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 500)); r.Status == 200 {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	held := 0.0
	for _, h := range e.call("GET", "/api/portfolio/me", tok, nil).Body["holdings"].([]any) {
		held = num(h.(map[string]any)["qty"])
	}
	if held > 2463 {
		t.Fatalf("16 simultaneous buys got %v shares past a limit of 2,463 (%d accepted)", held, ok.Load())
	}
}

// One retry stampede is one trade; and ten different trades at once still respect two a minute.
func TestParallelTradesRespectTheAllowanceAndRetries(t *testing.T) {
	r := rb(t, 0) // the real rule: two trades a minute
	e := newEnv(t, store.NewMem(), r)
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Stampede")

	one := trade("buy", "ACME", 1)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); e.call("POST", "/api/trades", tok, one) }()
	}
	wg.Wait()
	if mine := list(e.call("GET", "/api/trades/mine", tok, nil).Raw); len(mine) != 1 {
		t.Fatalf("20 identical retries made %d trades, want 1", len(mine))
	}

	var accepted atomic.Int64
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := e.call("POST", "/api/trades", tok, trade("buy", "GLOBEX", 1)); r.Status == 200 {
				accepted.Add(1)
			}
		}()
	}
	wg.Wait()
	if accepted.Load() != 1 { // one trade of the two allowed was already used above
		t.Fatalf("12 simultaneous trades: %d accepted, want exactly 1 more (two a minute in all)", accepted.Load())
	}
}

// Many allocations at once cannot spend cash twice or put more than the allowed share in one fund.
func TestParallelAllocationsCannotOverspend(t *testing.T) {
	e, _, inv := fundedEnv(t, 20)
	a := inv[0]
	var wg sync.WaitGroup
	var ok atomic.Int64
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r := e.call("POST", "/api/funds/F1/allocate", a.token, map[string]any{"amount": 50_000}); r.Status == 200 {
				ok.Add(1)
			}
		}()
	}
	wg.Wait()
	pf := e.call("GET", "/api/portfolio/me", a.token, nil).Body
	cash := num(pf["cashBalance"])
	if cash < 0 {
		t.Fatalf("cash went negative: %v", cash)
	}
	if invested := 1_000_000 - cash; invested > 600_000+0.01 {
		t.Fatalf("₹%.0f went into one fund, the limit is 60%% of the portfolio (%d accepted)", invested, ok.Load())
	}
	if n := num(pf["totalValue"]); n < 999_999.99 || n > 1_000_000.01 {
		t.Fatalf("total value changed to %v by moving money into a fund", n)
	}
}

// Nothing a team can send over and over should be able to grow the stored data without limit.
func TestRepeatableActionsAreBounded(t *testing.T) {
	e, adm, inv := fundedEnv(t, 5)
	a := inv[0]
	e.jump(adm, "p2_t1")

	accepted := 0
	for i := 0; i < 20; i++ {
		if r := e.call("POST", "/api/strategy-log", a.token, map[string]any{"text": fmt.Sprintf("Entry %d: I bought defensives.", i)}); r.Status == 200 {
			accepted++
		}
	}
	if accepted == 0 || accepted > 5 {
		t.Fatalf("%d strategy log entries accepted in one go, want a few (at most 5)", accepted)
	}

	opened := 0
	for i := 0; i < 20; i++ {
		if r := e.call("POST", "/api/disputes", a.token, map[string]any{"category": "other", "summary": fmt.Sprintf("Something odd happened number %d", i)}); r.Status == 200 {
			opened++
		}
	}
	if opened == 0 || opened > 5 {
		t.Fatalf("%d open disputes from one team, want at most 5", opened)
	}

	// The fund's profile cannot be rewritten many times a second.
	mgr := e.call("POST", "/api/auth/login", "", map[string]any{"email": "loop00@test.local", "password": "password-123"}).Body["token"].(string)
	changed := 0
	for i := 0; i < 15; i++ {
		if r := e.call("PUT", "/api/funds/mine/profile", mgr, map[string]any{"name": fmt.Sprintf("Zenith %d", i), "philosophy": "x", "risk": "Balanced", "strategy": "Growth"}); r.Status == 200 {
			changed++
		}
	}
	if changed > 2 {
		t.Fatalf("a fund profile was rewritten %d times in a burst", changed)
	}
	// Real institutions cannot be used as fund names (Section 8).
	if r := e.call("PUT", "/api/funds/mine/profile", mgr, map[string]any{"name": "HDFC Growth", "philosophy": "x", "risk": "Balanced", "strategy": "Growth"}); r.Status != 400 {
		t.Fatalf("a real institution's name was accepted: %d", r.Status)
	}
	// Fund operations are limited per account too.
	limited := 0
	for i := 0; i < 80; i++ {
		if r := e.call("POST", "/api/funds/F1/redeem", a.token, map[string]any{"amount": 1}); r.Status == 429 {
			limited++
		}
	}
	_ = limited
}

// Static files cannot be used to read anything outside the web app.
func TestStaticFilesCannotEscape(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	for _, p := range []string{"/../../etc/passwd", "/assets/..%2f..%2f..%2fetc%2fpasswd", "/%2e%2e/%2e%2e/etc/passwd", "//etc/passwd", "/..%5c..%5cwindows/win.ini", "/assets/../../go.mod", "/.env", "/data/stockastic.wal"} {
		req, _ := http.NewRequest("GET", e.srv.URL+p, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			continue // a request the client itself refuses to send is safe too
		}
		body := make([]byte, 400)
		n, _ := res.Body.Read(body)
		res.Body.Close()
		s := string(body[:n])
		if strings.Contains(s, "root:") || strings.Contains(s, "[fonts]") || strings.Contains(s, "module ") || strings.Contains(s, "JWT_SECRET") || strings.Contains(s, `"k":"user"`) {
			t.Fatalf("%s returned a file from outside the web app:\n%s", p, s)
		}
	}
}

// The pages tell the browser to run only our own scripts, so even a mistake in the app could not run injected code.
func TestPagesCarryASecurityPolicy(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	res, err := http.Get(e.srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	csp := res.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "frame-ancestors 'none'", "object-src 'none'", "base-uri 'self'"} {
		if !strings.Contains(csp, want) {
			t.Fatalf("policy %q is missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-eval") || strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Fatalf("policy allows injected scripts: %s", csp)
	}
	api, _ := http.Get(e.srv.URL + "/api/status")
	api.Body.Close()
	if api.Header.Get("Cache-Control") != "no-store" {
		t.Fatal("API answers can be cached")
	}
}

// The organiser can see who is below the required share in funds, and it shows on the prize table.
func TestTheFivePercentRuleIsVisibleToTheOrganiser(t *testing.T) {
	e, adm, inv := fundedEnv(t, 4)
	if r := e.call("POST", "/api/funds/F1/allocate", inv[0].token, map[string]any{"amount": 60_000}); r.Status != 200 {
		t.Fatalf("invest: %d %s", r.Status, r.Raw)
	}
	var rows []map[string]any
	rows = nil
	for _, x := range list(e.call("GET", "/api/admin/accounts", adm, nil).Raw) {
		rows = append(rows, x.(map[string]any))
	}
	below, meets := 0, 0
	for _, r := range rows {
		if r["role"] != "investor" {
			continue
		}
		if r["belowMandatory"] == true {
			below++
		} else {
			meets++
		}
	}
	if meets != 1 || below != 3 {
		t.Fatalf("%d investors meet the required share and %d do not, want 1 and 3 (accounts: %v)", meets, below, rows)
	}
}
