package httpapi

import (
	"strings"
	"sync"
	"time"
)

// loginGuard slows password guessing per email address, not per IP: a whole venue can share one
// address, so an IP limit would lock out honest teams.
type loginGuard struct {
	mu sync.Mutex
	m  map[string]*attempts
}

type attempts struct {
	count int
	first time.Time
	until time.Time
}

const (
	// Ten wrong passwords in five minutes lock that address for two minutes. It only blocks new sign-ins: a team
	// that is already signed in is never affected, so a rival guessing at its email cannot throw it out mid-trade.
	maxFailures = 10
	failWindow  = 5 * time.Minute
	lockFor     = 2 * time.Minute
)

func newLoginGuard() *loginGuard { return &loginGuard{m: map[string]*attempts{}} }

func key(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

// blocked returns how long the email is locked out (zero if it is not).
func (g *loginGuard) blocked(email string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	a := g.m[key(email)]
	if a == nil {
		return 0
	}
	if d := time.Until(a.until); d > 0 {
		return d
	}
	return 0
}

func (g *loginGuard) failed(email string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	k, now := key(email), time.Now()
	if len(g.m) > 10000 { // bound memory against a flood of made-up emails
		for x, a := range g.m {
			if now.After(a.until) && now.Sub(a.first) > failWindow {
				delete(g.m, x)
			}
		}
	}
	a := g.m[k]
	if a == nil || now.Sub(a.first) > failWindow {
		a = &attempts{first: now}
		g.m[k] = a
	}
	a.count++
	if a.count >= maxFailures {
		a.until = now.Add(lockFor)
	}
}

func (g *loginGuard) ok(email string) {
	g.mu.Lock()
	delete(g.m, key(email))
	g.mu.Unlock()
}
