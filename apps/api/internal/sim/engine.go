package sim

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"

	"stockastic/api/internal/market"
	"stockastic/api/internal/money"
	"stockastic/api/internal/news"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/universe"
)

// minPrice is the lowest a price may fall to (one rupee), so a run of bad news can never make a company free.
const minPrice money.Paise = 100

// ClockView is what the simulation needs from the event clock.
type ClockView interface {
	// Elapsed is how long the event has run, and whether it has started.
	Elapsed() (time.Duration, bool)
	MarketOpen() bool
	Stage() rulebook.Stage
}

// Publisher releases news (the dispatcher that handles the fund-manager head start).
type Publisher interface {
	Publish(kind news.Kind, headline, body string) news.Item
}

// Deps are the parts the engine works with.
type Deps struct {
	Prices    *market.Prices
	Companies []universe.Company
	Clock     ClockView
	News      Publisher
	// Lead is how long after fund managers the public sees news; a price reaction waits this long, so fund
	// managers have their window (Section 11). It is zero when news is not staggered (Phase 1).
	Lead func() time.Duration
	// Save makes the state durable. It is called after every price change and every event.
	Save func(State) error
	// OnPrices is told which prices changed, right after a price change.
	OnPrices func(at time.Time, changed map[string]money.Paise)
	Log      *slog.Logger
}

// Shock is part of an event's price effect still to be played out, one step per price change.
type Shock struct {
	Sector     string  `json:"sector,omitempty"`
	Symbol     string  `json:"symbol,omitempty"`
	PerTickPct float64 `json:"perTickPct"`
	TicksLeft  int     `json:"ticksLeft"`
}

// PendingShock is an event's effect waiting for the public release moment.
type PendingShock struct {
	ApplyAtMs int64    `json:"applyAtMs"`
	Impacts   []Impact `json:"impacts"`
}

// State is everything the engine must remember to carry on after a restart.
type State struct {
	Tick          int              `json:"tick"`
	MarketSeconds int              `json:"marketSeconds"`
	At            time.Time        `json:"at"`
	Prices        map[string]int64 `json:"prices"`
	Fired         []string         `json:"fired"`
	Pending       []PendingShock   `json:"pending"`
	Shocks        []Shock          `json:"shocks"`
}

type Engine struct {
	sc   Scenario
	d    Deps
	cos  []universe.Company
	byID map[string]universe.Company

	mu sync.Mutex
	st State
	// fired is the set form of st.Fired.
	fired map[string]bool
}

// New builds an engine. The scenario is validated against the company list.
func New(sc Scenario, d Deps) (*Engine, error) {
	if d.Prices == nil || d.Clock == nil || d.News == nil || len(d.Companies) == 0 {
		return nil, errors.New("sim: Prices, Clock, News and Companies are required")
	}
	if err := sc.Validate(d.Companies); err != nil {
		return nil, err
	}
	if d.Lead == nil {
		d.Lead = func() time.Duration { return 0 }
	}
	if d.Log == nil {
		d.Log = slog.Default()
	}
	cos := append([]universe.Company(nil), d.Companies...)
	sort.Slice(cos, func(i, j int) bool { return cos[i].Symbol < cos[j].Symbol })
	e := &Engine{sc: sc, d: d, cos: cos, byID: map[string]universe.Company{}, fired: map[string]bool{}}
	for _, c := range cos {
		e.byID[c.Symbol] = c
	}
	return e, nil
}

// TickSeconds is how often prices change, in seconds of open-market time.
func (e *Engine) TickSeconds() int { return e.sc.TickSeconds }

// Adopt carries the engine on from a saved state (after a restart). The caller has already put the saved
// prices back in the price store.
func (e *Engine) Adopt(s State) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.st = s
	e.st.Prices = nil
	e.fired = map[string]bool{}
	for _, id := range s.Fired {
		e.fired[id] = true
	}
}

// Run advances the simulation once a second until ctx ends. A panic is recovered and logged, and the
// simulation keeps going: a fault in one step must not stop the market.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			e.safeAdvance(now)
		}
	}
}

