package httpapi_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"sync"
	"testing"
	"time"

	"stockastic/api/internal/store"
)

// The busiest moment of Phase 2 at full size: about 350 teams, the top 20 formed into funds, and the other 330
// all putting money in the second Window 0 opens, competing for the same equal-cap fund limits. A team whose fund
// is full tries the next one, as a person would. Nothing may fail with a server error, every rupee must be
// accounted for (cash out = units in = fund AUM), and a restart must bring back exactly the same funds.
func TestWindowZeroRushAtFullSize(t *testing.T) {
	if testing.Short() {
		t.Skip("slow: 350 sign-ups")
	}
	wal := store.NewMem()
	r := rb(t, 100)
	e := newEnv(t, wal, r)
	adm := e.admin()

	const teamsN, fundsN, perTeam = 350, 10, 60_000.0
	var teams []team
	for i := 0; i < teamsN; i++ {
		tok, id := e.signup(fmt.Sprintf("Rush%03d", i))
		teams = append(teams, team{fmt.Sprintf("Rush%03d", i), tok, id})
	}
	// Wallets differ a little, as they will after Phase 1, so no fund limit is a round number.
	adj := map[string]float64{}
	for i, tm := range teams[2*fundsN:] {
		a := float64(i*37%997 + 1)
		adj[tm.token] = a
		if res := e.call("POST", "/api/admin/accounts/"+tm.id+"/cash", adm, map[string]any{"amount": a}); res.Status != 200 {
			t.Fatalf("cash adjust: %d %s", res.Status, res.Raw)
		}
	}
	e.openMarket(adm)
	e.jump(adm, "p1_freeze")
	var pairs [][]string
	for i := 0; i < 2*fundsN; i += 2 {
		pairs = append(pairs, []string{teams[i].id, teams[i+1].id})
	}
	if res := e.call("POST", "/api/admin/qualification/run", adm, map[string]any{"pairs": pairs}); res.Status != 200 {
		t.Fatalf("form funds: %d %s", res.Status, res.Raw)
	}
	e.jump(adm, "transition") // Window 0 opens
	investors := teams[2*fundsN:]

	// A browser keeps its connection open between requests; so does this client. (Hundreds of brand-new
	// connections in one instant are refused by the Windows test machine's small connection queue, which says
	// nothing about the server.)
	clients := map[string]*http.Client{}
	for _, tm := range teams {
		clients[tm.token] = &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 2}}
	}
	do := func(method, tok, path string, body any) (int, map[string]any) {
		client := clients[tok]
		var rd *bytes.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		} else {
			rd = bytes.NewReader(nil)
		}
		req, _ := http.NewRequest(method, e.srv.URL+path, rd)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := client.Do(req)
		if err != nil {
			return 0, map[string]any{"error": err.Error()}
		}
		defer res.Body.Close()
		var m map[string]any
		_ = json.NewDecoder(res.Body).Decode(&m)
		return res.StatusCode, m
	}
	num := func(v any) float64 { f, _ := v.(float64); return f }
	for _, tm := range investors { // open each team's connection before the rush, as their pages would have
		do("GET", tm.token, "/api/auth/me", nil)
	}

	// Each team wants 6% of its wallet in funds (the rule is at least 5%). Like a person, it looks at how much
	// room each fund has left, puts in what fits, and spreads the rest over other funds.
	accepted := make([]float64, len(investors))
	var mu sync.Mutex
	outcomes := map[string]int{}
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i, tm := range investors {
		wg.Add(1)
		go func(i int, tm team) {
			defer wg.Done()
			<-start
			remaining := perTeam
			for attempt := 0; attempt < 300 && remaining >= 5000; attempt++ {
				code, view := do("GET", tm.token, "/api/funds", nil)
				if code != 200 {
					t.Errorf("team %s could not read the funds: %d", tm.name, code)
					return
				}
				best, room := "", 0.0
				for _, f := range view["funds"].([]any) {
					fm := f.(map[string]any)
					if r := num(fm["room"]); r > room {
						best, room = fmt.Sprint(fm["id"]), r
					}
				}
				amount := math.Floor(math.Min(remaining, room)/1000) * 1000 // people type round numbers
				if amount < 5000 {
					time.Sleep(20 * time.Millisecond) // every fund is full for now; the level rises as they fill
					continue
				}
				code, body := do("POST", tm.token, "/api/funds/"+best+"/allocate", map[string]any{"amount": amount})
				key := fmt.Sprint(code)
				if body != nil && body["error"] != nil {
					key += " " + fmt.Sprint(body["error"])
				}
				mu.Lock()
				outcomes[key]++
				mu.Unlock()
				switch {
				case code == 0 || code >= 500:
					t.Errorf("team %s got %d on %s: %v", tm.name, code, best, body)
					return
				case code == 200:
					accepted[i] += amount
					remaining -= amount
				case body["error"] != "fund_at_cap":
					t.Errorf("team %s refused: %v", tm.name, body)
					return
				}
			}
		}(i, tm)
	}
	close(start)
	wg.Wait()
	t.Logf("outcomes: %v", outcomes)

	var total float64
	placed := 0
	for i, tm := range investors {
		pf := e.call("GET", "/api/portfolio/me", tm.token, nil).Body
		var units float64
		for _, p := range pf["fundPositions"].([]any) {
			units += num(p.(map[string]any)["units"])
		}
		start := num(r.Accounts.StartingCapital) + adj[tm.token]
		if gone := start - num(pf["cashBalance"]); math.Abs(gone-accepted[i]) > 0.01 || math.Abs(units*100-accepted[i]) > 0.01 {
			t.Fatalf("team %s: accepted %.2f, cash went down by %.2f, holds %.4f units", tm.name, accepted[i], gone, units)
		}
		total += accepted[i]
		if accepted[i] > 0 {
			placed++
		}
	}
	var funds []map[string]any
	_ = json.Unmarshal(e.call("GET", "/api/funds", investors[0].token, nil).Raw, &funds)
	if len(funds) == 0 {
		var v map[string]any
		_ = json.Unmarshal(e.call("GET", "/api/funds", investors[0].token, nil).Raw, &v)
		for _, f := range v["funds"].([]any) {
			funds = append(funds, f.(map[string]any))
		}
	}
	var aum float64
	for _, f := range funds {
		aum += num(f["aum"])
	}
	// The funds hold what investors put in plus what the 20 qualifying teams brought (their starting capital here).
	brought := float64(2*fundsN) * num(r.Accounts.StartingCapital)
	if math.Abs(aum-total-brought) > 1 {
		t.Fatalf("funds hold %.2f in all; investors put in %.2f and the fund managers brought %.2f", aum, total, brought)
	}
	for i, tm := range investors {
		if need := 0.05 * (num(r.Accounts.StartingCapital) + adj[tm.token]); accepted[i] < need { // the mandatory 5%
			t.Fatalf("team %s could only place %.0f in funds; every team must be able to meet the 5%% rule", tm.name, accepted[i])
		}
	}
	t.Logf("%d of %d teams invested; %.0f rupees in, %.0f in the funds", placed, len(investors), total, aum)

	before := string(e.call("GET", "/api/admin/funds", adm, nil).Raw)
	e2 := e.restart(r)
	if after := string(e2.call("GET", "/api/admin/funds", e2.admin(), nil).Raw); after != before {
		t.Fatalf("funds after a restart differ from before it")
	}
}
