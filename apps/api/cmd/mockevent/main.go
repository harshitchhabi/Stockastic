// Command mockevent rehearses the whole event against a running test server, the way the day will go: every person
// arrives through Google (the signed pass the server hands out after Google), leaders create teams and teammates join
// with the team code, everyone keeps a live connection open, and the organiser moves the event on step by step, from
// Phase 1 trading to the final close. At each step it checks what must work and what must be refused, and prints a
// PASS/FAIL line for each check.
//
//	go run ./cmd/mockevent -url http://127.0.0.1:8099 -admin-email a@b.c -admin-password ... -secret $JWT_SECRET
//
// The server must have Google sign-in configured (any client ID will do: no call reaches Google) and a fresh, empty
// data store. -secret is the server's JWT_SECRET, used only to write the passes Google would otherwise lead to.
// It creates hundreds of accounts and trades: never point it at the live event.
package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

var (
	base      = flag.String("url", "http://127.0.0.1:8099", "server URL")
	adminMail = flag.String("admin-email", "", "organiser email")
	adminPass = flag.String("admin-password", "", "organiser password")
	secret    = flag.String("secret", "", "the server's JWT_SECRET (to write Google passes)")
	domain    = flag.String("domain", "vitstudent.ac.in", "the college's email domain")
	nTeams    = flag.Int("teams", 350, "teams")
	nMates    = flag.Int("teammates", 2, "teammates who join each team besides the leader")
	saveTo    = flag.String("save", "", "write the final standings to this file")
	compareTo = flag.String("compare", "", "only compare the server's standings now with this file (after a restart)")
	password  = flag.String("password", "", "register everyone by email and this password instead of through Google (a local demo whose logins you can use)")
	p1Minutes = flag.Int("phase1-minutes", 0, "keep Phase 1 trading this many minutes longer, with larger trades (for a demo)")
	stopAt1   = flag.Bool("stop-after-phase1", false, "stop once Phase 1 has closed and leave the server there, to carry on by hand")
)

// ---- checks ----

var (
	checksMu sync.Mutex
	checks   []string
	failed   int
)

func check(name string, ok bool, detail string, args ...any) {
	checksMu.Lock()
	defer checksMu.Unlock()
	d := fmt.Sprintf(detail, args...)
	line := "PASS  " + name
	if !ok {
		line = "FAIL  " + name
		failed++
	}
	if d != "" {
		line += "  (" + d + ")"
	}
	checks = append(checks, line)
	fmt.Println(line)
}

// ---- HTTP ----

type person struct {
	name, email string
	team        int
	leader      bool
	token, id   string // id: the team's account ID
	c           *http.Client
	control     atomic.Int64 // "control" messages on this person's socket
	announce    atomic.Int64
	rules       atomic.Int64
	watchlist   atomic.Int64
	ready       atomic.Bool
}

func newClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 2, IdleConnTimeout: 10 * time.Minute}}
}

var adminClient = newClient()

func call(c *http.Client, method, path, token string, body any) (int, []byte) {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, *base+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := c.Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func errCode(b []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(b, &e)
	return e.Error
}

func decode[T any](b []byte) T {
	var v T
	_ = json.Unmarshal(b, &v)
	return v
}

func parallel(n, workers int, f func(i int)) {
	var wg sync.WaitGroup
	ch := make(chan int)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range ch {
				f(i)
			}
		}()
	}
	for i := 0; i < n; i++ {
		ch <- i
	}
	close(ch)
	wg.Wait()
}

// ---- the pass Google leads to ----

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func pass(email, name string, expires time.Time) string {
	raw, _ := json.Marshal(map[string]any{"e": email, "n": name, "x": expires.Unix()})
	p := b64(raw)
	m := hmac.New(sha256.New, []byte("oauth-state:"+*secret))
	m.Write([]byte("onboard:" + p))
	return p + "." + b64(m.Sum(nil))
}

// ---- server views ----

type standing struct {
	AccountID string  `json:"accountId"`
	Team      string  `json:"team"`
	Role      string  `json:"role"`
	Value     float64 `json:"value"`
	Trades    int     `json:"trades"`
}

type fundStanding struct {
	FundID     string  `json:"fundId"`
	NAV        float64 `json:"nav"`
	AUM        float64 `json:"aum"`
	Investors  int     `json:"investors"`
	FeePercent float64 `json:"feePercent"`
}

type standings struct {
	Teams     []standing     `json:"teams"`
	Investors []standing     `json:"investors"`
	Funds     []fundStanding `json:"funds"`
}

type fundInfo struct {
	ID      string  `json:"id"`
	NAV     float64 `json:"nav"`
	Room    float64 `json:"room"`
	MyValue float64 `json:"myValue"`
	MyUnits float64 `json:"myUnits"`
}

type fundsView struct {
	Formed         bool       `json:"formed"`
	WindowOpen     bool       `json:"windowOpen"`
	Window         int        `json:"window"`
	MandatoryPct   float64    `json:"mandatoryPercent"`
	MinAbsolute    float64    `json:"minAbsolute"`
	MinWalletPct   float64    `json:"minWalletPercent"`
	MaxWalletPct   float64    `json:"maxWalletPercent"`
	MyValueInFunds float64    `json:"myValueInFunds"`
	Compliant      bool       `json:"compliant"`
	Funds          []fundInfo `json:"funds"`
}

type portfolio struct {
	Cash     float64 `json:"cashBalance"`
	Total    float64 `json:"totalValue"`
	FundID   string  `json:"fundId"`
	Holdings []struct {
		Symbol string `json:"symbol"`
		Qty    int64  `json:"qty"`
	} `json:"holdings"`
}