func (e *Engine) safeAdvance(now time.Time) {
	defer func() {
		if r := recover(); r != nil {
			e.d.Log.Error("price simulation step panicked", "panic", fmt.Sprint(r))
		}
	}()
	e.Advance(now)
}

type release struct {
	kind     news.Kind
	headline string
	body     string
}

// Advance moves the simulation forward by one second at time now. It is what Run calls, and is exported so
// tests can drive time exactly.
func (e *Engine) Advance(now time.Time) {
	elapsed, started := e.d.Clock.Elapsed()
	if !started {
		return
	}
	var out []release
	e.mu.Lock()
	dirty := e.fireDueLocked(elapsed, now, &out)
	if e.applyPendingLocked(now) {
		dirty = true
	}
	var changed map[string]money.Paise
	if e.d.Clock.MarketOpen() {
		e.st.MarketSeconds++
		if e.st.MarketSeconds%e.sc.TickSeconds == 0 {
			e.st.Tick++
			e.st.At = now
			changed = e.stepLocked(elapsed)
			dirty = true
		}
	}
	var snapshot State
	if dirty {
		snapshot = e.copyStateLocked()
		snapshot.Prices = pricesOf(e.d.Prices, changed)
	}
	e.mu.Unlock()

	for _, r := range out {
		e.d.News.Publish(r.kind, r.headline, r.body)
	}
	if changed != nil {
		e.d.Prices.SetAll(changed, now)
		snapshot.Prices = pricesOf(e.d.Prices, nil)
	}
	if dirty && e.d.Save != nil {
		if err := e.d.Save(snapshot); err != nil {
			e.d.Log.Error("could not save the price simulation state", "err", err)
		}
	}
	if changed != nil && e.d.OnPrices != nil {
		e.d.OnPrices(now, changed)
	}
}

func pricesOf(p *market.Prices, _ map[string]money.Paise) map[string]int64 {
	all := p.All()
	out := make(map[string]int64, len(all))
	for k, v := range all {
		out[k] = int64(v)
	}
	return out
}

func (e *Engine) copyStateLocked() State {
	s := e.st
	s.Fired = append([]string(nil), e.st.Fired...)
	s.Pending = append([]PendingShock(nil), e.st.Pending...)
	s.Shocks = append([]Shock(nil), e.st.Shocks...)
	return s
}

// fireDueLocked releases every event and regime announcement whose time has come, exactly once.
func (e *Engine) fireDueLocked(elapsed time.Duration, now time.Time, out *[]release) bool {
	mins := elapsed.Minutes()
	dirty := false
	for _, ev := range e.sc.Events {
		if mins >= ev.AtMinute && !e.fired[ev.ID] {
			e.fireEventLocked(ev, now, out)
			dirty = true
		}
	}
	for _, r := range e.sc.Regimes {
		if mins >= r.AtMinute && !e.fired[r.ID] {
			e.fireRegimeLocked(r, out)
			dirty = true
		}
	}
	return dirty
}

func (e *Engine) markFiredLocked(id string) {
	e.fired[id] = true
	e.st.Fired = append(e.st.Fired, id)
}

func (e *Engine) fireEventLocked(ev Event, now time.Time, out *[]release) {
	e.markFiredLocked(ev.ID)
	*out = append(*out, release{kind: news.KindNews, headline: ev.Headline, body: ev.Body})
	if len(ev.Impacts) > 0 {
		// The price reaction arrives when the public sees the news, so fund managers, who see it first,
		// have the lead window to act before it (Section 11).
		e.st.Pending = append(e.st.Pending, PendingShock{ApplyAtMs: now.Add(e.d.Lead()).UnixMilli(), Impacts: ev.Impacts})
	}
}

func (e *Engine) fireRegimeLocked(r Regime, out *[]release) {
	e.markFiredLocked(r.ID)
	h, b := r.Headline, r.Body
	if h == "" {
		h = map[string]string{KindBull: "The market has entered a Bull Run", KindBear: "The market has entered a Bear Run"}[r.Kind]
	}
	*out = append(*out, release{kind: news.KindRegime, headline: h, body: b})
}

