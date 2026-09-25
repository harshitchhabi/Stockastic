// Package sim is the stock-price simulation: it decides how every company's price moves, and when the
// simulated market events (news, bull and bear runs) happen.
//
// What is fixed here is the machinery: prices move only while the market is open, a news item reaches
// fund managers first and the public a moment later (Section 11) with its price effect arriving when the
// public sees it, and the same scenario always produces the same market, including across a restart.
//
// What is data is the Scenario: how volatile each sector is, which events happen and when, what they do to
// which companies, and optionally exact price paths. A scenario is a JSON file, so the organisers' final
// numbers replace the placeholder without any code change. The DefaultScenario is a PLACEHOLDER: a plain
// random walk with no events.
package sim

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"

	"stockastic/api/internal/universe"
)

const (
	KindBull = "bull"
	KindBear = "bear"
)

// PathPoint is one point of a scripted price path: [minutes since the event started, price in rupees].
type PathPoint [2]float64

// Volatility is the typical size of one price change, as the standard deviation in percent.
type Volatility struct {
	DefaultPct float64            `json:"defaultPct"`
	BySector   map[string]float64 `json:"bySector"`
	// BySymbol overrides the sector and default values for one company.
	BySymbol map[string]float64 `json:"bySymbol"`
}

// Impact is what an event does to prices: a total move in percent, spread over RampMinutes, for every
// company in a sector, or for one company.
type Impact struct {
	Sector      string  `json:"sector"`
	Symbol      string  `json:"symbol"`
	ShockPct    float64 `json:"shockPct"`
	RampMinutes float64 `json:"rampMinutes"`
}

// Event is a piece of simulated market news and its effect on prices.
type Event struct {
	ID       string   `json:"id"`
	AtMinute float64  `json:"atMinute"` // minutes since the event started
	Headline string   `json:"headline"`
	Body     string   `json:"body"`
	Impacts  []Impact `json:"impacts"`
	// Type and Category are labels for the organiser only (REAL, FAKE, DENIAL; MACRO, POLICY, RUMOUR...).
	// A FAKE or DENIAL item has no impacts: it is news that moves nothing.
	Type     string `json:"type"`
	Category string `json:"category"`
	// Phase says which phase the item was designed for ("phase1" or "phase2"). It only drives a warning when the
	// schedule puts the item in a block of a different phase (the fund managers' head start depends on it).
	Phase string `json:"phase,omitempty"`
}

// Regime is a bull or bear run: broad optimism or pessimism, felt by most companies but not all, and not
// equally. Breadth is the share of companies it drives; DriftPctPerTick is the average push per price change.
type Regime struct {
	ID              string  `json:"id"`
	Kind            string  `json:"kind"`
	AtMinute        float64 `json:"atMinute"`
	DurationMinutes float64 `json:"durationMinutes"`
	DriftPctPerTick float64 `json:"driftPctPerTick"`
	Breadth         float64 `json:"breadth"`
	Headline        string  `json:"headline"`
	Body            string  `json:"body"`
}

// Scenario is everything the price simulation needs that is data rather than code.
type Scenario struct {
	Seed int64 `json:"seed"`
	// TickSeconds is how many seconds of open-market time pass between price changes.
	TickSeconds int                    `json:"tickSeconds"`
	Volatility  Volatility             `json:"volatility"`
	Events      []Event                `json:"events"`
	Regimes     []Regime               `json:"regimes"`
	Paths       map[string][]PathPoint `json:"paths"`
	// PriceFloor, PriceCap and RoundTo (rupees) keep every price inside the designed range and on the tick
	// grid. Zero means no limit (floor: one rupee) or no rounding.
	PriceFloor float64 `json:"priceFloor"`
	PriceCap   float64 `json:"priceCap"`
	RoundTo    float64 `json:"roundTo"`

	// PricesFile is a price table: the exact price of every company at every step, read from a file next to the
	// scenario. When there is one, prices follow it exactly (nothing is random) and the volatility, limits,
	// impacts and runs above are not used. Step k is the k-th TickSeconds of open-market time, whatever the
	// clock or the schedule says, so pausing, editing the schedule and market closures cannot put prices out
	// of step with the data.
	PricesFile string `json:"pricesFile"`
	// NewsClock says what event times are measured in: "event" (minutes since the event started, the default)
	// or "market" (minutes of open-market time, which stays in step with a price table).
	NewsClock string `json:"newsClock"`

	table *PriceTable
}

