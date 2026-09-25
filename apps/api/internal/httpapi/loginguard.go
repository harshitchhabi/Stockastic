package httpapi

import (
	"strings"
	"sync"
	"time"
)

// loginGuard slows password guessing. A lock is kept per email AND address together, so someone guessing from
// elsewhere can never lock a team out from its own device. A much higher limit per email alone (all addresses
// together) still bounds guessing spread over many addresses.
type loginGuard struct {
	mu   sync.Mutex
	pair map[string]*attempts // email|address
	mail map[string]*attempts // email alone
}

type attempts struct {
	count int
	first time.Time
	until time.Time
}

const (
	// Ten wrong passwords in five minutes from one address at one email lock that pair for two minutes. It only
	// blocks new sign-ins: a team already signed in is never affected.
	pairFailures = 10
	// A hundred wrong passwords at one email in five minutes, from anywhere, lock it for two minutes.
	mailFailures = 100
	failWindow   = 5 * time.Minute
	lockFor      = 2 * time.Minute
)

func newLoginGuard() *loginGuard {
	return &loginGuard{pair: map[string]*attempts{}, mail: map[string]*attempts{}}
}

func key(email string) string { return strings.ToLower(strings.TrimSpace(email)) }

func blockedIn(m map[string]*attempts, k string) time.Duration {
	if a := m[k]; a != nil {
		if d := time.Until(a.until); d > 0 {
			return d
		}
	}
	return 0
}

// blocked returns how long this email from this address is locked out (zero if it is not).
func (g *loginGuard) blocked(email, addr string) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	e := key(email)
	return max(blockedIn(g.pair, e+"|"+addr), blockedIn(g.mail, e))
}

func bump(m map[string]*attempts, k string, limit int, now time.Time) {
	if len(m) > 20000 { // bound memory against a flood of made-up emails
		for x, a := range m {
			if now.After(a.until) && now.Sub(a.first) > failWindow {
				delete(m, x)
			}
		}
	}
	a := m[k]
	if a == nil || now.Sub(a.first) > failWindow {
		a = &attempts{first: now}
		m[k] = a
	}
	a.count++
	if a.count >= limit {
		a.until = now.Add(lockFor)
	}
}

func (g *loginGuard) failed(email, addr string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	e, now := key(email), time.Now()
	bump(g.pair, e+"|"+addr, pairFailures, now)
	bump(g.mail, e, mailFailures, now)
}

// ok forgets the failures of this email at this address after a good sign-in.
func (g *loginGuard) ok(email, addr string) {
	g.mu.Lock()
	delete(g.pair, key(email)+"|"+addr)
	g.mu.Unlock()
}

// clear lifts every lock. It returns how many were in force.
func (g *loginGuard) clear() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, m := range []map[string]*attempts{g.pair, g.mail} {
		for k, a := range m {
			if time.Until(a.until) > 0 {
				n++
			}
			delete(m, k)
		}
	}
	return n
}
