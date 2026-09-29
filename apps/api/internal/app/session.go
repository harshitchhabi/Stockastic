package app

import (
	"sync"
	"time"

	"stockastic/api/internal/auth"
)

// Signing out cancels that one sign-in on the server, not just in the browser: a copy of the token (left on a shared
// computer, say) stops working at once. Other sign-ins are untouched: the person's other devices, and their
// teammates. A cancellation is kept only until the token would have expired anyway.

// KindRevoke is a signed-out token in the log.
const KindRevoke = "revoke"

type revocation struct {
	ID      string `json:"id"`
	Expires int64  `json:"exp"` // unix milliseconds
}

type revokedSet struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func newRevokedSet() *revokedSet { return &revokedSet{m: map[string]time.Time{}} }

func (r *revokedSet) add(id string, until, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !until.After(now) {
		return
	}
	r.m[id] = until
	// forget the ones that have expired by now, so the set stays small
	if len(r.m)%256 == 0 {
		for k, t := range r.m {
			if !t.After(now) {
				delete(r.m, k)
			}
		}
	}
}

func (r *revokedSet) has(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.m[id]
	return ok
}

// Session checks a token and returns the login it belongs to, or false if it is not valid now: expired, signed out,
// or belonging to a login that has been signed out, removed, locked or deleted.
func (a *App) Session(token string) (Login, auth.Token, bool) {
	t, err := a.Signer.Inspect(token)
	if err != nil {
		return Login{}, auth.Token{}, false
	}
	if t.ID != "" && a.revoked.has(t.ID) {
		return Login{}, auth.Token{}, false
	}
	l, ok := a.Resolve(t.Subject, t.Version)
	return l, t, ok
}

// SignOutToken cancels one sign-in. Tokens issued before tokens had ids cannot be cancelled one by one; they expire
// as before.
func (a *App) SignOutToken(t auth.Token) error {
	if t.ID == "" {
		return nil
	}
	if err := a.wal.Append(KindRevoke, revocation{ID: t.ID, Expires: t.Expires.UnixMilli()}); err != nil {
		return err
	}
	a.revoked.add(t.ID, t.Expires, a.now())
	return nil
}
