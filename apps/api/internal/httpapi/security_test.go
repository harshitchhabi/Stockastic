package httpapi_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"

	"stockastic/api/internal/app"
	"stockastic/api/internal/store"
)

var publicRoutes = map[string]bool{
	"GET /healthz": true, "GET /readyz": true, "GET /ws": true, "GET /api/status": true,
	"POST /api/auth/signup": true, "POST /api/auth/login": true,
}

// Every route that is not public needs a login, and every organiser route needs an organiser. This walks the
// real route table, so a route added later without protection fails here.
func TestEveryRouteIsProtected(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	team, _ := e.signup("Alice")
	routes := e.h.(*gin.Engine).Routes()
	if len(routes) < 60 {
		t.Fatalf("only %d routes found: the walk is not seeing them all", len(routes))
	}
	admin, other := 0, 0
	for _, r := range routes {
		if publicRoutes[r.Method+" "+r.Path] {
			continue
		}
		path := strings.NewReplacer(":id", "x", ":symbol", "ACME", ":i", "0", ":name", "phase1", ":account", "x").Replace(r.Path)
		if strings.Contains(path, ":") {
			t.Fatalf("route %s has a parameter the test does not fill in", r.Path)
		}
		if res := e.call(r.Method, path, "", map[string]any{}); res.Status != 401 && res.Status != 404 {
			t.Errorf("%s %s with no login: %d, want 401", r.Method, r.Path, res.Status)
		}
		if res := e.call(r.Method, path, "not.a.token", map[string]any{}); res.Status != 401 {
			t.Errorf("%s %s with a bad token: %d, want 401", r.Method, r.Path, res.Status)
		}
		if strings.HasPrefix(r.Path, "/api/admin") {
			admin++
			if res := e.call(r.Method, path, team, map[string]any{}); res.Status != 403 {
				t.Errorf("%s %s as a team: %d, want 403", r.Method, r.Path, res.Status)
			}
		} else {
			other++
		}
	}
	t.Logf("checked %d organiser routes and %d others", admin, other)
	if admin < 40 {
		t.Fatalf("only %d organiser routes checked", admin)
	}
	_ = adm
}

func TestForgedAndTamperedTokensAreRefused(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	tok, id := e.signup("Alice")
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("token = %q", tok)
	}
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	forged := map[string]string{
		"alg none":        b64(`{"alg":"none","typ":"JWT"}`) + "." + parts[1] + ".",
		"wrong signature": parts[0] + "." + parts[1] + "." + b64("nope"),
		"other payload":   parts[0] + "." + b64(`{"sub":"`+id+`","admin":true,"exp":9999999999}`) + "." + parts[2],
		"empty":           "",
		"garbage":         "aaaa.bbbb.cccc",
		"huge":            strings.Repeat("a", 100_000),
	}
	for name, tk := range forged {
		if r := e.call("GET", "/api/auth/me", tk, nil); r.Status != 401 {
			t.Errorf("%s: %d, want 401", name, r.Status)
		}
	}
	// A team's own token never makes it an organiser, however the request is dressed.
	if r := e.call("GET", "/api/admin/overview", tok, nil); r.Status != 403 {
		t.Fatalf("a team on an organiser route: %d", r.Status)
	}
	// Extra fields in a sign-up cannot grant power.
	r := e.call("POST", "/api/auth/signup", "", map[string]any{"displayName": "Mallory", "email": "m@test.local", "password": "password-123",
		"isAdmin": true, "role": "fund_manager", "cashBalance": 999999999, "status": "active"})
	if r.Status != 200 {
		t.Fatalf("signup: %d %s", r.Status, r.Raw)
	}
	acc := r.Body["account"].(map[string]any)
	if acc["isAdmin"] != false || acc["role"] != "investor" || num(acc["cashBalance"]) != 1_000_000 {
		t.Fatalf("a sign-up granted itself power: %v", acc)
	}
}

