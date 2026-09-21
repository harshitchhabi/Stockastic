// Package ratelimit enforces the rulebook's trade-rate limit (Sec 11/14): at most N trades in any
// rolling window, keyed by PLATFORM ACCOUNT — never by person or by login. A Fund Management Team's
// six members share one trading account and therefore one limit; that is what closes the
// throughput-stacking gap. Callers must pass the trading account id (a fund's shared account), not
// the id of whichever member logged in.
package ratelimit

import (
	"sync"
	"time"

	"stockastic/api/internal/rulebook"
)

// Limiter is a rolling-window limiter: an event counts against the limit for exactly one window.
type Limiter struct {
	mu     sync.Mutex
	n      int
	window time.Duration
	hits   map[string][]time.Time
}

func New(n int, window time.Duration) *Limiter {
	return &Limiter{n: n, window: window, hits: make(map[string][]time.Time)}
}

// FromRulebook builds the limiter from rulebook.rateLimits.
func FromRulebook(r rulebook.RateLimits) *Limiter { return New(r.TradesPerWindow, r.Window()) }

func (l *Limiter) prune(key string, now time.Time) []time.Time {
	cutoff := now.Add(-l.window)
	h := l.hits[key]
	i := 0
	for i < len(h) && !h[i].After(cutoff) { // an event at t stops counting at exactly t+window
		i++
	}
	h = h[i:]
	if len(h) == 0 {
		delete(l.hits, key)
		return nil
	}
	l.hits[key] = h
	return h
}

// Allow records one trade for the account if it is within the limit. When it is not, nothing is
// recorded and retryAfter says how long until the oldest counted trade ages out.
func (l *Limiter) Allow(account string, now time.Time) (ok bool, retryAfter time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h := l.prune(account, now)
	if len(h) >= l.n {
		return false, h[0].Add(l.window).Sub(now)
	}
	l.hits[account] = append(h, now)
	return true, 0
}

// Refund undoes the most recent Allow for the account. Use it when an order that consumed a slot was
// then not processed at all (frozen, journal failure), so a platform-side rejection does not cost
// the team one of its two trades this minute.
func (l *Limiter) Refund(account string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if h := l.hits[account]; len(h) > 0 {
		if h = h[:len(h)-1]; len(h) == 0 {
			delete(l.hits, account)
		} else {
			l.hits[account] = h
		}
	}
}

// Sweep drops accounts with nothing left in their window, bounding memory over a long event.
func (l *Limiter) Sweep(now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k := range l.hits {
		l.prune(k, now)
	}
}

// Tracked is the number of accounts currently holding a counted trade.
func (l *Limiter) Tracked() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.hits)
}

// Reset forgets every account's recent trades.
func (l *Limiter) Reset() {
	l.mu.Lock()
	for k := range l.hits {
		delete(l.hits, k)
	}
	l.mu.Unlock()
}
