// Command loadsim drives a running server the way an event would: many teams sign up, all log in at
// once, each opens a live WebSocket, trades at a steady pace, and then every team fires an order at
// the same company in the same instant. It prints latency percentiles and error counts.
//
//	go run ./cmd/loadsim -url http://127.0.0.1:8099 -admin-email a@b.c -admin-password ... -users 300
//
// It creates accounts and orders on the server it is pointed at: use a throwaway data directory.
package main

import (
	"bytes"
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
	c := append([]time.Duration(nil), s.d...)
	sort.Slice(c, func(i, j int) bool { return c[i] < c[j] })
	at := func(q float64) time.Duration { return c[min(len(c)-1, int(q*float64(len(c))))] }
	return fmt.Sprintf("%-34s n=%-6d p50=%-8s p95=%-8s p99=%-8s max=%s", name, len(c), at(.5).Round(time.Microsecond), at(.95).Round(time.Microsecond), at(.99).Round(time.Microsecond), c[len(c)-1].Round(time.Microsecond))
}

type user struct {
	idx   int
	email string
	id    string
	token string
	c     *http.Client // one connection per team, like a browser
}

var (
	base      = flag.String("url", "http://127.0.0.1:8099", "server URL")
	adminMail = flag.String("admin-email", "", "organiser email")
	adminPass = flag.String("admin-password", "", "organiser password")
	nUsers    = flag.Int("users", 300, "teams to simulate")
	perMin    = flag.Float64("orders-per-min", 2, "orders each team sends per minute (the rulebook allows 2)")
	steady    = flag.Duration("steady", 60*time.Second, "how long to trade at the steady pace")
	subs      = flag.Int("subs", 1, "companies each browser has open (order book subscriptions)")
	storm     = flag.Int("storm", 200, "most teams logging in at the very same instant (a venue rarely exceeds this)")

	client = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConns: 2000, MaxIdleConnsPerHost: 2000, IdleConnTimeout: 90 * time.Second}}

	statSignup, statLogin, statOrder, statBurst, statWSLag, statDepth, statPortfolio stats
	orderOK, orderRate, orderErr, wsMsgs, wsBytes, wsDropped, wsConnected            atomic.Int64
	sent                                                                             sync.Map // clientOrderId -> start time
	statusMu                                                                         sync.Mutex
	statuses                                                                         = map[string]int{}
)

func call(method, path, token string, body any) (int, []byte, error) {
	return callC(client, method, path, token, body)
}

func callC(c *http.Client, method, path, token string, body any) (int, []byte, error) {
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
		return 0, nil, err
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b, nil
}

