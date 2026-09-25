// Command abusesim attacks a running server the way a hostile team might, while honest teams keep trading, and
// reports whether the honest teams noticed. Point it at a throwaway server (it creates accounts):
//
//	abusesim -url http://127.0.0.1:8099 -admin-email a@b.c -admin-password ...
//
// Attacks, all at once: a flood of unauthenticated requests, a wrong-password flood, a flood from one signed-in
// account, thousands of idle WebSockets, a WebSocket message flood, slow-header connections that never finish, and
// huge request bodies.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
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
	honest    = flag.Int("honest", 100, "honest teams trading during the test")
	dur       = flag.Duration("duration", 20*time.Second, "how long each phase runs")
	idleWS    = flag.Int("idle-sockets", 3000, "idle unauthenticated WebSockets to open")
	wait      = flag.Duration("wait", 62*time.Second, "pause between the baseline and the attack (a trade allowance is a minute)")
	slow      = flag.Int("slow", 400, "slow-header connections to hold open")
)

var client = &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 512, MaxConnsPerHost: 0}}

func call(method, path, token string, body any) (int, []byte, time.Duration) {
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
	t0 := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return 0, nil, time.Since(t0)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	return res.StatusCode, b, time.Since(t0)
}

type stats struct {
	mu sync.Mutex
	d  []time.Duration
}

func (s *stats) add(d time.Duration) { s.mu.Lock(); s.d = append(s.d, d); s.mu.Unlock() }
func (s *stats) line(name string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.d) == 0 {
		return fmt.Sprintf("%-34s no samples", name)
	}
	d := append([]time.Duration(nil), s.d...)
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	p := func(q float64) time.Duration { return d[int(float64(len(d)-1)*q)] }
	return fmt.Sprintf("%-34s n=%-6d p50=%-10v p95=%-10v p99=%-10v max=%v", name, len(d), p(0.5), p(0.95), p(0.99), d[len(d)-1])
}

type tally struct {
	mu sync.Mutex
	m  map[string]int
}

func (t *tally) add(k string) {
	t.mu.Lock()
	if t.m == nil {
		t.m = map[string]int{}
	}
	t.m[k]++
	t.mu.Unlock()
}
func (t *tally) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	var ks []string
	for k := range t.m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	var b strings.Builder
	for _, k := range ks {
		fmt.Fprintf(&b, "%s x%d  ", k, t.m[k])
	}
	return b.String()
}

func must(code int, b []byte, want int, what string) []byte {
	if code != want {
		fmt.Fprintf(os.Stderr, "%s: status %d %s\n", what, code, string(b))
		os.Exit(1)
	}
	return b
}

type user struct {
	token string
	id    string
}