type company struct {
	Symbol string  `json:"symbol"`
	Price  float64 `json:"lastPrice"`
	Open   float64 `json:"openPrice"`
}

var adm string

func admin(method, path string, body map[string]any) (int, []byte) {
	if body != nil {
		body["reason"] = "mock event"
	}
	return call(adminClient, method, "/api/admin/"+path, adm, body)
}

func getStandings() standings {
	_, b := admin("GET", "standings", nil)
	return decode[standings](b)
}

func main() {
	flag.Parse()
	if *adminMail == "" || *adminPass == "" {
		fmt.Fprintln(os.Stderr, "need -admin-email and -admin-password")
		os.Exit(2)
	}
	st, b := call(adminClient, "POST", "/api/auth/login", "", map[string]any{"email": *adminMail, "password": *adminPass})
	if st != 200 {
		fmt.Fprintf(os.Stderr, "organiser sign-in failed: %d %s\n", st, b)
		os.Exit(1)
	}
	adm = decode[struct{ Token string }](b).Token

	if *compareTo != "" {
		compare()
		return
	}
	if *secret == "" && *password == "" {
		fmt.Fprintln(os.Stderr, "need -secret (or -password)")
		os.Exit(2)
	}
	began := time.Now()

	// ================= arrival =================
	fmt.Println("\n== Before the event ==")
	if *password == "" {
		googleChecks()
	}
	how := "through Google"
	if *password != "" {
		how = "by email and password"
	}
	// create registers a team and join a teammate, the way this run registers people.
	create := func(c *http.Client, email, name, team string) (int, []byte) {
		if *password != "" {
			return call(c, "POST", "/api/auth/signup", "", map[string]any{"displayName": team, "email": email, "password": *password, "yourName": name})
		}
		return call(c, "POST", "/api/auth/onboard", "", map[string]any{"token": pass(email, name, time.Now().Add(30*time.Minute)), "action": "create", "teamName": team})
	}
	join := func(c *http.Client, email, name, code string) (int, []byte) {
		if *password != "" {
			return call(c, "POST", "/api/auth/join", "", map[string]any{"teamCode": code, "name": name, "email": email, "password": *password})
		}
		return call(c, "POST", "/api/auth/onboard", "", map[string]any{"token": pass(email, name, time.Now().Add(30*time.Minute)), "action": "join", "teamCode": code})
	}

	fmt.Printf("\n== %d people arrive %s: %d leaders create teams, %d teammates join ==\n", *nTeams*(1+*nMates), how, *nTeams, *nTeams**nMates)
	teams := make([][]*person, *nTeams)
	var signupErr atomic.Int64
	var errMu sync.Mutex
	firstErrs := map[string]int{}
	t0 := time.Now()
	parallel(*nTeams, 48, func(i int) {
		lead := &person{name: fmt.Sprintf("Leader %d", i), email: fmt.Sprintf("leader%d.mock2026@%s", i, *domain), team: i, leader: true, c: newClient()}
		st, b := create(lead.c, lead.email, lead.name, fmt.Sprintf("Mock Team %03d", i))
		if st != 200 {
			signupErr.Add(1)
			errMu.Lock()
			firstErrs[fmt.Sprintf("create %d %s", st, errCode(b))]++
			errMu.Unlock()
			return
		}
		out := decode[struct {
			Token   string
			Account struct{ ID string }
		}](b)
		lead.token, lead.id = out.Token, out.Account.ID
		_, tb := call(lead.c, "GET", "/api/team", lead.token, nil)
		code := decode[struct{ JoinCode string }](tb).JoinCode
		team := []*person{lead}
		for k := 0; k < *nMates; k++ {
			m := &person{name: fmt.Sprintf("Mate %d-%d", i, k), email: fmt.Sprintf("mate%d.%d.mock2026@%s", i, k, *domain), team: i, c: newClient()}
			st, b := join(m.c, m.email, m.name, code)
			if st != 200 {
				signupErr.Add(1)
				errMu.Lock()
				firstErrs[fmt.Sprintf("join %d %s", st, errCode(b))]++
				errMu.Unlock()
				continue
			}
			out := decode[struct {
				Token   string
				Account struct{ ID string }
			}](b)
			m.token, m.id = out.Token, out.Account.ID
			team = append(team, m)
		}
		teams[i] = team
	})
	var everyone, leaders []*person
	for _, t := range teams {
		if t == nil {
			continue
		}
		leaders = append(leaders, t[0])
		everyone = append(everyone, t...)
	}
	check("every person got in", signupErr.Load() == 0 && len(everyone) == *nTeams*(1+*nMates), "%d people in %s, %d failures %v", len(everyone), time.Since(t0).Round(time.Millisecond), signupErr.Load(), firstErrs)
	sameTeam := 0
	for _, t := range teams {
		if t != nil && len(t) == 1+*nMates && t[1].id == t[0].id && t[len(t)-1].id == t[0].id {
			sameTeam++
		}
	}
	check("every teammate landed in their leader's team", sameTeam == *nTeams, "%d of %d teams complete", sameTeam, *nTeams)

	// a fourth person, the same person twice, a wrong code
	_, tb := call(leaders[0].c, "GET", "/api/team", leaders[0].token, nil)
	code0 := decode[struct{ JoinCode string }](tb).JoinCode
	st, b = join(newClient(), "extra.mock2026@"+*domain, "Extra", code0)
	check("a full team refuses one more person", st != 200, "%d %s", st, errCode(b))
	st, b = join(newClient(), "lost.mock2026@"+*domain, "Lost", "ZZZZZZZZ")
	check("a wrong team code is refused", st != 200, "%d %s", st, errCode(b))
	st, b = create(newClient(), leaders[1].email, "Again", "Second Team")
	again := decode[struct{ Account struct{ ID string } }](b)
	check("someone already in a team cannot make a second team", st != 200 || again.Account.ID == leaders[1].id, "%d %s", st, errCode(b))
	s0 := getStandings()
	check("the organisers see exactly the teams that registered", len(s0.Teams) == *nTeams, "%d teams", len(s0.Teams))

	// ================= live connections =================
	fmt.Printf("\n== %d live connections ==\n", len(everyone))
	wsURL := "ws" + strings.TrimPrefix(*base, "http") + "/ws"
	var dropped, dialErr atomic.Int64
	stop := make(chan struct{})
	parallel(len(everyone), 48, func(i int) {
		p := everyone[i]
		c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			dialErr.Add(1)
			return
		}
		_ = c.WriteJSON(map[string]any{"t": "auth", "d": map[string]any{"token": p.token}})
		go func() {
			t := time.NewTicker(15 * time.Second)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					if c.WriteJSON(map[string]any{"t": "ping"}) != nil {
						return
					}
				}
			}
		}()
		go func() {
			defer c.Close()
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					select {
					case <-stop:
					default:
						dropped.Add(1)
					}
					return
				}
				var f struct {
					T string `json:"t"`
				}
				_ = json.Unmarshal(data, &f)
				switch f.T {
				case "ready":
					p.ready.Store(true)
				case "controlState":
					p.control.Add(1)
				case "news":
					if strings.Contains(string(data), "Mock announcement") {
						p.announce.Add(1)
					}
				case "rules":
					p.rules.Add(1)
				case "watchlist":
					p.watchlist.Add(1)
				}
			}
		}()
	})
	waitFor(10*time.Second, func() bool {
		return countTrue(everyone, func(p *person) bool { return p.ready.Load() }) == len(everyone)
	})
	nReady := countTrue(everyone, func(p *person) bool { return p.ready.Load() })
	check("every connection is signed in and live", nReady == len(everyone) && dialErr.Load() == 0, "%d of %d ready, %d failed to connect", nReady, len(everyone), dialErr.Load())
	_, tb = call(leaders[2].c, "GET", "/api/team", leaders[2].token, nil)
	tv := decode[struct {
		LeaderOnline bool
		Members      []struct{ Online bool }
	}](tb)
	on := tv.LeaderOnline
	for _, m := range tv.Members {
		on = on && m.Online
	}
	check("the Team page shows all three teammates online", on && len(tv.Members) == *nMates, "%+v", tv)

	var syms []company
	_, sb := call(leaders[0].c, "GET", "/api/symbols", leaders[0].token, nil)
	syms = decode[[]company](sb)
	check("the companies are loaded", len(syms) >= 10, "%d companies", len(syms))
	if len(syms) == 0 {
		os.Exit(1)
	}

	st, b = call(leaders[0].c, "POST", "/api/trades", leaders[0].token, map[string]any{"clientTradeId": "early-1", "symbol": syms[0].Symbol, "side": "buy", "qty": 1})
	check("nobody can trade before the event starts", st != 200, "%d %s", st, errCode(b))

	// ================= Phase 1 =================
	steps := 0
	next := func(want string) {
		before := make([]int64, len(everyone))
		for i, p := range everyone {
			before[i] = p.control.Load()
		}
		st, b := admin("POST", "clock/next", map[string]any{})
		steps++
		check("Next step: "+want, st == 200, "%d %s", st, errCode(b))
		ok := waitFor(10*time.Second, func() bool {
			for i, p := range everyone {
				if p.control.Load() <= before[i] {
					return false
				}
			}
			return true
		})
		missing := 0
		for i, p := range everyone {
			if p.control.Load() <= before[i] {
				missing++
			}
		}
		check("  every screen heard about it at once", ok, "%d of %d did not", missing, len(everyone))
	}

	fmt.Println("\n== Phase 1: trading ==")
	next("Phase 1 trading")
	big := syms[5]
	bigQty := int64(300000 / math.Max(big.Price, 1))
	st, b = call(leaders[4].c, "POST", "/api/trades", leaders[4].token, map[string]any{"clientTradeId": "big-1", "symbol": big.Symbol, "side": "buy", "qty": bigQty})
	check("a buy over 25% of the portfolio in one company is refused", errCode(b) == "too_concentrated", "%d %s (qty %d)", st, errCode(b), bigQty)
	st, b = call(leaders[4].c, "POST", "/api/trades", leaders[4].token, map[string]any{"clientTradeId": "short-1", "symbol": syms[9].Symbol, "side": "sell", "qty": 1000000})
	check("selling shares you do not own is refused", errCode(b) == "insufficient_shares", "%d %s", st, errCode(b))

	time.Sleep(61 * time.Second) // those two count toward the trade limit
	phase1Trades := tradeRound(leaders, syms, "p1")
	check("every leader's trades in Phase 1 went through", phase1Trades.ok == 2*len(leaders) && phase1Trades.other == 0, "%s", phase1Trades)
	if *p1Minutes > 0 {
		// A fuller Phase 1 for a demo: the market runs longer and every leader keeps trading larger amounts in
		// different companies, so prices move, the early news goes out and the standings spread out.
		end := time.Now().Add(time.Duration(*p1Minutes) * time.Minute)
		for round := 0; time.Now().Before(end); round++ {
			waitRate()
			t := tradeRoundSized(leaders, syms, fmt.Sprintf("p1x%d", round), round)
			fmt.Printf("   Phase 1 trading, round %d: %s\n", round+1, t)
		}
	}
	var refused, leaked atomic.Int64
	parallel(len(everyone), 64, func(i int) {
		p := everyone[i]
		if p.leader {
			return
		}
		st, _ := call(p.c, "POST", "/api/trades", p.token, map[string]any{"clientTradeId": "m-" + p.email[:8] + fmt.Sprint(i), "symbol": syms[0].Symbol, "side": "buy", "qty": 1})
		if st == 403 {
			refused.Add(1)
		} else {
			leaked.Add(1)
		}
	})
	check("teammates can watch but cannot trade", leaked.Load() == 0, "%d refused, %d got through", refused.Load(), leaked.Load())
	var reads atomic.Int64
	parallel(len(everyone), 64, func(i int) {
		p := everyone[i]
		for _, path := range []string{"/api/portfolio/me", "/api/leaderboard", "/api/news", "/api/team", "/api/rules", "/api/watchlist", "/api/trades/mine", "/api/funds", "/api/config", "/api/auth/me", "/api/symbols/" + syms[i%len(syms)].Symbol + "/history"} {
			if st, _ := call(p.c, "GET", path, p.token, nil); st != 200 {
				reads.Add(1)
			}
		}
	})
	check("every page every player opens loads (11 pages x everyone)", reads.Load() == 0, "%d failed", reads.Load())

	// the team's watchlist and last trade reach teammates
	t1 := teams[3]
	st, _ = call(t1[0].c, "PUT", "/api/watchlist", t1[0].token, map[string]any{"symbols": []string{syms[1].Symbol, syms[2].Symbol}})
	time.Sleep(500 * time.Millisecond)
	_, wb := call(t1[1].c, "GET", "/api/watchlist", t1[1].token, nil)
	check("the leader's watchlist shows for teammates, live", st == 200 && strings.Contains(string(wb), syms[1].Symbol) && t1[1].watchlist.Load() > 0, "%s", wb)
	_, mt := call(t1[2].c, "GET", "/api/trades/mine", t1[2].token, nil)
	check("teammates see the team's trades", strings.Count(string(mt), `"symbol"`) >= 2, "")

	// the 25% rule, the kill switch, a paused company, an announcement, the rules page
	admin("POST", "control/freeze", map[string]any{"frozen": true})
	st, b = call(leaders[5].c, "POST", "/api/trades", leaders[5].token, map[string]any{"clientTradeId": "frz-1", "symbol": syms[0].Symbol, "side": "buy", "qty": 1})
	check("the kill switch stops trading", st != 200, "%d %s", st, errCode(b))
	admin("POST", "control/freeze", map[string]any{"frozen": false})
	admin("POST", "control/symbols/"+syms[7].Symbol, map[string]any{"paused": true})
	st, b = call(leaders[6].c, "POST", "/api/trades", leaders[6].token, map[string]any{"clientTradeId": "pz-1", "symbol": syms[7].Symbol, "side": "buy", "qty": 1})
	check("a paused company cannot be traded", st != 200, "%d %s", st, errCode(b))
	admin("POST", "control/symbols/"+syms[7].Symbol, map[string]any{"paused": false})

	st, _ = admin("POST", "announce", map[string]any{"text": "Mock announcement to everyone"})
	st2, _ := admin("POST", "rules", map[string]any{"text": "# Mock rules\n- Trade fairly."})
	ok := waitFor(10*time.Second, func() bool {
		return countTrue(everyone, func(p *person) bool { return p.announce.Load() > 0 && p.rules.Load() > 0 }) == len(everyone)
	})
	check("an announcement and new rules reach every screen", st == 200 && st2 == 200 && ok, "%d of %d", countTrue(everyone, func(p *person) bool { return p.announce.Load() > 0 && p.rules.Load() > 0 }), len(everyone))
	_, rb := call(leaders[7].c, "GET", "/api/rules", leaders[7].token, nil)
	check("players read the new rules", strings.Contains(string(rb), "Mock rules"), "")
	admin("POST", "rules", map[string]any{"text": ""})

	fmt.Println("\n== A break during Phase 1 ==")
	_, pb := call(leaders[0].c, "GET", "/api/symbols", leaders[0].token, nil)
	st, b = admin("POST", "clock/pause", map[string]any{})
	check("the organiser pauses the event for a break", st == 200, "%d %s", st, errCode(b))
	time.Sleep(12 * time.Second) // prices would change every 10 seconds
	_, pa := call(leaders[0].c, "GET", "/api/symbols", leaders[0].token, nil)
	check("no price moves during the break", string(pa) == string(pb), "")
	var pausedRefused atomic.Int64
	parallel(len(leaders), 64, func(i int) {
		st, b := call(leaders[i].c, "POST", "/api/trades", leaders[i].token, map[string]any{"clientTradeId": fmt.Sprintf("brk-%d", i), "symbol": syms[i%len(syms)].Symbol, "side": "buy", "qty": 1})
		if st == 423 && errCode(b) == "event_paused" {
			pausedRefused.Add(1)
		}
	})
	check("nobody can trade during the break", pausedRefused.Load() == int64(len(leaders)), "%d of %d refused", pausedRefused.Load(), len(leaders))
	st, b = admin("POST", "clock/next", map[string]any{})
	check("the event cannot move on during the break", st != 200 && errCode(b) == "event_paused", "%d %s", st, errCode(b))
	st, b = admin("POST", "clock/resume", map[string]any{})
	check("the organiser resumes", st == 200, "%d %s", st, errCode(b))
	time.Sleep(12 * time.Second)
	_, pr := call(leaders[0].c, "GET", "/api/symbols", leaders[0].token, nil)
	check("prices move again after the break", string(pr) != string(pa), "")

	fmt.Println("\n== Phase 1: closed ==")
	next("Phase 1 closed")
	st, b = call(leaders[0].c, "POST", "/api/trades", leaders[0].token, map[string]any{"clientTradeId": "closed-1", "symbol": syms[0].Symbol, "side": "buy", "qty": 1})
	check("trading is closed for everyone", st != 200, "%d %s", st, errCode(b))
	st, _ = call(leaders[0].c, "GET", "/api/leaderboard", leaders[0].token, nil)
	check("once Phase 1 closes, only the organisers see the standings", st == 403, "%d", st)
	st, b = admin("POST", "clock/next", map[string]any{})
	check("the organiser cannot open window 0 before the funds exist", st != 200 && errCode(b) == "funds_not_formed", "%d %s", st, errCode(b))
	if *stopAt1 {
		fmt.Println("\n== Stopped after Phase 1, as asked: the server is left here to carry on by hand ==")
		close(stop)
		summary(time.Since(began))
		return
	}

	before := getStandings()
	sort.Slice(before.Teams, func(i, j int) bool { return before.Teams[i].Value > before.Teams[j].Value })
	top := 0.0
	for _, t := range before.Teams[:20] {
		top += t.Value
	}
	totalBefore := 0.0
	for _, t := range before.Teams {
		totalBefore += t.Value
	}
	st, b = admin("POST", "qualification/run", map[string]any{})
	check("the organiser forms the funds from the Phase 1 result", st == 200, "%d %s", st, errCode(b))
	after := getStandings()
	aum, managers := 0.0, 0
	for _, f := range after.Funds {
		aum += f.AUM
	}
	for _, t := range after.Teams {
		if t.Role == "fund_manager" {
			managers++
		}
	}
	check("10 funds, 20 manager teams", len(after.Funds) == 10 && managers == 20, "%d funds, %d managers", len(after.Funds), managers)
	check("the funds hold exactly the top 20 teams' money (cash and shares)", math.Abs(aum-top) < 1, "funds ₹%.2f, top 20 ₹%.2f", aum, top)
	navOK := true
	for _, f := range after.Funds {
		navOK = navOK && math.Abs(f.NAV-100) < 1e-6
	}
	check("every fund starts at ₹100 a unit", navOK, "")
	totalAfter := 0.0
	for _, t := range after.Teams {
		totalAfter += t.Value
	}
	check("no money appeared or vanished when the funds formed", math.Abs(totalAfter-totalBefore) < 1, "before ₹%.2f after ₹%.2f", totalBefore, totalAfter)

	// who manages what
	role := map[string]string{}
	for _, t := range after.Teams {
		role[t.AccountID] = t.Role
	}
	var traders, otherManagers, investors []*person
	for _, l := range leaders {
		if role[l.id] != "fund_manager" {
			investors = append(investors, l)
			continue
		}
		_, fb := call(l.c, "GET", "/api/funds/mine", l.token, nil)
		if decode[struct{ CanTrade bool }](fb).CanTrade {
			traders = append(traders, l)
		} else {
			otherManagers = append(otherManagers, l)
		}
	}
	check("each fund has one trading team and one other", len(traders) == 10 && len(otherManagers) == 10, "%d trading, %d other", len(traders), len(otherManagers))

	// ================= Phase 2 =================
	fmt.Println("\n== Phase 2: allocation window 0 ==")
	next("allocation window 0")
	st, _ = call(investors[0].c, "GET", "/api/leaderboard", investors[0].token, nil)
	check("players no longer see the standings in Phase 2", st == 403 || st == 404, "%d", st)
	st, _ = call(adminClient, "GET", "/api/admin/standings", adm, nil)
	check("the organisers still do", st == 200, "%d", st)

	profile := func(p *person, fee float64) (int, []byte) {
		return call(p.c, "PUT", "/api/funds/mine/profile", p.token, map[string]any{"name": fmt.Sprintf("Mock Growth %d", p.team), "philosophy": "Long-term value in steady companies.", "risk": "Balanced", "strategy": "Buy quality, hold, rebalance each window.", "managementFeePercent": fee})
	}
	st, b = profile(traders[0], 2.5)
	check("a fee above 2% is refused", st != 200, "%d %s", st, errCode(b))
	st, b = profile(traders[0], 0.5)
	check("a fee below 1% is refused", st != 200, "%d %s", st, errCode(b))
	if len(otherManagers) > 0 {
		st, b = profile(otherManagers[0], 1.5)
		check("the fund's other team cannot change the profile", st != 200, "%d %s", st, errCode(b))
		time.Sleep(5 * time.Second) // a fund may change its profile once every 5 seconds
	}
	var profErr atomic.Int64
	parallel(len(traders), 10, func(i int) {
		if st, _ := profile(traders[i], 1+float64(i)/10); st != 200 {
			profErr.Add(1)
		}
	})
	check("every fund publishes its profile and chooses its fee (1% to 1.9%)", profErr.Load() == 0, "%d failed", profErr.Load())
	st, b = call(traders[0].c, "POST", "/api/funds/F1/allocate", traders[0].token, map[string]any{"amount": 10000})
	check("fund managers cannot invest in funds", st != 200, "%d %s", st, errCode(b))
	mate := teams[indexOf(leaders, investors[0])][1]
	st, b = call(mate.c, "POST", "/api/funds/F1/allocate", mate.token, map[string]any{"amount": 10000})
	check("a teammate who does not trade cannot invest", st == 403, "%d %s", st, errCode(b))

	alloc := allocateRound(investors, 1.2)
	check("every investor team reaches the 5% it must keep in funds, all at the same time", alloc.compliant == len(investors), "%s", alloc)

	fmt.Println("\n== Phase 2: trading ==")
	next("Phase 2 trading 1")
	st, b = call(investors[1].c, "POST", "/api/funds/F2/allocate", investors[1].token, map[string]any{"amount": 10000})
	check("no investing while the window is closed", st != 200 && errCode(b) == "window_closed", "%d %s", st, errCode(b))
	st, b = profile(traders[1], 2)
	check("a fund cannot change its fee once it has investors", st != 200, "%d %s", st, errCode(b))
	waitRate()
	r := tradeRound(append(append([]*person{}, investors...), traders...), syms, "p2a")
	check("investors and fund traders trade in Phase 2", r.ok == 2*(len(investors)+len(traders)) && r.other == 0, "%s", r)
	var fundTraded int
	for _, t := range traders {
		_, pb := call(t.c, "GET", "/api/funds/mine", t.token, nil)
		if strings.Count(string(pb), `"symbol"`) > 0 {
			fundTraded++
		}
	}
	check("fund trades land in the fund's own portfolio", fundTraded == len(traders), "%d of %d funds hold shares", fundTraded, len(traders))
	var logErr atomic.Int64
	parallel(len(investors), 32, func(i int) {
		if st, _ := call(investors[i].c, "POST", "/api/strategy-log", investors[i].token, map[string]any{"text": "Spread across three funds with different risk. Kept cash for the next window."}); st != 200 {
			logErr.Add(1)
		}
	})
	check("investors write strategy logs", logErr.Load() == 0, "%d failed", logErr.Load())

	for w := 1; w <= 3; w++ {
		fmt.Printf("\n== Phase 2: allocation window %d ==\n", w)
		next(fmt.Sprintf("allocation window %d", w))
		red := redeemRound(investors[:len(investors)/3])
		check("investors take money out of funds", red.ok > 0 && red.other == 0, "%s", red)
		a := allocateRound(investors[len(investors)/3:], 1.5)
		check("investors put more in", a.compliant == len(investors)-len(investors)/3 && a.other == 0, "%s", a)
		fmt.Println("\n== Phase 2: trading ==")
		next(fmt.Sprintf("Phase 2 trading %d", w+1))
		waitRate()
		r := tradeRound(append(append([]*person{}, investors...), traders...), syms, fmt.Sprintf("p2%d", w))
		check("trading goes through", r.ok == 2*(len(investors)+len(traders)) && r.other == 0, "%s", r)
	}
	st, b = call(investors[2].c, "POST", "/api/funds/F3/redeem", investors[2].token, map[string]any{"all": true})
	check("after the last window, fund money is locked", st != 200, "%d %s", st, errCode(b))

	fmt.Println("\n== Final close ==")
	next("final close")
	st, b = call(traders[0].c, "POST", "/api/trades", traders[0].token, map[string]any{"clientTradeId": "end-1", "symbol": syms[0].Symbol, "side": "buy", "qty": 1})
	check("no trading after the close", st != 200, "%d %s", st, errCode(b))
	st, b = admin("POST", "clock/next", map[string]any{})
	check("there is no step after the final close", st != 200, "%d %s", st, errCode(b))

	fin := getStandings()
	check("final standings: every team, the investors and the 10 funds", len(fin.Teams) == *nTeams && len(fin.Investors) == *nTeams-20 && len(fin.Funds) == 10, "%d / %d / %d", len(fin.Teams), len(fin.Investors), len(fin.Funds))
	feesOK := true
	for _, f := range fin.Funds {
		feesOK = feesOK && f.FeePercent >= 1 && f.FeePercent <= 2
	}
	check("every fund shows the fee it chose", feesOK, "")
	// every investor's own view of its fund units adds up with what the funds say they hold
	held := map[string]float64{}
	var mu sync.Mutex
	parallel(len(investors), 32, func(i int) {
		_, fb := call(investors[i].c, "GET", "/api/funds", investors[i].token, nil)
		for _, f := range decode[fundsView](fb).Funds {
			mu.Lock()
			held[f.ID] += f.MyValue
			mu.Unlock()
		}
	})
	sane := true
	for _, f := range fin.Funds {
		sane = sane && held[f.FundID] <= f.AUM+1
	}
	check("investors' fund money never exceeds what each fund holds", sane, "")
	check("no connection dropped during the whole event", dropped.Load() == 0, "%d dropped", dropped.Load())
	_, sysb := admin("GET", "systems", nil)
	sys := decode[struct {
		Connected     int
		JournalErrors int64
		CommitP99Ms   int64
	}](sysb)
	check("the server saw every connection and never failed to save", sys.Connected >= len(everyone) && sys.JournalErrors == 0, "connected %d, failed saves %d, save p99 %dms", sys.Connected, sys.JournalErrors, sys.CommitP99Ms)

	if *saveTo != "" {
		raw, _ := json.Marshal(fin)
		_ = os.WriteFile(*saveTo, raw, 0o644)
	}
	close(stop)
	summary(time.Since(began))
}