// PriceTable is the exact price of every company at every step. Rows[k][i] is Symbols[i] at step k, in rupees.
type PriceTable struct {
	BarSeconds int         `json:"barSeconds"`
	Symbols    []string    `json:"symbols"`
	Rows       [][]float64 `json:"rows"`
}

// HasTable reports whether prices come from a table.
func (sc Scenario) HasTable() bool { return sc.table != nil }

// TableSteps is how many steps the price table has.
func (sc Scenario) TableSteps() int {
	if sc.table == nil {
		return 0
	}
	return len(sc.table.Rows)
}

// DefaultScenario is a PLACEHOLDER until the organisers' data arrives: a plain random walk, no events.
func DefaultScenario() Scenario {
	return Scenario{Seed: 1, TickSeconds: 60, Volatility: Volatility{DefaultPct: 0.3}}
}

// Load reads a scenario file. It is strict: an unknown field or an event that makes no sense is an error,
// so the event never starts on a scenario that was only half understood.
func Load(path string) (Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Scenario{}, err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var sc Scenario
	if err := dec.Decode(&sc); err != nil {
		return Scenario{}, fmt.Errorf("sim: %s: %w", path, err)
	}
	if sc.PricesFile != "" {
		p := sc.PricesFile
		if !filepath.IsAbs(p) {
			p = filepath.Join(filepath.Dir(path), p)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return Scenario{}, fmt.Errorf("sim: price table: %w", err)
		}
		d := json.NewDecoder(bytes.NewReader(b))
		d.DisallowUnknownFields()
		var t PriceTable
		if err := d.Decode(&t); err != nil {
			return Scenario{}, fmt.Errorf("sim: %s: %w", p, err)
		}
		sc.table = &t
	}
	return sc, nil
}