func TestHostileInputIsRefusedNotCrashed(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	e.openMarket(adm)
	tok, _ := e.signup("Alice")

	post := func(path string, raw string) int {
		req, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	cases := map[string]string{
		"huge quantity":       `{"clientTradeId":"a","symbol":"ACME","side":"buy","qty":99999999999999999999}`,
		"quantity past limit": `{"clientTradeId":"a","symbol":"ACME","side":"buy","qty":10000001}`,
		"negative quantity":   `{"clientTradeId":"a","symbol":"ACME","side":"buy","qty":-1}`,
		"fractional quantity": `{"clientTradeId":"a","symbol":"ACME","side":"buy","qty":1.5}`,
		"string quantity":     `{"clientTradeId":"a","symbol":"ACME","side":"buy","qty":"1"}`,
		"negative price":      `{"clientTradeId":"a","symbol":"ACME","side":"buy","qty":1,"expectedPrice":-5}`,
		"huge price":          `{"clientTradeId":"a","symbol":"ACME","side":"buy","qty":1,"expectedPrice":1e308}`,
		"path in symbol":      `{"clientTradeId":"a","symbol":"../../etc/passwd","side":"buy","qty":1}`,
		"script in symbol":    `{"clientTradeId":"a","symbol":"<script>alert(1)</script>","side":"buy","qty":1}`,
		"long client id":      `{"clientTradeId":"` + strings.Repeat("x", 500) + `","symbol":"ACME","side":"buy","qty":1}`,
		"null body":           `null`,
		"array body":          `[1,2,3]`,
		"not json":            `{{{{`,
		"empty":               ``,
		"deeply nested":       strings.Repeat(`{"a":`, 5000) + `1` + strings.Repeat(`}`, 5000),
		"oversized":           `{"clientTradeId":"` + strings.Repeat("x", 200_000) + `"}`,
		"unknown side":        `{"clientTradeId":"a","symbol":"ACME","side":"short","qty":1}`,
		"null bytes":          "{\"clientTradeId\":\"a\\u0000b\",\"symbol\":\"ACME\",\"side\":\"buy\",\"qty\":1}",
	}
	for name, raw := range cases {
		if code := post("/api/trades", raw); code < 400 || code >= 500 {
			t.Errorf("%s: status %d, want a 4xx refusal", name, code)
		}
	}
	// Organiser inputs are checked too.
	for name, raw := range map[string]string{
		"grant past limit":   `{"accountId":"*","symbol":"ACME","qty":99999999999,"price":100}`,
		"grant zero price":   `{"accountId":"*","symbol":"ACME","qty":1,"price":0}`,
		"grant bad symbol":   `{"accountId":"*","symbol":"NOPE","qty":1,"price":1}`,
		"cash absurd":        `{"amount":1e30}`,
		"news huge headline": `{"kind":"news","headline":"` + strings.Repeat("h", 100_000) + `"}`,
	} {
		path := "/api/admin/grants"
		switch {
		case strings.HasPrefix(name, "cash"):
			path = "/api/admin/accounts/x/cash"
		case strings.HasPrefix(name, "news"):
			path = "/api/admin/news"
		}
		admReq, _ := http.NewRequest("POST", e.srv.URL+path, strings.NewReader(raw))
		admReq.Header.Set("Authorization", "Bearer "+adm)
		admReq.Header.Set("Content-Type", "application/json")
		res, err := http.DefaultClient.Do(admReq)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode < 400 || res.StatusCode >= 500 {
			t.Errorf("organiser %s: status %d, want a 4xx refusal", name, res.StatusCode)
		}
	}
	// The team is untouched by all that, and the server is still healthy.
	if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 200 || num(r.Body["cashBalance"]) != 1_000_000 {
		t.Fatalf("after the hostile input: %d %v", r.Status, r.Body["cashBalance"])
	}
	if r := e.call("POST", "/api/trades", tok, trade("buy", "ACME", 1)); r.Status != 200 {
		t.Fatalf("a normal trade after the hostile input: %d %s", r.Status, r.Raw)
	}
}

func TestSignupIsGuarded(t *testing.T) {
	e := newEnvWith(t, store.NewMem(), rb(t, 100), testScenario(), func(c *app.Config) { c.SignupCode = "ROOM-42"; c.MaxAccounts = 6 })
	adm := e.admin()
	body := func(name, email, code string) map[string]any {
		return map[string]any{"displayName": name, "email": email, "password": "password-123", "eventCode": code}
	}
	if st := e.call("GET", "/api/status", "", nil).Body; st["signupNeedsCode"] != true {
		t.Fatalf("status = %v", st)
	}
	if r := e.call("POST", "/api/auth/signup", "", body("Alice", "alice@test.local", "")); r.Status != 403 || r.Body["error"] != "wrong_event_code" {
		t.Fatalf("no code: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/auth/signup", "", body("Alice", "alice@test.local", "room-42")); r.Status != 403 {
		t.Fatalf("a code with the wrong case: %d", r.Status)
	}
	if r := e.call("POST", "/api/auth/signup", "", body("Alice", "alice@test.local", "ROOM-42")); r.Status != 200 {
		t.Fatalf("the right code: %d %s", r.Status, r.Raw)
	}
	// One person cannot open many accounts from one mailbox.
	for _, dup := range []string{"alice+2@test.local", "ALICE@test.local", "alice+x+y@test.local"} {
		if r := e.call("POST", "/api/auth/signup", "", body("Alice2", dup, "ROOM-42")); r.Status != 409 {
			t.Errorf("%s was accepted as a second account: %d", dup, r.Status)
		}
	}
	if r := e.call("POST", "/api/auth/signup", "", body("Gee", "g.mail@gmail.com", "ROOM-42")); r.Status != 200 {
		t.Fatalf("gmail: %d %s", r.Status, r.Raw)
	}
	if r := e.call("POST", "/api/auth/signup", "", body("Gee2", "gmail@gmail.com", "ROOM-42")); r.Status != 409 {
		t.Fatalf("a gmail dot variant was accepted as a second account: %d", r.Status)
	}
	// Names cannot hide text or run as markup.
	for _, name := range []string{"<b>Bob</b>", "Bo​b", "Bob‮evil", "Bo\x00b", "B"} {
		if r := e.call("POST", "/api/auth/signup", "", body(name, "bob@test.local", "ROOM-42")); r.Status != 400 {
			t.Errorf("the name %q was accepted: %d", name, r.Status)
		}
	}
	// The organiser can change the code, and the cap stops a flood.
	if r := e.call("POST", "/api/admin/settings/signup-code", adm, map[string]any{"code": ""}); r.Status != 200 {
		t.Fatalf("clear the code: %d", r.Status)
	}
	full := 0
	for i := 0; i < 8; i++ {
		r := e.call("POST", "/api/auth/signup", "", body(fmt.Sprintf("Team%d", i), fmt.Sprintf("t%d@test.local", i), ""))
		if r.Status == 403 && r.Body["error"] == "accounts_full" {
			full++
		}
	}
	if full == 0 {
		t.Fatal("the account cap never stopped a sign-up")
	}
}

func TestGuessingAPasswordLocksOnlyThatEmailAndNeverThrowsAnyoneOut(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	alice, _ := e.signup("Alice")
	e.signup("Bob")
	login := func(email, pw string) resp {
		return e.call("POST", "/api/auth/login", "", map[string]any{"email": email, "password": pw})
	}
	locked := 0
	for i := 0; i < 14; i++ {
		if login("alice@test.local", fmt.Sprintf("guess-%d", i)).Status == 429 {
			locked++
		}
	}
	if locked == 0 {
		t.Fatal("fourteen wrong guesses at one email were never slowed")
	}
	if r := login("alice@test.local", "password-123"); r.Status != 429 {
		t.Fatalf("even the right password is refused while the guessing lock lasts, got %d", r.Status)
	}
	// Bob shares the address and is not affected; Alice's open session keeps working.
	if r := login("bob@test.local", "password-123"); r.Status != 200 {
		t.Fatalf("another team could not log in because of guesses at Alice: %d", r.Status)
	}
	if r := e.call("GET", "/api/auth/me", alice, nil); r.Status != 200 {
		t.Fatalf("a signed-in team was thrown out by someone guessing its password: %d", r.Status)
	}
	// A flood of made-up emails is never throttled into blocking honest people, and costs almost nothing.
	t0 := time.Now()
	for i := 0; i < 300; i++ {
		if r := login(fmt.Sprintf("nobody%d@test.local", i), "x-password"); r.Status != 401 {
			t.Fatalf("made-up email %d: %d", i, r.Status)
		}
	}
	if d := time.Since(t0); d > 4*time.Second {
		t.Fatalf("300 made-up logins took %v: they should not cost password work", d)
	}
}

func TestOneAccountCannotJamTheServer(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	tok, _ := e.signup("Alice")
	other, _ := e.signup("Bob")
	limited, ok := 0, 0
	for i := 0; i < 400; i++ {
		switch e.call("GET", "/api/symbols", tok, nil).Status {
		case 429:
			limited++
		case 200:
			ok++
		}
	}
	if limited == 0 {
		t.Fatalf("400 quick requests from one account were all served (%d)", ok)
	}
	// Someone else is not affected by it.
	if r := e.call("GET", "/api/symbols", other, nil); r.Status != 200 {
		t.Fatalf("another team was slowed by the flood: %d", r.Status)
	}
}

func TestSocketsAreLimited(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	tok, id := e.signup("Alice")

	// More sockets than one account may hold: the newest wins and the oldest are closed.
	var conns []*websocket.Conn
	for i := 0; i < 7; i++ {
		c := wsDial(t, e, tok)
		wsReady(t, c)
		conns = append(conns, c)
	}
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	time.Sleep(300 * time.Millisecond)
	if n := e.a.Hub.Sockets(id); n != 4 {
		t.Fatalf("an account holds %d sockets, want at most 4", n)
	}
	if code := wsClosedWith(conns[0]); code != 4429 {
		t.Fatalf("the oldest socket was closed with %d, want 4429", code)
	}

	// A socket that floods messages is cut off.
	flood := wsDial(t, e, tok)
	wsReady(t, flood)
	defer flood.Close()
	for i := 0; i < 500; i++ {
		if err := flood.WriteJSON(map[string]any{"t": "ping"}); err != nil {
			break
		}
	}
	// The server cuts it off; depending on timing the client sees the close code or just a dropped connection.
	if code := wsClosedWith(flood); code != 4429 && code != -1 {
		t.Fatalf("a flooding socket was closed with %d, want 4429 or a dropped connection", code)
	}

	// A frame far bigger than any real message closes the socket.
	big := wsDial(t, e, tok)
	wsReady(t, big)
	defer big.Close()
	_ = big.WriteMessage(websocket.TextMessage, bytes.Repeat([]byte("x"), 1<<20))
	if code := wsClosedWith(big); code == 0 {
		t.Fatal("an oversized frame did not close the socket")
	}

	// A socket that never logs in is dropped by itself.
	idle, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()
	_ = idle.SetReadDeadline(time.Now().Add(9 * time.Second))
	if _, _, err := idle.ReadMessage(); err == nil {
		// the server sends its unauthenticated error, then closes
		if _, _, err := idle.ReadMessage(); err == nil {
			t.Fatal("a socket that never logged in stayed open")
		}
	}
}

func TestExportsCannotRunAFormula(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	adm := e.admin()
	for i, name := range []string{"=HYPERLINK(\"http://evil\")", "+SUM(A1)"} {
		r := e.call("POST", "/api/auth/signup", "", map[string]any{"displayName": name, "email": fmt.Sprintf("f%d@test.local", i), "password": "password-123"})
		if r.Status != 200 {
			t.Fatalf("signup %q: %d %s", name, r.Status, r.Raw)
		}
	}
	out := string(e.call("GET", "/api/admin/export/accounts.csv", adm, nil).Raw)
	for _, bad := range []string{",=HYPERLINK", ",+SUM", "\n=", "\n+"} {
		if strings.Contains(out, bad) {
			t.Fatalf("a spreadsheet would run a team name as a formula (%q):\n%s", bad, out)
		}
	}
	if !strings.Contains(out, "'=HYPERLINK") {
		t.Fatalf("the name is missing from the export:\n%s", out)
	}
}

// Many teams doing everything at once while the organiser changes things underneath them: nothing may return
// a server error, deadlock or lose track of money, and a restart must rebuild exactly the same state.
func TestChaosThenRestartIsIdentical(t *testing.T) {
	wal := store.NewMem()
	r := rb(t, 100)
	r.Fund.MandatoryAllocationPercent = 0.1
	r.Market.MaxSingleStockPercent = 0
	e := newEnvWith(t, wal, r, testScenario())
	adm := e.admin()
	var teams []team
	for i := 0; i < 24; i++ {
		tok, id := e.signup(fmt.Sprintf("Chaos%02d", i))
		teams = append(teams, team{fmt.Sprintf("Chaos%02d", i), tok, id})
		e.grant(adm, id, "ACME", 50+i)
	}
	e.openMarket(adm)
	e.jump(adm, "p1_freeze")
	if r := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": [][]string{{teams[0].id, teams[1].id}, {teams[2].id, teams[3].id}}}); r.Status != 200 {
		t.Fatalf("form: %d %s", r.Status, r.Raw)
	}
	e.jump(adm, "transition") // window 0 open

	var serverErrors, requests, throttled atomic.Int64
	stop := make(chan struct{})
	var wg sync.WaitGroup
	syms := []string{"ACME", "GLOBEX", "INITECH", "UMBRELLA", "STARK", "WAYNE"}
	for i, tm := range teams {
		wg.Add(1)
		go func(i int, tm team) {
			defer wg.Done()
			n := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				n++
				var res resp
				switch n % 7 {
				case 0:
					res = e.call("POST", "/api/funds/F1/allocate", tm.token, map[string]any{"amount": 5000})
				case 1:
					res = e.call("POST", "/api/funds/F2/redeem", tm.token, map[string]any{"all": true})
				case 2:
					res = e.call("GET", "/api/portfolio/me", tm.token, nil)
				case 3:
					res = e.call("GET", "/api/funds", tm.token, nil)
				default:
					side := []string{"buy", "sell"}[(i+n)%2]
					res = e.call("POST", "/api/trades", tm.token, trade(side, syms[(i+n)%len(syms)], 1+n%5))
				}
				requests.Add(1)
				if res.Status == 429 {
					throttled.Add(1)
				}
				if res.Status >= 500 && res.Status != 503 {
					serverErrors.Add(1)
					t.Errorf("server error %d on request %d of team %d: %s", res.Status, n, i, res.Raw)
				}
				time.Sleep(time.Duration(35+i%7) * time.Millisecond) // about 25 requests a second, under the limit
			}
		}(i, tm)
	}
	// The organiser keeps changing things while the teams work.
	for i := 0; i < 6; i++ {
		time.Sleep(400 * time.Millisecond)
		e.call("POST", "/api/admin/control/freeze", adm, map[string]any{"frozen": i%2 == 0})
		e.call("POST", "/api/admin/announce", adm, map[string]any{"text": fmt.Sprintf("notice %d", i)})
		e.call("POST", "/api/admin/accounts/"+teams[5+i].id+"/cash", adm, map[string]any{"amount": 1000})
		e.call("POST", "/api/admin/accounts/"+teams[10+i].id+"/shares", adm, map[string]any{"direction": "give", "symbol": "WONKA", "qty": 3, "price": 40})
		e.call("GET", "/api/admin/accounts", adm, nil)
		e.call("GET", "/api/admin/funds", adm, nil)
		e.call("GET", "/api/admin/trades", adm, nil)
	}
	e.call("POST", "/api/admin/control/freeze", adm, map[string]any{"frozen": false})
	close(stop)
	wg.Wait()
	t.Logf("%d requests, %d throttled, %d server errors", requests.Load(), throttled.Load(), serverErrors.Load())
	if throttled.Load() > requests.Load()/10 {
		t.Fatalf("%d of %d requests were throttled: the test is not exercising the server", throttled.Load(), requests.Load())
	}

	snapshot := func(e *env) string {
		time.Sleep(3 * time.Second) // let every account's request allowance refill
		var b strings.Builder
		for _, tm := range teams {
			b.WriteString(string(e.call("GET", "/api/portfolio/me", tm.token, nil).Raw))
			b.WriteString(string(e.call("GET", "/api/trades/mine", tm.token, nil).Raw))
		}
		b.WriteString(string(e.call("GET", "/api/admin/funds", e.admin(), nil).Raw))
		return b.String()
	}
	// The event clock and prices keep moving, so compare what must be exact: money, holdings, trades, funds.
	strip := func(s string) string { return s }
	before := strip(snapshot(e))
	e2 := e.restart(r)
	after := strip(snapshot(e2))
	if before != after {
		t.Fatalf("state after a restart is different from before it\nbefore: %s\nafter:  %s", clip(before), clip(after))
	}

	// Then the organiser resets the whole event in the middle of activity, and it is clean.
	stop2 := make(chan struct{})
	var wg2 sync.WaitGroup
	for i, tm := range teams[:8] {
		wg2.Add(1)
		go func(i int, tm team) {
			defer wg2.Done()
			for n := 0; ; n++ {
				select {
				case <-stop2:
					return
				default:
				}
				res := e2.call("POST", "/api/trades", tm.token, trade("buy", syms[(i+n)%len(syms)], 1))
				if res.Status >= 500 && res.Status != 503 {
					t.Errorf("server error %d during the reset: %s", res.Status, res.Raw)
				}
				time.Sleep(40 * time.Millisecond)
			}
		}(i, tm)
	}
	time.Sleep(300 * time.Millisecond)
	if r := e2.call("POST", "/api/admin/event/reset", e2.admin(), map[string]any{}); r.Status != 200 {
		t.Fatalf("reset during activity: %d %s", r.Status, r.Raw)
	}
	time.Sleep(200 * time.Millisecond)
	close(stop2)
	wg2.Wait()
}

