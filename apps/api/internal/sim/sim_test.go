package sim_test

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"stockastic/api/internal/market"
	"stockastic/api/internal/money"
	"stockastic/api/internal/news"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/sim"
	"stockastic/api/internal/universe"
)

type clock struct {
	mu      sync.Mutex
	elapsed time.Duration
	started bool
	open    bool
	stage   rulebook.Stage
}

func (c *clock) Elapsed() (time.Duration, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.elapsed, c.started
}
func (c *clock) MarketOpen() bool      { c.mu.Lock(); defer c.mu.Unlock(); return c.open }
func (c *clock) Stage() rulebook.Stage { return c.stage }

type item struct {
	kind     news.Kind
	headline string
}
type wire struct {
	mu    sync.Mutex
	items []item
}

func (w *wire) Publish(k news.Kind, h, _ string) news.Item {
	w.mu.Lock()
	w.items = append(w.items, item{k, h})
	w.mu.Unlock()
	return news.Item{}
}
func (w *wire) count() int { w.mu.Lock(); defer w.mu.Unlock(); return len(w.items) }

type rig struct {
	e      *sim.Engine
	prices *market.Prices
	clk    *clock
	wire   *wire
	now    time.Time
	saved  []sim.State
	lead   time.Duration
	cos    []universe.Company
}

func companies() []universe.Company {
	return []universe.Company{
		{Symbol: "A", Name: "A", Sector: "Banking", Open: 10000},
		{Symbol: "B", Name: "B", Sector: "Banking", Open: 20000},
		{Symbol: "C", Name: "C", Sector: "IT", Open: 30000},
		{Symbol: "D", Name: "D", Sector: "IT", Open: 40000},
	}
}