// ---- rounds ----

type tally struct {
	ok, rate, other int
	errs            map[string]int
	compliant       int
}

func (t tally) String() string {
	return fmt.Sprintf("ok %d, rate-limited %d, other %d %v, compliant %d", t.ok, t.rate, t.other, t.errs, t.compliant)
}

func (t *tally) add(mu *sync.Mutex, st int, b []byte) {
	mu.Lock()
	defer mu.Unlock()
	if t.errs == nil {
		t.errs = map[string]int{}
	}
	switch {
	case st == 200:
		t.ok++
	case st == 429:
		t.rate++
	default:
		t.other++
		t.errs[fmt.Sprintf("%d %s", st, errCode(b))]++
	}
}

var lastTrade time.Time

// waitRate waits out the trade limit (2 a minute) since the last round.
func waitRate() {
	if d := time.Until(lastTrade.Add(62 * time.Second)); d > 0 {
		fmt.Printf("   (waiting %s for the one-minute trade limit)\n", d.Round(time.Second))
		time.Sleep(d)
	}
}

// tradeRound: everyone given places two trades at the same moment, a buy of a small amount of one company and a
// buy of another.
// tradeRoundSized: every leader buys two companies of its own choosing for ₹20,000 to ₹1,20,000 each (well inside
// the 25% limit), sometimes selling one of them back later, so teams end up with different portfolios.
func tradeRoundSized(ps []*person, syms []company, tag string, round int) tally {
	var t tally
	var mu sync.Mutex
	parallel(len(ps), 64, func(i int) {
		p := ps[i]
		r := rand.New(rand.NewSource(int64(i*1000 + round)))
		for k := 0; k < 2; k++ {
			s := syms[r.Intn(len(syms))]
			side := "buy"
			if round > 1 && r.Intn(4) == 0 {
				side = "sell"
				// sell part of something bought earlier
				_, pb := call(p.c, "GET", "/api/portfolio/me", p.token, nil)
				if h := decode[portfolio](pb).Holdings; len(h) > 0 {
					pick := h[r.Intn(len(h))]
					st, b := call(p.c, "POST", "/api/trades", p.token, map[string]any{"clientTradeId": fmt.Sprintf("%s-%d-%d", tag, i, k), "symbol": pick.Symbol, "side": "sell", "qty": max(1, pick.Qty/2)})
					t.add(&mu, st, b)
					continue
				}
				side = "buy"
			}
			rupees := 20000 + r.Float64()*100000
			qty := int64(math.Max(1, math.Floor(rupees/math.Max(s.Price, 1))))
			st, b := call(p.c, "POST", "/api/trades", p.token, map[string]any{"clientTradeId": fmt.Sprintf("%s-%d-%d", tag, i, k), "symbol": s.Symbol, "side": side, "qty": qty})
			t.add(&mu, st, b)
		}
	})
	lastTrade = time.Now()
	return t
}