func clip(s string) string {
	if len(s) > 1500 {
		return s[:1500] + "…"
	}
	return s
}

var _ = json.Marshal

func TestTheServerRefusesMoreSocketsThanItsLimit(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	e.a.Hub.SetLimits(5, 0)
	var conns []*websocket.Conn
	defer func() {
		for _, c := range conns {
			c.Close()
		}
	}()
	for i := 0; i < 5; i++ {
		tok, _ := e.signup(fmt.Sprintf("Sock%d", i))
		c := wsDial(t, e, tok)
		wsReady(t, c)
		conns = append(conns, c)
	}
	_, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(e.srv.URL, "http")+"/ws", nil)
	if err == nil {
		t.Fatal("a sixth socket was accepted past the limit of five")
	}
	if resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("the refusal was %v, want 503", resp)
	}
}

func TestOrganiserCanAlwaysSignInFromAnywhere(t *testing.T) {
	e := newEnv(t, store.NewMem(), rb(t, 100))
	for i := 0; i < 30; i++ {
		e.call("POST", "/api/auth/login", "", map[string]any{"email": adminEmail, "password": fmt.Sprintf("wrong-guess-%d", i)})
	}
	for _, from := range []string{"", "203.0.113.9", "198.51.100.77"} {
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/auth/login", strings.NewReader(fmt.Sprintf(`{"email":%q,"password":%q}`, adminEmail, adminPass)))
		req.Header.Set("Content-Type", "application/json")
		if from != "" {
			req.Header.Set("X-Forwarded-For", from)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Fatalf("the organiser could not sign in (via %q) after guesses at the account: %d", from, res.StatusCode)
		}
	}
	// And the console works with that login from a browser on any origin the server is reached by.
	if r := e.call("GET", "/api/admin/overview", e.admin(), nil); r.Status != 200 {
		t.Fatalf("console: %d", r.Status)
	}
}