func newRig(t *testing.T, sc sim.Scenario, cos []universe.Company, lead time.Duration) *rig {
	t.Helper()
	r := &rig{clk: &clock{started: true, open: true, stage: rulebook.StagePhase2}, wire: &wire{}, now: time.Unix(1_000_000, 0), lead: lead, cos: cos}
	r.prices = market.New(cos, r.now)
	e, err := sim.New(sc, sim.Deps{
		Prices: r.prices, Companies: cos, Clock: r.clk, News: r.wire,
		Lead: func() time.Duration { return r.lead },
		Save: func(s sim.State) error { r.saved = append(r.saved, s); return nil },
		Log:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	r.e = e
	return r
}

// step advances the world one second.
func (r *rig) step() {
	r.now = r.now.Add(time.Second)
	r.clk.mu.Lock()
	if r.clk.started {
		r.clk.elapsed += time.Second
	}
	r.clk.mu.Unlock()
	r.e.Advance(r.now)
}
func (r *rig) run(seconds int) {
	for i := 0; i < seconds; i++ {
		r.step()
	}
}
func (r *rig) price(sym string) money.Paise { p, _ := r.prices.Price(sym); return p }

func still(tick int) sim.Scenario {
	return sim.Scenario{Seed: 7, TickSeconds: tick, Volatility: sim.Volatility{DefaultPct: 0}}
}

func near(a, b money.Paise, tol money.Paise) bool { d := a - b; return d >= -tol && d <= tol }

func TestPricesMoveOnlyWhileTheMarketIsOpen(t *testing.T) {
	sc := sim.Scenario{Seed: 3, TickSeconds: 10, Volatility: sim.Volatility{DefaultPct: 1}}
	r := newRig(t, sc, companies(), 0)
	r.clk.open = false
	r.run(300)
	for _, c := range companies() {
		if r.price(c.Symbol) != c.Open {
			t.Fatalf("%s moved to %d while the market was closed", c.Symbol, r.price(c.Symbol))
		}
	}
	if len(r.saved) != 0 {
		t.Fatalf("saved %d states while closed", len(r.saved))
	}
	r.clk.open = true
	r.run(35) // three price changes at 10s
	if got := r.saved[len(r.saved)-1].Tick; got != 3 {
		t.Fatalf("ticks = %d, want 3 after 35 open seconds", got)
	}
	moved := 0
	for _, c := range companies() {
		if r.price(c.Symbol) != c.Open {
			moved++
		}
	}
	if moved == 0 {
		t.Fatal("no price moved with volatility on")
	}
	// Closing the market again freezes them where they are.
	frozen := r.prices.All()
	r.clk.open = false
	r.run(120)
	for k, v := range r.prices.All() {
		if frozen[k] != v {
			t.Fatalf("%s moved after the market closed", k)
		}
	}
	// Nothing happens before the event has started.
	r2 := newRig(t, sc, companies(), 0)
	r2.clk.started = false
	r2.run(200)
	if len(r2.saved) != 0 {
		t.Fatal("the simulation ran before the event started")
	}
}

func TestTheSameScenarioAlwaysMakesTheSameMarket(t *testing.T) {
	sc := sim.Scenario{Seed: 11, TickSeconds: 5, Volatility: sim.Volatility{DefaultPct: 0.8}}
	a, b := newRig(t, sc, companies(), 0), newRig(t, sc, companies(), 0)
	a.run(600)
	b.run(600)
	for _, c := range companies() {
		if a.price(c.Symbol) != b.price(c.Symbol) {
			t.Fatalf("%s: %d vs %d for the same scenario", c.Symbol, a.price(c.Symbol), b.price(c.Symbol))
		}
	}
	sc.Seed = 12
	c := newRig(t, sc, companies(), 0)
	c.run(600)
	same := 0
	for _, co := range companies() {
		if c.price(co.Symbol) == a.price(co.Symbol) {
			same++
		}
	}
	if same == len(companies()) {
		t.Fatal("a different seed produced the identical market")
	}
}

func TestAnEventIsReleasedOnceAndItsPriceEffectWaitsForThePublic(t *testing.T) {
	sc := still(10)
	sc.Events = []sim.Event{{ID: "rate-cut", AtMinute: 1, Headline: "Central bank cuts rates", Impacts: []sim.Impact{{Sector: "Banking", ShockPct: -10}}}}
	r := newRig(t, sc, companies(), 60*time.Second) // Phase 2: the public sees it 60 seconds after fund managers

	r.run(59)
	if r.wire.count() != 0 {
		t.Fatal("released before its time")
	}
	r.run(2) // 61s: the event fires
	if r.wire.count() != 1 || r.wire.items[0].kind != news.KindNews || r.wire.items[0].headline != "Central bank cuts rates" {
		t.Fatalf("news = %+v", r.wire.items)
	}
	// For the next minute fund managers have the news and the price has not reacted.
	r.run(50)
	if r.price("A") != 10000 || r.price("B") != 20000 {
		t.Fatalf("the price reacted %d seconds before the public saw the news: A=%d", 50, r.price("A"))
	}
	// Once the public release moment has passed, the next price change carries the effect.
	r.run(30)
	if !near(r.price("A"), 9000, 1) || !near(r.price("B"), 18000, 1) {
		t.Fatalf("banks after the public release: A=%d B=%d, want -10%%", r.price("A"), r.price("B"))
	}
	if r.price("C") != 30000 || r.price("D") != 40000 {
		t.Fatal("a Banking event moved IT companies")
	}
	r.run(600)
	if r.wire.count() != 1 {
		t.Fatalf("the event was released %d times", r.wire.count())
	}
	if !near(r.price("A"), 9000, 1) {
		t.Fatalf("the effect was applied more than once: A=%d", r.price("A"))
	}
}

func TestInPhaseOneThePriceReactsAtOnce(t *testing.T) {
	sc := still(10)
	sc.Events = []sim.Event{{ID: "e", AtMinute: 0.5, Headline: "Good news for one company", Impacts: []sim.Impact{{Symbol: "C", ShockPct: 20}}}}
	r := newRig(t, sc, companies(), 0) // no stagger in Phase 1
	r.clk.stage = rulebook.StagePhase1
	r.run(45)
	if !near(r.price("C"), 36000, 1) {
		t.Fatalf("C = %d, want +20%% with no delay", r.price("C"))
	}
	if r.price("D") != 40000 {
		t.Fatal("a one-company event moved another company")
	}
}

func TestARampSpreadsAMoveAcrossPriceChanges(t *testing.T) {
	sc := still(60)
	sc.Events = []sim.Event{{ID: "e", AtMinute: 0, Headline: "Boom", Impacts: []sim.Impact{{Symbol: "A", ShockPct: 20, RampMinutes: 3}}}}
	r := newRig(t, sc, companies(), 0)
	var seen []money.Paise
	for i := 0; i < 6*60; i++ {
		r.step()
		if i%60 == 59 {
			seen = append(seen, r.price("A"))
		}
	}
	// Three price changes carry the move: rising each time, ending +20%, then flat.
	if !(seen[0] > 10000 && seen[1] > seen[0] && seen[2] > seen[1]) {
		t.Fatalf("the move was not spread over three changes: %v", seen)
	}
	if !near(seen[2], 12000, 3) || seen[3] != seen[2] || seen[5] != seen[2] {
		t.Fatalf("total move should end at +20%% and stop: %v", seen)
	}
}

func bigUniverse(n int) []universe.Company {
	cs := make([]universe.Company, n)
	for i := range cs {
		cs[i] = universe.Company{Symbol: fmt.Sprintf("S%03d", i), Name: "x", Sector: "Banking", Open: 10000}
	}
	return cs
}

func TestBullAndBearRunsMoveMostCompaniesButNotAll(t *testing.T) {
	for _, kind := range []string{sim.KindBull, sim.KindBear} {
		sc := still(10)
		sc.Regimes = []sim.Regime{{ID: "run", Kind: kind, AtMinute: 0, DurationMinutes: 30, DriftPctPerTick: 1, Breadth: 0.7}}
		r := newRig(t, sc, bigUniverse(200), 0)
		r.run(120)
		up, down, flat := 0, 0, 0
		for _, c := range r.cos {
			switch p := r.price(c.Symbol); {
			case p > 10000:
				up++
			case p < 10000:
				down++
			default:
				flat++
			}
		}
		lead, other := up, down
		if kind == sim.KindBear {
			lead, other = down, up
		}
		if other != 0 {
			t.Errorf("%s: %d companies moved against the run", kind, other)
		}
		if share := float64(lead) / 200; share < 0.55 || share > 0.85 {
			t.Errorf("%s: %.0f%% of companies moved with the run, want about 70%%", kind, share*100)
		}
		if flat == 0 {
			t.Errorf("%s: every company moved, but a run must not drive them all", kind)
		}
		if r.wire.count() != 1 || r.wire.items[0].kind != news.KindRegime {
			t.Errorf("%s: announcement = %+v", kind, r.wire.items)
		}
	}
}

func TestAScriptedPathIsFollowedExactly(t *testing.T) {
	sc := still(60)
	sc.Paths = map[string][]sim.PathPoint{"A": {{0, 100}, {10, 200}, {20, 100}}}
	r := newRig(t, sc, companies(), 0)
	r.run(5 * 60)
	if got := r.price("A"); !near(got, 15000, 200) { // at minute 5 the path is halfway from 100 to 200
		t.Fatalf("A at minute 5 = %d, want about 150 rupees", got)
	}
	r.run(15 * 60)
	if got := r.price("A"); got != 10000 {
		t.Fatalf("A after the path ends = %d, want it to hold the last point (100 rupees)", got)
	}
	if r.price("B") != 20000 {
		t.Fatal("an unscripted company moved with zero volatility")
	}
}

func TestAPriceNeverFallsBelowOneRupee(t *testing.T) {
	sc := still(10)
	sc.Events = []sim.Event{{ID: "crash", AtMinute: 0, Headline: "Crash", Impacts: []sim.Impact{{Symbol: "A", ShockPct: -99.9}, {Symbol: "A", ShockPct: -99.9}}}}
	r := newRig(t, sc, companies(), 0)
	r.run(120)
	if got := r.price("A"); got < 100 {
		t.Fatalf("A = %d paise, below the one-rupee floor", got)
	}
}

func TestRestartingContinuesTheMarketExactly(t *testing.T) {
	sc := sim.Scenario{Seed: 5, TickSeconds: 10, Volatility: sim.Volatility{DefaultPct: 0.7}}
	sc.Events = []sim.Event{{ID: "e", AtMinute: 4, Headline: "News", Impacts: []sim.Impact{{Sector: "IT", ShockPct: 5, RampMinutes: 2}}}}

	straight := newRig(t, sc, companies(), 20*time.Second)
	straight.run(900)

	a := newRig(t, sc, companies(), 20*time.Second)
	a.run(300)
	last := a.saved[len(a.saved)-1]

	// A fresh server: prices come back from the saved record, the engine carries on from its state.
	b := newRig(t, sc, companies(), 20*time.Second)
	b.now, b.clk.elapsed = a.now, a.clk.elapsed
	restored := map[string]money.Paise{}
	for k, v := range last.Prices {
		restored[k] = money.Paise(v)
	}
	b.prices.SetAll(restored, a.now)
	b.e.Adopt(last)
	b.wire = a.wire // the news already went out before the restart
	b.run(600)
	for _, c := range companies() {
		if b.price(c.Symbol) != straight.price(c.Symbol) {
			t.Errorf("%s after a restart = %d, uninterrupted = %d", c.Symbol, b.price(c.Symbol), straight.price(c.Symbol))
		}
	}
	if a.wire.count() != 1 {
		t.Errorf("the event was released %d times across the restart, want once", a.wire.count())
	}
}

func TestAnOrganiserCanFireAnEventNowButOnlyOnce(t *testing.T) {
	sc := still(10)
	sc.Events = []sim.Event{{ID: "later", AtMinute: 200, Headline: "Much later", Impacts: []sim.Impact{{Symbol: "A", ShockPct: 10}}}}
	r := newRig(t, sc, companies(), 0)
	if err := r.e.FireEvent("later", r.now); err != nil {
		t.Fatal(err)
	}
	if r.wire.count() != 1 {
		t.Fatal("firing an event did not release its news")
	}
	if err := r.e.FireEvent("later", r.now); err == nil {
		t.Fatal("an event fired twice")
	}
	if err := r.e.FireEvent("nope", r.now); err == nil {
		t.Fatal("firing an unknown event succeeded")
	}
	r.run(20)
	if !near(r.price("A"), 11000, 1) {
		t.Fatalf("A = %d, want +10%%", r.price("A"))
	}
	r.run(60 * 60 * 4) // its scheduled time passes: it must not fire again
	if r.wire.count() != 1 {
		t.Fatalf("released %d times", r.wire.count())
	}
	st := r.e.Status()
	if len(st.Items) != 1 || !st.Items[0].Fired {
		t.Fatalf("status = %+v", st)
	}
}

func TestScenarioValidation(t *testing.T) {
	bad := map[string]sim.Scenario{
		"negative volatility":     {Volatility: sim.Volatility{DefaultPct: -1}},
		"unknown sector":          {Volatility: sim.Volatility{BySector: map[string]float64{"Mining": 1}}},
		"duplicate ids":           {Events: []sim.Event{{ID: "x", Headline: "h"}, {ID: "x", Headline: "h"}}},
		"no headline":             {Events: []sim.Event{{ID: "x"}}},
		"impact with neither":     {Events: []sim.Event{{ID: "x", Headline: "h", Impacts: []sim.Impact{{ShockPct: 1}}}}},
		"impact with both":        {Events: []sim.Event{{ID: "x", Headline: "h", Impacts: []sim.Impact{{Sector: "IT", Symbol: "A", ShockPct: 1}}}}},
		"unknown company":         {Events: []sim.Event{{ID: "x", Headline: "h", Impacts: []sim.Impact{{Symbol: "ZZZ", ShockPct: 1}}}}},
		"a shock of minus 100":    {Events: []sim.Event{{ID: "x", Headline: "h", Impacts: []sim.Impact{{Symbol: "A", ShockPct: -100}}}}},
		"bad regime kind":         {Regimes: []sim.Regime{{ID: "r", Kind: "sideways", DurationMinutes: 1, Breadth: 1}}},
		"regime with no breadth":  {Regimes: []sim.Regime{{ID: "r", Kind: "bull", DurationMinutes: 1}}},
		"regime with no duration": {Regimes: []sim.Regime{{ID: "r", Kind: "bull", Breadth: 1}}},
		"path for unknown":        {Paths: map[string][]sim.PathPoint{"ZZZ": {{0, 1}}}},
		"path out of order":       {Paths: map[string][]sim.PathPoint{"A": {{5, 1}, {1, 2}}}},
		"path with a zero price":  {Paths: map[string][]sim.PathPoint{"A": {{0, 0}}}},
		"tick too long":           {TickSeconds: 99999},
	}
	for name, sc := range bad {
		if err := sc.Validate(companies()); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	ok := sim.DefaultScenario()
	if err := ok.Validate(companies()); err != nil {
		t.Fatalf("the default scenario: %v", err)
	}
}

func TestScenarioFilesAreReadStrictly(t *testing.T) {
	dir := t.TempDir()
	write := func(body string) string {
		p := filepath.Join(dir, "s.json")
		_ = os.WriteFile(p, []byte(body), 0o600)
		return p
	}
	sc, err := sim.Load(write(`{"seed": 9, "tickSeconds": 30, "volatility": {"defaultPct": 0.4},
		"events": [{"id":"e1","atMinute":12,"headline":"Hello","impacts":[{"sector":"IT","shockPct":-3,"rampMinutes":2}]}],
		"regimes": [{"id":"r1","kind":"bull","atMinute":150,"durationMinutes":20,"driftPctPerTick":0.1,"breadth":0.8}],
		"paths": {"A": [[0,100],[60,120]]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.Validate(companies()); err != nil || sc.Seed != 9 || len(sc.Events) != 1 || sc.Paths["A"][1][1] != 120 {
		t.Fatalf("scenario = %+v, err %v", sc, err)
	}
	if _, err := sim.Load(write(`{"seed": 1, "surprise": true}`)); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("an unknown field: %v", err)
	}
	if _, err := sim.Load(filepath.Join(dir, "missing.json")); err == nil {
		t.Fatal("a missing file was accepted")
	}
}