func tradeRound(ps []*person, syms []company, tag string) tally {
	var t tally
	var mu sync.Mutex
	var g sync.WaitGroup
	g.Add(1)
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func(i int, p *person) {
			defer wg.Done()
			g.Wait()
			for k := 0; k < 2; k++ {
				s := syms[(i*7+k*13)%len(syms)]
				qty := int64(math.Max(1, math.Floor(5000/math.Max(s.Price, 1))))
				st, b := call(p.c, "POST", "/api/trades", p.token, map[string]any{"clientTradeId": fmt.Sprintf("%s-%d-%d", tag, i, k), "symbol": s.Symbol, "side": "buy", "qty": qty})
				t.add(&mu, st, b)
			}
		}(i, p)
	}
	g.Done()
	wg.Wait()
	lastTrade = time.Now()
	return t
}

// allocateRound: each investor tops its fund money up to `factor` times the mandatory share, spreading it over
// funds with room, and choosing another fund when one is full, as a person would.
func allocateRound(ps []*person, factor float64) tally {
	var t tally
	var mu sync.Mutex
	var g sync.WaitGroup
	g.Add(1)
	var wg sync.WaitGroup
	for i, p := range ps {
		wg.Add(1)
		go func(i int, p *person) {
			defer wg.Done()
			g.Wait()
			for attempt := 0; attempt < 40; attempt++ {
				_, fb := call(p.c, "GET", "/api/funds", p.token, nil)
				v := decode[fundsView](fb)
				_, pb := call(p.c, "GET", "/api/portfolio/me", p.token, nil)
				pf := decode[portfolio](pb)
				need := pf.Total*v.MandatoryPct/100*factor - v.MyValueInFunds
				if need <= 0 {
					break
				}
				min := math.Min(v.MinAbsolute, pf.Total*v.MinWalletPct/100)
				amt := math.Ceil(math.Max(need, min))
				// any fund with room, as a person choosing another fund when one is full would
				var open []fundInfo
				for _, f := range v.Funds {
					if f.Room >= min {
						open = append(open, f)
					}
				}
				if len(open) == 0 {
					// every fund is full for now: wait for the others, as the page asks
					time.Sleep(300 * time.Millisecond)
					continue
				}
				f := open[rand.Intn(len(open))]
				amt = math.Min(amt, math.Floor(f.Room))
				st, b := call(p.c, "POST", "/api/funds/"+f.ID+"/allocate", p.token, map[string]any{"amount": amt})
				t.add(&mu, st, b)
				if st != 200 && errCode(b) != "fund_at_cap" {
					break
				}
				if st != 200 {
					time.Sleep(time.Duration(50+rand.Intn(200)) * time.Millisecond)
				}
			}
			_, fb := call(p.c, "GET", "/api/funds", p.token, nil)
			if decode[fundsView](fb).Compliant {
				mu.Lock()
				t.compliant++
				mu.Unlock()
			}
		}(i, p)
	}
	g.Done()
	wg.Wait()
	return t
}

