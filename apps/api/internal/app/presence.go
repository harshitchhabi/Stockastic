package app

import (
	"time"

	"stockastic/api/internal/wsapi"
)

// presence is whether a team has a live connection and when it last did.
type presence struct {
	online bool
	last   time.Time
}

func (a *App) onPresence(id string, _ bool) {
	// The hub reports a change; the truth is how many connections the account has right now. Asking again
	// keeps the answer right even if a quick disconnect and reconnect delivers the two reports out of order.
	online := a.Hub.Sockets(id) > 0
	a.presMu.Lock()
	a.pres[id] = presence{online: online, last: a.now()}
	a.presMu.Unlock()
}

// presenceOf reports whether the account is connected now, and when it last connected or disconnected
// (milliseconds since 1970, 0 if it has not connected since the server started).
func (a *App) presenceOf(id string) (online bool, lastSeen int64) {
	a.presMu.Lock()
	defer a.presMu.Unlock()
	p, ok := a.pres[id]
	if !ok {
		return false, 0
	}
	return p.online, p.last.UnixMilli()
}

// sessionOK says whether a login token stamped with this session version may still be used. A locked
// account has no valid sessions, and signing a user out raises the version so earlier tokens stop working.
func (u User) sessionOK(version int) bool {
	return !u.Locked && version == u.SessionVersion
}

// SessionOK is sessionOK for the HTTP layer.
func (a *App) SessionOK(u User, version int) bool { return u.sessionOK(version) }

// SignOut ends every login of one team. Its open pages are sent to the sign-in screen and its old tokens
// stop working; the team can sign in again straight away.
func (a *App) SignOut(actor User, reason, id string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	return a.Do(actor, "Signed out", u.DisplayName, reason, func() error {
		if _, err := a.updateUser(id, func(x *User) error { x.SessionVersion++; return nil }); err != nil {
			return err
		}
		a.Hub.DisconnectAccount(id, wsapi.CloseUnauthenticated, "signed out by an organiser")
		return nil
	})
}

// SignOutAll ends every team's logins at once. Organisers stay signed in.
func (a *App) SignOutAll(actor User, reason string) (int, error) {
	n := 0
	err := a.Do(actor, "Signed out every team", "everyone", reason, func() error {
		for _, u := range a.users.all() {
			if u.IsAdmin {
				continue
			}
			if _, err := a.updateUser(u.ID, func(x *User) error { x.SessionVersion++; return nil }); err != nil {
				return err
			}
			a.Hub.DisconnectAccount(u.ID, wsapi.CloseUnauthenticated, "signed out by an organiser")
			n++
		}
		return nil
	})
	return n, err
}

// Lock stops a team from logging in and ends its current sessions, until it is unlocked.
func (a *App) Lock(actor User, reason, id string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	return a.Do(actor, "Locked the account", u.DisplayName, reason, func() error {
		if _, err := a.updateUser(id, func(x *User) error { x.Locked = true; return nil }); err != nil {
			return err
		}
		a.Hub.DisconnectAccount(id, wsapi.CloseUnauthenticated, "account locked")
		return nil
	})
}

// Unlock lets a locked team log in again.
func (a *App) Unlock(actor User, reason, id string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	return a.Do(actor, "Unlocked the account", u.DisplayName, reason, func() error {
		_, err := a.updateUser(id, func(x *User) error { x.Locked = false; return nil })
		return err
	})
}
