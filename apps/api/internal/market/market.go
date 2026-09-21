// Package market holds the current price of every company and its recent history. Prices are set by the
// price simulation (package sim); nothing here generates them, and participants' trades do not move them
// (a trade executes at the price shown).
package market

import (
	"sort"
	"sync"
	"time"

	"stockastic/api/internal/money"
	"stockastic/api/internal/universe"
)

// MaxHistory bounds memory: each company keeps this many of its most recent prices.
const MaxHistory = 2000

type Point struct {
	Price money.Paise
	At    time.Time
}

type series struct {
	open money.Paise
	cur  money.Paise
	pts  []Point
}

type Prices struct {
	mu sync.RWMutex
	m  map[string]*series
}

// New starts every company at its opening price; the first point of its history is that price.
func New(cs []universe.Company, start time.Time) *Prices {
	p := &Prices{m: make(map[string]*series, len(cs))}
	for _, c := range cs {
		p.m[c.Symbol] = &series{open: c.Open, cur: c.Open, pts: []Point{{Price: c.Open, At: start}}}
	}
	return p
}

// Price is the company's current price. It satisfies trading.Prices.
func (p *Prices) Price(symbol string) (money.Paise, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := p.m[symbol]
	if s == nil {
		return 0, false
	}
	return s.cur, true
}

// Open is the price the company started the event at.
func (p *Prices) Open(symbol string) (money.Paise, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := p.m[symbol]
	if s == nil {
		return 0, false
	}
	return s.open, true
}

// Set records a new price. Unknown companies and non-positive prices are ignored.
func (p *Prices) Set(symbol string, price money.Paise, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.setLocked(symbol, price, at)
}

func (p *Prices) setLocked(symbol string, price money.Paise, at time.Time) {
	s := p.m[symbol]
	if s == nil || price <= 0 {
		return
	}
	s.cur = price
	s.pts = append(s.pts, Point{Price: price, At: at})
	if len(s.pts) > MaxHistory {
		s.pts = append(s.pts[:0], s.pts[len(s.pts)-MaxHistory:]...)
	}
}

// SetAll records new prices for many companies at one moment.
func (p *Prices) SetAll(prices map[string]money.Paise, at time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for sym, px := range prices {
		p.setLocked(sym, px, at)
	}
}

// History is the company's recent prices, oldest first (a copy).
func (p *Prices) History(symbol string) []Point {
	p.mu.RLock()
	defer p.mu.RUnlock()
	s := p.m[symbol]
	if s == nil {
		return nil
	}
	return append([]Point(nil), s.pts...)
}

// All is every company's current price (a copy).
func (p *Prices) All() map[string]money.Paise {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make(map[string]money.Paise, len(p.m))
	for k, s := range p.m {
		out[k] = s.cur
	}
	return out
}

// Symbols lists every company, sorted.
func (p *Prices) Symbols() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.m))
	for k := range p.m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Snapshot is a frozen copy of every price at one moment, taken at a freeze block so results are computed
// from the freeze-time prices and cannot drift afterwards.
type Snapshot struct {
	Name   string
	At     time.Time
	Prices map[string]money.Paise
}

// Take captures the current prices.
func (p *Prices) Take(name string, at time.Time) Snapshot {
	return Snapshot{Name: name, At: at, Prices: p.All()}
}

// Price returns a company's price in the snapshot.
func (s Snapshot) Price(symbol string) (money.Paise, bool) {
	v, ok := s.Prices[symbol]
	return v, ok
}