// redeemRound: each takes a third of one of its fund holdings out.
func redeemRound(ps []*person) tally {
	var t tally
	var mu sync.Mutex
	parallel(len(ps), 64, func(i int) {
		p := ps[i]
		_, fb := call(p.c, "GET", "/api/funds", p.token, nil)
		v := decode[fundsView](fb)
		_, pb := call(p.c, "GET", "/api/portfolio/me", p.token, nil)
		spare := v.MyValueInFunds - decode[portfolio](pb).Total*v.MandatoryPct/100*1.02
		for _, f := range v.Funds {
			if amt := math.Floor(math.Min(spare, f.MyValue/2)); amt > 1000 {
				st, b := call(p.c, "POST", "/api/funds/"+f.ID+"/redeem", p.token, map[string]any{"amount": amt})
				t.add(&mu, st, b)
				return
			}
		}
	})
	return t
}

// ---- after a restart ----

func compare() {
	raw, err := os.ReadFile(*compareTo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	was := decode[standings](raw)
	now := getStandings()
	same := len(was.Teams) == len(now.Teams) && len(was.Funds) == len(now.Funds)
	byID := map[string]standing{}
	for _, t := range now.Teams {
		byID[t.AccountID] = t
	}
	diff := 0
	for _, t := range was.Teams {
		n := byID[t.AccountID]
		if math.Abs(n.Value-t.Value) > 0.005 || n.Trades != t.Trades || n.Role != t.Role {
			diff++
		}
	}
	fundDiff := 0
	for i := range was.Funds {
		if i < len(now.Funds) && (math.Abs(was.Funds[i].AUM-now.Funds[i].AUM) > 0.005 || was.Funds[i].NAV != now.Funds[i].NAV || was.Funds[i].FeePercent != now.Funds[i].FeePercent) {
			fundDiff++
		}
	}
	check("after the restart every team and fund is exactly as it was", same && diff == 0 && fundDiff == 0, "%d teams differ, %d funds differ", diff, fundDiff)
	_, ob := admin("GET", "overview", nil)
	ov := decode[struct {
		Clock struct {
			Status     string `json:"status"`
			BlockIndex int    `json:"blockIndex"`
		} `json:"clock"`
	}](ob)
	check("after the restart the event is still on the final step", ov.Clock.BlockIndex == 10, "%s, step %d", ov.Clock.Status, ov.Clock.BlockIndex+1)
	summary(0)
}

// ---- helpers ----

func summary(d time.Duration) {
	fmt.Println("\n================ SUMMARY ================")
	fmt.Printf("%d checks, %d failed", len(checks), failed)
	if d > 0 {
		fmt.Printf(", %s", d.Round(time.Second))
	}
	fmt.Println()
	for _, c := range checks {
		if strings.HasPrefix(c, "FAIL") {
			fmt.Println(c)
		}
	}
	if failed > 0 {
		os.Exit(1)
	}
}

func waitFor(d time.Duration, f func() bool) bool {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if f() {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return f()
}

func countTrue(ps []*person, f func(*person) bool) int {
	n := 0
	for _, p := range ps {
		if f(p) {
			n++
		}
	}
	return n
}

func indexOf(ps []*person, p *person) int {
	for i, q := range ps {
		if q == p {
			return i
		}
	}
	return 0
}

// googleChecks: the sign-in page offers only Google, and expired, forged or altered Google passes are refused.
func googleChecks() {
	stat := decode[map[string]any](func() []byte { _, b := call(adminClient, "GET", "/api/status", "", nil); return b }())
	check("the sign-in page offers Google, and only Google can add people", stat["googleEnabled"] == true && stat["googleOnlySignup"] == true, "%v", stat)
	st, b := call(newClient(), "POST", "/api/auth/signup", "", map[string]any{"displayName": "Sneaky", "email": "sneaky@" + *domain, "password": "password-123", "yourName": "Sneaky"})
	check("registering with a password is refused", st != 200, "%d %s", st, errCode(b))

	// hostile passes
	st, _ = call(newClient(), "POST", "/api/auth/onboard", "", map[string]any{"token": pass("x@"+*domain, "X", time.Now().Add(-time.Minute)), "action": "create", "teamName": "Late"})
	check("an expired Google pass is refused", st == 401, "%d", st)
	forged := pass("x@"+*domain, "X", time.Now().Add(time.Hour))
	forged = forged[:len(forged)-3] + "AAA"
	st, _ = call(newClient(), "POST", "/api/auth/onboard", "", map[string]any{"token": forged, "action": "create", "teamName": "Forged"})
	check("a forged Google pass is refused", st == 401, "%d", st)
	payload, _ := json.Marshal(map[string]any{"e": "evil@" + *domain, "n": "Evil", "x": time.Now().Add(time.Hour).Unix()})
	realSig := strings.SplitN(pass("x@"+*domain, "X", time.Now().Add(time.Hour)), ".", 2)[1]
	st, _ = call(newClient(), "POST", "/api/auth/onboard", "", map[string]any{"token": b64(payload) + "." + realSig, "action": "create", "teamName": "Swapped"})
	check("a valid signature on a different person's details is refused", st == 401, "%d", st)
}