func must(status int, body []byte, err error) []byte {
	if err != nil || status != 200 {
		fmt.Fprintf(os.Stderr, "request failed: status %d err %v body %s\n", status, err, body)
		os.Exit(1)
	}
	return body
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

func main() {
	flag.Parse()
	if *adminMail == "" || *adminPass == "" {
		fmt.Fprintln(os.Stderr, "need -admin-email and -admin-password")
		os.Exit(2)
	}
	start := time.Now()

	// ---- organiser ----
	var login struct{ Token string }
	_ = json.Unmarshal(must(call("POST", "/api/auth/login", "", map[string]any{"email": *adminMail, "password": *adminPass})), &login)
	adm := login.Token
	adminDo := func(path string, body map[string]any) {
		body["reason"] = "load simulation"
		must(call("POST", "/api/admin/"+path, adm, body))
	}

	// ---- 1. sign up every team ----
	fmt.Printf("signing up %d teams ...\n", *nUsers)
	users := make([]*user, *nUsers)
	parallel(*nUsers, 16, func(i int) {
		u := &user{idx: i, email: fmt.Sprintf("load%d@sim.test", i), c: &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{MaxIdleConnsPerHost: 2, IdleConnTimeout: 5 * time.Minute}}}
		t0 := time.Now()
		var out struct {
			Token   string
			Account struct{ ID string }
		}
		st, b, err := callC(u.c, "POST", "/api/auth/signup", "", map[string]any{"displayName": fmt.Sprintf("Team %d", i), "email": u.email, "password": "sim-password-1"})
		if err != nil || st != 200 {
			fmt.Fprintf(os.Stderr, "signup %d: %d %v %s\n", i, st, err, b)
			os.Exit(1)
		}
		statSignup.add(time.Since(t0))
		_ = json.Unmarshal(b, &out)
		u.token, u.id = out.Token, out.Account.ID
		users[i] = u
	})

	// ---- 2. everyone logs in at the same moment (the start-of-event storm) ----
	fmt.Printf("all %d teams log in at once ...\n", *nUsers)
	var gate sync.WaitGroup
	gate.Add(1)
	var wg sync.WaitGroup
	sem := make(chan struct{}, *storm)
	for _, u := range users {
		wg.Add(1)
		go func(u *user) {
			defer wg.Done()
			gate.Wait()
			sem <- struct{}{}
			defer func() { <-sem }()
			t0 := time.Now()
			st, b, err := callC(u.c, "POST", "/api/auth/login", "", map[string]any{"email": u.email, "password": "sim-password-1"})
			if err != nil || st != 200 {
				orderErr.Add(1)
				return
			}
			statLogin.add(time.Since(t0))
			var out struct{ Token string }
			_ = json.Unmarshal(b, &out)
			u.token = out.Token
		}(u)
	}
	gate.Done()
	wg.Wait()

	// ---- 3. open the market and give every team stock ----
	adminDo("clock/start", map[string]any{})
	adminDo("clock/jump", map[string]any{"blockId": "p1_trading"})
	var syms []struct {
		Symbol    string
		OpenPrice float64
	}
	_ = json.Unmarshal(must(call("GET", "/api/symbols", adm, nil)), &syms)
	for _, s := range syms {
		adminDo("grants", map[string]any{"accountId": "*", "symbol": s.Symbol, "qty": 5000, "price": s.OpenPrice})
	}
	adminDo("accounts/"+users[0].id+"/cash", map[string]any{"amount": 1}) // exercise an organiser write too

	// ---- 4. every team opens a live socket and looks at a company ----
	wsURL := "ws" + strings.TrimPrefix(*base, "http") + "/ws"
	fmt.Printf("opening %d WebSockets ...\n", *nUsers)
	stop := make(chan struct{})
	var wsWG sync.WaitGroup
	var readyMu sync.Mutex
	var statReady stats
	parallel(*nUsers, 32, func(i int) {
		u := users[i]
		t0 := time.Now()
		c, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
		if err != nil {
			orderErr.Add(1)
			return
		}
		_ = c.WriteJSON(map[string]any{"t": "auth", "d": map[string]any{"token": u.token}})
		for k := 0; k < *subs; k++ {
			_ = c.WriteJSON(map[string]any{"t": "subscribe:symbol", "d": syms[rand.Intn(len(syms))].Symbol})
		}
		readyMu.Lock()
		wsConnected.Add(1)
		readyMu.Unlock()
		wsWG.Add(1)
		go func() { // the real web client sends a ping every 15 seconds
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
			defer wsWG.Done()
			defer c.Close()
			first := true
			for {
				_, data, err := c.ReadMessage()
				if err != nil {
					select {
					case <-stop:
					default:
						wsDropped.Add(1)
					}
					return
				}
				now := time.Now()
				wsMsgs.Add(1)
				wsBytes.Add(int64(len(data)))
				var f struct {
					T string `json:"t"`
					D struct {
						ClientOrderID string `json:"clientOrderId"`
					} `json:"d"`
				}
				if json.Unmarshal(data, &f) != nil {
					continue
				}
				if f.T == "ready" && first {
					first = false
					statReady.add(now.Sub(t0))
				}
				if f.T == "orderAccepted" && f.D.ClientOrderID != "" {
					if v, ok := sent.LoadAndDelete(f.D.ClientOrderID); ok {
						statWSLag.add(now.Sub(v.(time.Time)))
					}
				}
			}
		}()
	})

	place := func(u *user, n int, sym string, open float64, into *stats) {
		side := "buy"
		if rand.Intn(2) == 0 {
			side = "sell"
		}
		price := math.Round(open*(1+rand.NormFloat64()*0.004)*20) / 20
		cid := fmt.Sprintf("u%d-%d-%d", u.idx, n, time.Now().UnixNano())
		t0 := time.Now()
		sent.Store(cid, t0)
		st, body, err := callC(u.c, "POST", "/api/orders", u.token, map[string]any{"clientOrderId": cid, "symbol": sym, "side": side, "price": price, "qty": 1 + rand.Intn(10)})
		d := time.Since(t0)
		if err != nil || st != 200 {
			var e struct {
				Error string `json:"error"`
			}
			_ = json.Unmarshal(body, &e)
			key := fmt.Sprintf("%d %s", st, e.Error)
			if err != nil {
				key = "network: " + err.Error()
			}
			statusMu.Lock()
			statuses[key]++
			statusMu.Unlock()
		}
		switch {
		case err != nil:
			orderErr.Add(1)
			sent.Delete(cid)
		case st == 200:
			orderOK.Add(1)
			into.add(d)
		case st == 429:
			orderRate.Add(1)
			sent.Delete(cid)
		default:
			orderErr.Add(1)
			sent.Delete(cid)
		}
	}

	// ---- 5. steady trading ----
	fmt.Printf("trading steadily for %s (%.1f orders per team per minute) ...\n", *steady, *perMin)
	deadline := time.Now().Add(*steady)
	var tw sync.WaitGroup
	interval := time.Duration(float64(time.Minute) / *perMin)
	for _, u := range users {
		tw.Add(1)
		go func(u *user) {
			defer tw.Done()
			time.Sleep(time.Duration(rand.Int63n(int64(interval)))) // teams do not all act on the same tick
			n := 0
			for time.Now().Before(deadline) {
				s := syms[rand.Intn(len(syms))]
				place(u, n, s.Symbol, s.OpenPrice, &statOrder)
				n++
				// a team also looks at its book and portfolio now and then
				if true {
					t0 := time.Now()
					callC(u.c, "GET", "/api/symbols/"+s.Symbol+"/depth", u.token, nil)
					statDepth.add(time.Since(t0))
					t0 = time.Now()
					callC(u.c, "GET", "/api/portfolio/me", u.token, nil)
					statPortfolio.add(time.Since(t0))
				}
				time.Sleep(interval)
			}
		}(u)
	}
	tw.Wait()

	// ---- 6. the worst moment: every team hits the same company in the same instant ----
	time.Sleep(2 * time.Second)
	fmt.Printf("burst: all %d teams order the same company at the same instant ...\n", *nUsers)
	hot := syms[0]
	var bw sync.WaitGroup
	var g2 sync.WaitGroup
	g2.Add(1)
	for _, u := range users {
		bw.Add(1)
		go func(u *user) {
			defer bw.Done()
			g2.Wait()
			place(u, 1_000_000, hot.Symbol, hot.OpenPrice, &statBurst)
		}(u)
	}
	g2.Done()
	bw.Wait()
	time.Sleep(3 * time.Second)

	// ---- report ----
	var sys struct {
		Connected                  int
		OpenOrders                 int
		CommitP50Ms, CommitP99Ms   int64
		JournalErrors              int64
		OrdersPerMin, TradesPerMin int64
		Halted                     []any
	}
	_ = json.Unmarshal(must(call("GET", "/api/admin/systems", adm, nil)), &sys)
	secs := time.Since(start).Seconds()

	fmt.Println()
	fmt.Println("================ RESULTS ================")
	fmt.Printf("teams %d, steady pace %.1f orders/team/min, total run %.0fs\n\n", *nUsers, *perMin, secs)
	fmt.Println(statSignup.line("sign up (bcrypt)"))
	fmt.Println(statLogin.line("log in, all at once (bcrypt)"))
	fmt.Println(statReady.line("WebSocket connect to ready"))
	fmt.Println(statOrder.line("place order, steady"))
	fmt.Println(statBurst.line("place order, burst on one company"))
	fmt.Println(statWSLag.line("order to live update on own socket"))
	fmt.Println(statDepth.line("read order book"))
	fmt.Println(statPortfolio.line("read portfolio"))
	fmt.Printf("\norders accepted %d, refused by the trade limit %d, errors %d\n", orderOK.Load(), orderRate.Load(), orderErr.Load())
	statusMu.Lock()
	for k, v := range statuses {
		fmt.Printf("    not accepted: %-40s x%d\n", k, v)
	}
	statusMu.Unlock()
	fmt.Printf("sockets connected %d, dropped %d, messages received %d (%.1f per socket per second, %.1f KB/s per socket)\n",
		wsConnected.Load(), wsDropped.Load(), wsMsgs.Load(),
		float64(wsMsgs.Load())/float64(max(1, int(wsConnected.Load())))/secs, float64(wsBytes.Load())/1024/float64(max(1, int(wsConnected.Load())))/secs)
	fmt.Printf("server view: connected %d, open orders %d, save time p50 %dms p99 %dms, failed saves %d, stopped companies %d\n",
		sys.Connected, sys.OpenOrders, sys.CommitP50Ms, sys.CommitP99Ms, sys.JournalErrors, len(sys.Halted))
	close(stop)
	fmt.Println("(closing sockets)")
	os.Exit(0)
}
