package app

import (
	"errors"
	"net/mail"
	"strings"
	"sync"
	"time"

	"stockastic/api/internal/auth"
	"stockastic/api/internal/ids"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/store"
)

const (
	RoleInvestor    = "investor"
	RoleFundManager = "fund_manager"

	StatusActive       = "active"
	StatusWarned       = "warned"
	StatusDisqualified = "disqualified"
)

var (
	ErrEmailTaken         = errors.New("email_taken")
	ErrInvalidCredentials = errors.New("invalid_credentials")
	ErrSignupClosed       = errors.New("signup_closed")
	ErrDisqualified       = errors.New("account_disqualified")
	ErrUnknownUser        = errors.New("unknown_account")
	ErrAccountLocked      = errors.New("account_locked")
)

// BadRequest is a validation failure whose message is safe to show the caller.
type BadRequest struct{ Code, Message string }

func (e *BadRequest) Error() string { return e.Code }

func bad(code, msg string) error { return &BadRequest{code, msg} }

// User is a team's login and standing. Its ID is also its trading account id.
type User struct {
	ID           string
	Email        string
	DisplayName  string
	PasswordHash string
	Role         string
	IsAdmin      bool
	Status       string
	Warnings     int
	CreatedAt    time.Time
	// Locked stops the account from logging in or using any session at all.
	Locked bool
	// SessionVersion is stamped into every login token. Signing a team out raises it, which cancels all
	// of that team's earlier tokens at once.
	SessionVersion int
}

type userStore struct {
	mu      sync.RWMutex
	byID    map[string]*User
	byEmail map[string]*User
}

func newUserStore() *userStore {
	return &userStore{byID: map[string]*User{}, byEmail: map[string]*User{}}
}

func normEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func (s *userStore) get(id string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[id]
	if !ok {
		return User{}, false
	}
	return *u, true
}

func (s *userStore) byEmailAddr(email string) (User, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byEmail[normEmail(email)]
	if !ok {
		return User{}, false
	}
	return *u, true
}

func (s *userStore) all() []User {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]User, 0, len(s.byID))
	for _, u := range s.byID {
		out = append(out, *u)
	}
	return out
}

// put inserts or replaces by id. An email already held by a different user is refused.
func (s *userStore) put(u User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := normEmail(u.Email)
	if other, ok := s.byEmail[key]; ok && other.ID != u.ID {
		return ErrEmailTaken
	}
	if old, ok := s.byID[u.ID]; ok && normEmail(old.Email) != key {
		delete(s.byEmail, normEmail(old.Email))
	}
	c := u
	s.byID[u.ID], s.byEmail[key] = &c, &c
	return nil
}

// Signup creates a team account with the rulebook's starting capital.
func (a *App) Signup(displayName, email, password string) (User, error) {
	if !a.cfg.AllowSignup {
		return User{}, ErrSignupClosed
	}
	displayName = strings.TrimSpace(displayName)
	if n := len([]rune(displayName)); n < 2 || n > 40 {
		return User{}, bad("invalid_display_name", "Team name must be 2 to 40 characters.")
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || len(addr.Address) > 254 {
		return User{}, bad("invalid_email", "Enter a valid email address.")
	}
	if len(password) < 8 || len(password) > 72 {
		return User{}, bad("invalid_password", "Password must be 8 to 72 characters.")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return User{}, err
	}
	u := User{ID: ids.New(), Email: normEmail(addr.Address), DisplayName: displayName, PasswordHash: hash,
		Role: RoleInvestor, Status: StatusActive, CreatedAt: a.now()}
	// Reserve the email first so two concurrent signups cannot both succeed.
	if err := a.users.put(u); err != nil {
		return User{}, err
	}
	if err := a.persistUser(u); err != nil {
		a.users.mu.Lock()
		delete(a.users.byID, u.ID)
		delete(a.users.byEmail, u.Email)
		a.users.mu.Unlock()
		return User{}, err
	}
	if err := a.Ledger.Open(ledger.Account{ID: u.ID, Name: u.DisplayName, Kind: ledger.KindTeam}, a.RB.StartingCapital()); err != nil {
		return User{}, err
	}
	return u, nil
}

// Login verifies a password. The same error is returned for an unknown email and a wrong password.
func (a *App) Login(email, password string) (User, error) {
	u, ok := a.users.byEmailAddr(email)
	if !ok {
		// Spend comparable time so response timing does not reveal which emails exist.
		auth.CheckPassword(a.dummyHash, password)
		return User{}, ErrInvalidCredentials
	}
	if !auth.CheckPassword(u.PasswordHash, password) {
		return User{}, ErrInvalidCredentials
	}
	if u.Locked {
		return User{}, ErrAccountLocked
	}
	return u, nil
}

func (a *App) persistUser(u User) error { return a.wal.Append(store.KindUser, u) }

// User returns the current state of an account.
func (a *App) User(id string) (User, bool) { return a.users.get(id) }

// Users returns every account.
func (a *App) Users() []User { return a.users.all() }

func (a *App) updateUser(id string, f func(*User) error) (User, error) {
	a.users.mu.Lock()
	cur, ok := a.users.byID[id]
	if !ok {
		a.users.mu.Unlock()
		return User{}, ErrUnknownUser
	}
	next := *cur
	if err := f(&next); err != nil {
		a.users.mu.Unlock()
		return User{}, err
	}
	// Durable first: only a stored change becomes visible.
	if err := a.persistUser(next); err != nil {
		a.users.mu.Unlock()
		return User{}, err
	}
	*cur = next
	a.users.mu.Unlock()
	if !next.IsAdmin {
		a.Hub.SetRole(id, next.Role) // sockets already open pick up a role change straight away
	}
	return next, nil
}

// SeedAdmin makes sure the organiser account exists with the given password.
func (a *App) SeedAdmin(email, password, name string) error {
	if email == "" || password == "" {
		return nil
	}
	if len(password) < 12 {
		return errors.New("app: ADMIN_PASSWORD must be at least 12 characters")
	}
	if name == "" {
		name = "Organiser"
	}
	if u, ok := a.users.byEmailAddr(email); ok {
		if u.IsAdmin && auth.CheckPassword(u.PasswordHash, password) {
			return nil
		}
		hash, err := auth.HashPassword(password)
		if err != nil {
			return err
		}
		_, err = a.updateUser(u.ID, func(x *User) error { x.PasswordHash, x.IsAdmin, x.Status = hash, true, StatusActive; return nil })
		return err
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	u := User{ID: ids.New(), Email: normEmail(email), DisplayName: name, PasswordHash: hash, Role: RoleInvestor,
		IsAdmin: true, Status: StatusActive, CreatedAt: a.now()}
	if err := a.users.put(u); err != nil {
		return err
	}
	return a.persistUser(u)
}

// orderable reports whether the user may place orders at all.
func (u User) orderable() error {
	if u.Status == StatusDisqualified {
		return ErrDisqualified
	}
	return nil
}