// Validate checks the scenario against the company list and fills in defaults.
func (sc *Scenario) Validate(cs []universe.Company) error {
	if sc.table != nil && sc.TickSeconds == 0 {
		sc.TickSeconds = sc.table.BarSeconds
	}
	if sc.TickSeconds == 0 {
		sc.TickSeconds = 60
	}
	if sc.NewsClock != "" && sc.NewsClock != "event" && sc.NewsClock != "market" {
		return errors.New(`sim: newsClock must be "event" or "market"`)
	}
	if t := sc.table; t != nil {
		if t.BarSeconds != sc.TickSeconds {
			return fmt.Errorf("sim: the price table has a step every %d seconds but tickSeconds is %d", t.BarSeconds, sc.TickSeconds)
		}
		if len(t.Rows) == 0 || len(t.Symbols) != len(cs) {
			return fmt.Errorf("sim: the price table has %d companies and %d steps, the company list has %d companies", len(t.Symbols), len(t.Rows), len(cs))
		}
		want := map[string]bool{}
		for _, c := range cs {
			want[c.Symbol] = true
		}
		for _, s := range t.Symbols {
			if !want[s] {
				return fmt.Errorf("sim: the price table has %q, which is not in the company list", s)
			}
			delete(want, s)
		}
		for k, row := range t.Rows {
			if len(row) != len(t.Symbols) {
				return fmt.Errorf("sim: price table step %d has %d prices, want %d", k, len(row), len(t.Symbols))
			}
			for i, v := range row {
				if math.IsNaN(v) || math.IsInf(v, 0) || v <= 0 {
					return fmt.Errorf("sim: price table step %d, %s: %v is not a price", k, t.Symbols[i], v)
				}
			}
		}
	}
	if sc.TickSeconds < 1 || sc.TickSeconds > 3600 {
		return errors.New("sim: tickSeconds must be from 1 to 3600")
	}
	if sc.Volatility.DefaultPct < 0 || sc.Volatility.DefaultPct > 50 {
		return errors.New("sim: volatility.defaultPct must be from 0 to 50")
	}
	symbols, sectors := map[string]bool{}, map[string]bool{}
	for _, c := range cs {
		symbols[c.Symbol] = true
		if c.Sector != "" {
			sectors[c.Sector] = true
		}
	}
	for s, v := range sc.Volatility.BySymbol {
		if !symbols[s] {
			return fmt.Errorf("sim: volatility for unknown company %q", s)
		}
		if v < 0 || v > 50 {
			return fmt.Errorf("sim: volatility for %q must be from 0 to 50", s)
		}
	}
	if sc.PriceFloor < 0 || sc.PriceCap < 0 || (sc.PriceCap > 0 && sc.PriceCap < sc.PriceFloor) || sc.RoundTo < 0 {
		return errors.New("sim: priceFloor, priceCap and roundTo must not be negative, and the cap must not be below the floor")
	}
	for s, v := range sc.Volatility.BySector {
		if !sectors[s] {
			return fmt.Errorf("sim: volatility for unknown sector %q", s)
		}
		if v < 0 || v > 50 {
			return fmt.Errorf("sim: volatility for %q must be from 0 to 50", s)
		}
	}
	ids := map[string]bool{}
	for i, e := range sc.Events {
		if e.ID == "" || ids[e.ID] {
			return fmt.Errorf("sim: event %d has an empty or duplicate id %q", i, e.ID)
		}
		ids[e.ID] = true
		if e.AtMinute < 0 {
			return fmt.Errorf("sim: event %q happens before the event starts", e.ID)
		}
		if e.Headline == "" {
			return fmt.Errorf("sim: event %q has no headline", e.ID)
		}
		for _, im := range e.Impacts {
			if (im.Sector == "") == (im.Symbol == "") {
				return fmt.Errorf("sim: an impact of event %q must name a sector or a symbol, not both or neither", e.ID)
			}
			if im.Sector != "" && !sectors[im.Sector] {
				return fmt.Errorf("sim: event %q hits unknown sector %q", e.ID, im.Sector)
			}
			if im.Symbol != "" && !symbols[im.Symbol] {
				return fmt.Errorf("sim: event %q hits unknown company %q", e.ID, im.Symbol)
			}
			if im.ShockPct <= -100 || im.ShockPct > 1000 {
				return fmt.Errorf("sim: event %q has an impossible shock of %v%%", e.ID, im.ShockPct)
			}
			if im.RampMinutes < 0 {
				return fmt.Errorf("sim: event %q has a negative ramp", e.ID)
			}
		}
	}
	for i, r := range sc.Regimes {
		if r.ID == "" || ids[r.ID] {
			return fmt.Errorf("sim: regime %d has an empty or duplicate id %q", i, r.ID)
		}
		ids[r.ID] = true
		if r.Kind != KindBull && r.Kind != KindBear {
			return fmt.Errorf("sim: regime %q kind must be %q or %q", r.ID, KindBull, KindBear)
		}
		if r.AtMinute < 0 || r.DurationMinutes <= 0 {
			return fmt.Errorf("sim: regime %q needs a start at or after 0 and a positive duration", r.ID)
		}
		if r.Breadth <= 0 || r.Breadth > 1 {
			return fmt.Errorf("sim: regime %q breadth must be above 0 and at most 1", r.ID)
		}
		if r.DriftPctPerTick < 0 || r.DriftPctPerTick > 20 {
			return fmt.Errorf("sim: regime %q drift must be from 0 to 20 (its direction comes from its kind)", r.ID)
		}
	}
	for sym, path := range sc.Paths {
		if !symbols[sym] {
			return fmt.Errorf("sim: a scripted path for unknown company %q", sym)
		}
		if len(path) == 0 {
			return fmt.Errorf("sim: the path for %q is empty", sym)
		}
		if !sort.SliceIsSorted(path, func(i, j int) bool { return path[i][0] < path[j][0] }) {
			return fmt.Errorf("sim: the path for %q is not in time order", sym)
		}
		for _, p := range path {
			if p[0] < 0 || p[1] <= 0 {
				return fmt.Errorf("sim: the path for %q has a bad point %v", sym, p)
			}
		}
	}
	return nil
}