// applyPendingLocked turns every pending effect whose moment has come into shocks that play out over the
// following price changes.
func (e *Engine) applyPendingLocked(now time.Time) bool {
	if len(e.st.Pending) == 0 {
		return false
	}
	keep := e.st.Pending[:0]
	applied := false
	for _, p := range e.st.Pending {
		if p.ApplyAtMs > now.UnixMilli() {
			keep = append(keep, p)
			continue
		}
		applied = true
		for _, im := range p.Impacts {
			ticks := int(math.Ceil(im.RampMinutes * 60 / float64(e.sc.TickSeconds)))
			if ticks < 1 {
				ticks = 1
			}
			// Spread a total move of ShockPct evenly (as a compound rate) over the ticks.
			per := (math.Pow(1+im.ShockPct/100, 1/float64(ticks)) - 1) * 100
			e.st.Shocks = append(e.st.Shocks, Shock{Sector: im.Sector, Symbol: im.Symbol, PerTickPct: per, TicksLeft: ticks})
		}
	}
	e.st.Pending = keep
	return applied
}

// stepLocked works out every company's next price.
func (e *Engine) stepLocked(elapsed time.Duration) map[string]money.Paise {
	cur := e.d.Prices.All()
	mins := elapsed.Minutes()
	out := make(map[string]money.Paise, len(e.cos))
	for i, c := range e.cos {
		if path, ok := e.sc.Paths[c.Symbol]; ok {
			out[c.Symbol] = pathPrice(path, mins)
			continue
		}
		pct := e.volatility(c) * e.normal(e.st.Tick, i)
		pct += e.regimeDrift(c, mins)
		for _, sh := range e.st.Shocks {
			if (sh.Symbol != "" && sh.Symbol == c.Symbol) || (sh.Sector != "" && sh.Sector == c.Sector) {
				pct += sh.PerTickPct
			}
		}
		px := money.Paise(math.Round(float64(cur[c.Symbol]) * (1 + pct/100)))
		out[c.Symbol] = e.bound(px)
	}
	keep := e.st.Shocks[:0]
	for _, sh := range e.st.Shocks {
		if sh.TicksLeft--; sh.TicksLeft > 0 {
			keep = append(keep, sh)
		}
	}
	e.st.Shocks = keep
	return out
}

// bound keeps a price on the tick grid and inside the scenario's floor and cap.
func (e *Engine) bound(px money.Paise) money.Paise {
	if e.sc.RoundTo > 0 {
		step := float64(money.FromRupees(e.sc.RoundTo))
		px = money.Paise(math.Round(float64(px)/step) * step)
	}
	if f := money.FromRupees(e.sc.PriceFloor); f > 0 {
		px = max(px, f)
	}
	if c := money.FromRupees(e.sc.PriceCap); c > 0 {
		px = min(px, c)
	}
	return max(px, minPrice)
}

func (e *Engine) volatility(c universe.Company) float64 {
	if v, ok := e.sc.Volatility.BySymbol[c.Symbol]; ok {
		return v
	}
	sector := c.Sector
	if v, ok := e.sc.Volatility.BySector[sector]; ok {
		return v
	}
	return e.sc.Volatility.DefaultPct
}

// normal is a repeatable standard-normal number for one company at one price change. It depends only on the
// scenario seed, so a restarted server continues with exactly the numbers it would have used.
func (e *Engine) normal(tick, idx int) float64 {
	seed := e.sc.Seed*1_000_003 + int64(tick)*7_919 + int64(idx)*104_729
	return rand.New(rand.NewSource(seed)).NormFloat64()
}

func hash32(s string) uint32 {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return h.Sum32()
}

// regimeDrift is the push a bull or bear run gives this company right now. A run drives only a share of
// companies (its breadth), and each by a different amount.
func (e *Engine) regimeDrift(c universe.Company, mins float64) float64 {
	var d float64
	for _, r := range e.sc.Regimes {
		if mins < r.AtMinute || mins >= r.AtMinute+r.DurationMinutes {
			continue
		}
		h := hash32(c.Symbol + "|" + r.ID)
		if float64(h%1000)/1000 >= r.Breadth {
			continue
		}
		mult := 0.5 + float64((h/1000)%1000)/1000
		push := r.DriftPctPerTick * mult
		if r.Kind == KindBear {
			push = -push
		}
		d += push
	}
	return d
}

