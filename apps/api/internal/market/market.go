// Package market tracks each company's last price, opening price and recent price history, updated from
// matched trades. It generates nothing: prices exist only because participants traded.
package market

import (
	"sync"
	"time"

	"stockastic/api/internal/money"
	"stockastic/api/internal/universe"
)

// MaxHistory bounds memory: each company keeps this many of its most recent trade prices.
const MaxHistory = 2000

type Point struct {
	Price money.Paise
	At    time.Time
}

type series struct {
	open   money.Paise
	last   money.Paise
	traded bool
	pts    []Point
}

type Tracker struct {
	mu sync.RWMutex
	m  map[string]*series
}

// New starts every company at its opening price; the first point of its history is that price.
func New(cs []universe.Company, start time.Time) *Tracker {
	t := &Tracker{m: make(map[string]*series, len(cs))}
	for _, c := range cs {
		t.m[c.Symbol] = &series{open: c.Open, last: c.Open, pts: []Point{{Price: c.Open, At: start}}}
	}
	return t
}

// PriceUpdate implements engine.PriceSink: the engine reports the last trade price after every fill.
func (t *Tracker) PriceUpdate(symbol string, price money.Paise, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.m[symbol]
	if s == nil {
		return
	}
	s.last, s.traded = price, true
	s.pts = append(s.pts, Point{Price: price, At: at})
	if len(s.pts) > MaxHistory {
		s.pts = append(s.pts[:0], s.pts[len(s.pts)-MaxHistory:]...)
	}
}

func (t *Tracker) Last(symbol string) (money.Paise, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.m[symbol]
	if s == nil {
		return 0, false
	}
	return s.last, true
}

// Traded reports the last price only if the company has actually traded (used to mark holdings).
func (t *Tracker) Traded(symbol string) (money.Paise, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.m[symbol]
	if s == nil || !s.traded {
		return 0, false
	}
	return s.last, true
}

func (t *Tracker) Open(symbol string) (money.Paise, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.m[symbol]
	if s == nil {
		return 0, false
	}
	return s.open, true
}

func (t *Tracker) History(symbol string) []Point {
	t.mu.RLock()
	defer t.mu.RUnlock()
	s := t.m[symbol]
	if s == nil {
		return nil
	}
	return append([]Point(nil), s.pts...)
}