func main() {
	flag.Parse()
	code, b, _ := call("POST", "/api/auth/login", "", map[string]any{"email": *adminMail, "password": *adminPass})
	var l struct{ Token string }
	_ = json.Unmarshal(must(code, b, 200, "admin login"), &l)
	adm := l.Token
	admin := func(p string, body any) {
		c, b, _ := call("POST", "/api/admin/"+p, adm, body)
		if c != 200 {
			fmt.Fprintf(os.Stderr, "admin %s: %d %s\n", p, c, b)
			os.Exit(1)
		}
	}

	fmt.Printf("creating %d honest teams (and one attacker account) ...\n", *honest)
	users := make([]*user, *honest+1)
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i := range users {
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			for try := 0; try < 20; try++ {
				c, b, _ := call("POST", "/api/auth/signup", "", map[string]any{"displayName": fmt.Sprintf("Team%d", i), "email": fmt.Sprintf("abuse%d-%d@sim.io", i, time.Now().UnixNano()), "password": "sim-password-1"})
				if c == 200 {
					var r struct {
						Token   string
						Account struct{ ID string }
					}
					_ = json.Unmarshal(b, &r)
					users[i] = &user{r.Token, r.Account.ID}
					return
				}
				time.Sleep(time.Second)
			}
			fmt.Fprintln(os.Stderr, "could not sign up a team")
			os.Exit(1)
		}(i)
	}
	wg.Wait()
	c, b2, _ := call("GET", "/api/symbols", users[0].token, nil)
	var syms []struct {
		Symbol    string
		LastPrice float64
	}
	_ = json.Unmarshal(must(c, b2, 200, "symbols"), &syms)
	admin("grants", map[string]any{"accountId": "*", "symbol": syms[0].Symbol, "qty": 1000, "price": syms[0].LastPrice})
	admin("clock/start", map[string]any{"blockId": ""})
	admin("clock/jump", map[string]any{"blockId": "p1_trading"})
	time.Sleep(1500 * time.Millisecond)

	honestRun := func(d time.Duration, into *stats, errs *tally, reads *stats) {
		var hw sync.WaitGroup
		deadline := time.Now().Add(d)
		for _, u := range users[:*honest] {
			hw.Add(1)
			go func(u *user) {
				defer hw.Done()
				time.Sleep(time.Duration(rand.Int63n(int64(3 * time.Second))))
				n := 0
				for time.Now().Before(deadline) {
					n++
					side := []string{"buy", "sell"}[n%2]
					code, _, dt := call("POST", "/api/trades", u.token, map[string]any{"clientTradeId": fmt.Sprintf("h%d-%d-%d", rand.Int(), n, time.Now().UnixNano()), "symbol": syms[0].Symbol, "side": side, "qty": 1})
					into.add(dt)
					errs.add(fmt.Sprint(code))
					_, _, d1 := call("GET", "/api/portfolio/me", u.token, nil)
					reads.add(d1)
					_, _, d2 := call("GET", "/api/symbols", u.token, nil)
					reads.add(d2)
					time.Sleep(time.Duration(2500+rand.Intn(1500)) * time.Millisecond)
				}
			}(u)
		}
		hw.Wait()
	}

	var base0, baseRead, atkRead stats
	var e0 tally
	fmt.Printf("baseline: honest teams alone for %v ...\n", *dur)
	honestRun(*dur, &base0, &e0, &baseRead)

	// ---- the attack ----
	fmt.Println("waiting a minute so every team's trade allowance is fresh ...")
	time.Sleep(*wait)
	fmt.Printf("attack: everything at once for %v, honest teams keep trading ...\n", *dur)
	stop := make(chan struct{})
	var aw sync.WaitGroup
	var flood, login, own, big tally
	launch := func(n int, f func()) {
		for i := 0; i < n; i++ {
			aw.Add(1)
			go func() { defer aw.Done(); f() }()
		}
	}
	loop := func(f func()) func() {
		return func() {
			for {
				select {
				case <-stop:
					return
				default:
					f()
				}
			}
		}
	}
	launch(150, loop(func() { c, _, _ := call("GET", "/api/portfolio/me", "", nil); flood.add(fmt.Sprint(c)) }))
	launch(40, loop(func() {
		c, _, _ := call("POST", "/api/auth/login", "", map[string]any{"email": fmt.Sprintf("victim%d@sim.io", rand.Intn(20)), "password": "guess-" + fmt.Sprint(rand.Int())})
		login.add(fmt.Sprint(c))
	}))
	launch(60, loop(func() { c, _, _ := call("GET", "/api/symbols", users[*honest].token, nil); own.add(fmt.Sprint(c)) }))
	launch(10, loop(func() {
		c, _, _ := call("POST", "/api/trades", users[*honest].token, map[string]any{"clientTradeId": "x", "symbol": "ACME", "side": "buy", "qty": 1, "pad": strings.Repeat("A", 300_000)})
		big.add(fmt.Sprint(c))
	}))

	wsURL := "ws" + strings.TrimPrefix(*base, "http") + "/ws"
	var idleOpen, idleRefused atomic.Int64
	var idleWhy tally
	var idleConns []*websocket.Conn
	var idleMu sync.Mutex
	launch(1, func() {
		var iw sync.WaitGroup
		s := make(chan struct{}, 200)
		for i := 0; i < *idleWS; i++ {
			iw.Add(1)
			s <- struct{}{}
			go func() {
				defer iw.Done()
				defer func() { <-s }()
				c, resp, err := websocket.DefaultDialer.Dial(wsURL, nil)
				if err != nil {
					if resp != nil {
						idleWhy.add(fmt.Sprint(resp.StatusCode))
					} else {
						idleWhy.add("dial error")
					}
					idleRefused.Add(1)
					return
				}
				idleOpen.Add(1)
				idleMu.Lock()
				idleConns = append(idleConns, c)
				idleMu.Unlock()
			}()
		}
		iw.Wait()
	})
	var floodClosed, floodTried atomic.Int64
	launch(20, func() {
		c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{"t": "auth", "d": map[string]any{"token": users[*honest].token}})
		floodTried.Add(1)
		for i := 0; i < 100000; i++ {
			if err := c.WriteJSON(map[string]any{"t": "ping"}); err != nil {
				floodClosed.Add(1)
				return
			}
		}
	})
	var slowHeld atomic.Int64
	u, _ := url.Parse(*base)
	launch(*slow, func() {
		cn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
		if err != nil {
			return
		}
		defer cn.Close()
		_, _ = cn.Write([]byte("GET /api/status HTTP/1.1\r\nHost: x\r\nX-Slow: "))
		slowHeld.Add(1)
		// Drip one byte at a time, never finishing the header. The server should cut this off by itself.
		t0 := time.Now()
		for time.Since(t0) < 40*time.Second {
			select {
			case <-stop:
				return
			case <-time.After(2 * time.Second):
			}
			if _, err := cn.Write([]byte("a")); err != nil {
				slowHeld.Add(-1)
				return
			}
		}
	})

	var atk stats
	var e1 tally
	time.Sleep(1500 * time.Millisecond)
	honestRun(*dur, &atk, &e1, &atkRead)
	close(stop)
	aw.Wait()
	for _, c := range idleConns {
		c.Close()
	}

	// ---- results ----
	fmt.Println("\n================ RESULTS ================")
	fmt.Println(baseRead.line("honest page loads, no attack"))
	fmt.Println(atkRead.line("honest page loads, UNDER ATTACK"))
	fmt.Println(base0.line("honest trade, no attack"))
	fmt.Println(atk.line("honest trade, UNDER ATTACK"))
	fmt.Printf("honest results without attack: %s\n", e0.String())
	fmt.Printf("honest results under attack:   %s\n", e1.String())
	fmt.Printf("\nunauthenticated request flood:  %s\n", flood.String())
	fmt.Printf("wrong-password flood:           %s\n", login.String())
	fmt.Printf("one account flooding:           %s\n", own.String())
	fmt.Printf("300 KB request bodies:          %s\n", big.String())
	fmt.Printf("idle sockets: %d opened, %d refused by the server (of %d tried)\n", idleOpen.Load(), idleRefused.Load(), *idleWS)
	fmt.Printf("message-flood sockets: %d of %d were cut off\n", floodClosed.Load(), floodTried.Load())
	fmt.Printf("slow-header connections still held when the test ended: %d of %d (the server should have dropped them)\n", slowHeld.Load(), *slow)

	c1, b1, _ := call("GET", "/readyz", "", nil)
	c3, _, dt := call("GET", "/api/portfolio/me", users[0].token, nil)
	fmt.Printf("\nafter the attack: /readyz %d %s, a team's portfolio %d in %v\n", c1, strings.TrimSpace(string(b1)), c3, dt)
	c4, sb, _ := call("GET", "/api/admin/systems", adm, nil)
	if c4 == 200 {
		var s struct {
			Connected, JournalErrors int
			CommitP99Ms              int
		}
		_ = json.Unmarshal(sb, &s)
		fmt.Printf("server view: connected %d, failed saves %d, save time p99 %d ms\n", s.Connected, s.JournalErrors, s.CommitP99Ms)
	}
}