// pathPrice reads a scripted price path at a time, joining its points with straight lines.
func pathPrice(path []PathPoint, mins float64) money.Paise {
	rupees := path[len(path)-1][1]
	switch {
	case mins <= path[0][0]:
		rupees = path[0][1]
	case mins < path[len(path)-1][0]:
		i := sort.Search(len(path), func(i int) bool { return path[i][0] > mins })
		a, b := path[i-1], path[i]
		rupees = a[1] + (b[1]-a[1])*(mins-a[0])/(b[0]-a[0])
	}
	return max(money.FromRupees(rupees), minPrice)
}

// ---- organiser controls and status ----

// FireEvent releases an event now, whatever its scheduled time. It fires at most once.
func (e *Engine) FireEvent(id string, now time.Time) error {
	var out []release
	e.mu.Lock()
	if e.fired[id] {
		e.mu.Unlock()
		return fmt.Errorf("sim: %q has already happened", id)
	}
	found := false
	for _, ev := range e.sc.Events {
		if ev.ID == id {
			e.fireEventLocked(ev, now, &out)
			found = true
		}
	}
	for _, r := range e.sc.Regimes {
		if r.ID == id {
			e.fireRegimeLocked(r, &out)
			found = true
		}
	}
	snapshot := e.copyStateLocked()
	e.mu.Unlock()
	if !found {
		return fmt.Errorf("sim: no event or regime %q", id)
	}
	for _, r := range out {
		e.d.News.Publish(r.kind, r.headline, r.body)
	}
	snapshot.Prices = pricesOf(e.d.Prices, nil)
	if e.d.Save != nil {
		return e.d.Save(snapshot)
	}
	return nil
}

// Item is one scheduled event or regime, as the organiser sees it.
type Item struct {
	ID       string  `json:"id"`
	Kind     string  `json:"kind"` // "news", "bull" or "bear"
	AtMinute float64 `json:"atMinute"`
	Headline string  `json:"headline"`
	Fired    bool    `json:"fired"`
	// Type is REAL, FAKE or DENIAL for market events: organiser-only labels, never shown to teams.
	Type     string `json:"type,omitempty"`
	Category string `json:"category,omitempty"`
	Impacts  int    `json:"impacts"`
}

// Status is what the organiser console shows about the simulation.
type Status struct {
	TickSeconds   int    `json:"tickSeconds"`
	Ticks         int    `json:"ticks"`
	ActiveShocks  int    `json:"activeShocks"`
	PendingShocks int    `json:"pendingShocks"`
	Scripted      int    `json:"scriptedCompanies"`
	Items         []Item `json:"items"`
}

func (e *Engine) Status() Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	s := Status{TickSeconds: e.sc.TickSeconds, Ticks: e.st.Tick, ActiveShocks: len(e.st.Shocks), PendingShocks: len(e.st.Pending), Scripted: len(e.sc.Paths), Items: []Item{}}
	for _, ev := range e.sc.Events {
		s.Items = append(s.Items, Item{ID: ev.ID, Kind: "news", AtMinute: ev.AtMinute, Headline: ev.Headline, Fired: e.fired[ev.ID], Type: ev.Type, Category: ev.Category, Impacts: len(ev.Impacts)})
	}
	for _, r := range e.sc.Regimes {
		h := r.Headline
		if h == "" {
			h = "Market regime: " + r.Kind
		}
		s.Items = append(s.Items, Item{ID: r.ID, Kind: r.Kind, AtMinute: r.AtMinute, Headline: h, Fired: e.fired[r.ID]})
	}
	sort.Slice(s.Items, func(i, j int) bool { return s.Items[i].AtMinute < s.Items[j].AtMinute })
	return s
}

// Reset returns the simulation to its start: no price updates done, no events fired, nothing waiting. The
// caller puts the opening prices back in the price store.
func (e *Engine) Reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.st = State{}
	e.fired = map[string]bool{}
}
