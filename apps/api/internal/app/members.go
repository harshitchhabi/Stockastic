package app

import (
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"net/mail"
	"sort"
	"strings"
	"sync"
	"time"

	"stockastic/api/internal/auth"
	"stockastic/api/internal/dto"
	"stockastic/api/internal/ids"
	"stockastic/api/internal/store"
	"stockastic/api/internal/wsapi"
)

// Team members. A team is still one account with one portfolio (the rulebook's own definition), and the team
// account's login belongs to the person who registered it: the team leader. Every other person in the team joins
// with the team's code and gets a login of their own that sees the team's dashboard. Only one login per team may
// trade or move the team's money: the leader, unless the leader or an organiser hands that to a teammate.
//
// This sits on top of the rest of the platform without changing it: trades, the ledger and the funds still only
// know the team account. With rulebook teams.investorTeamSize at 1, nobody can join and the platform behaves as if
// this file did not exist.

// Member is one person's own login inside a team.
type Member struct {
	ID             string
	TeamID         string
	Email          string
	Name           string
	PasswordHash   string
	Locked         bool
	Removed        bool
	SessionVersion int
	CreatedAt      time.Time
}

const memberPrefix = "m-"

var (
	ErrBadTeamCode      = errors.New("wrong_team_code")
	ErrTeamFull         = errors.New("team_full")
	ErrNotTeamTrader    = errors.New("not_team_trader")
	ErrUnknownMember    = errors.New("unknown_member")
	joinCodeAlphabet    = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789" // no 0/O or 1/I, so a code read aloud is not misheard
	joinCodeLength      = 8
	memberTokenVerShift = 20
)

type memberStore struct {
	mu      sync.RWMutex
	byID    map[string]*Member
	byEmail map[string]*Member
	canon   map[string]string // emailKey -> member id
}

func newMemberStore() *memberStore {
	return &memberStore{byID: map[string]*Member{}, byEmail: map[string]*Member{}, canon: map[string]string{}}
}

// put stores the latest version of a member. A removed member keeps its record (so its logins stay dead) but
// frees its email.
func (s *memberStore) put(m Member) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.byID[m.ID]; ok {
		delete(s.byEmail, normEmail(old.Email))
		if s.canon[emailKey(old.Email)] == m.ID {
			delete(s.canon, emailKey(old.Email))
		}
	}
	c := m
	s.byID[m.ID] = &c
	if !m.Removed {
		s.byEmail[normEmail(m.Email)] = &c
		s.canon[emailKey(m.Email)] = m.ID
	}
}

// clear deletes every teammate.
func (s *memberStore) clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID, s.byEmail, s.canon = map[string]*Member{}, map[string]*Member{}, map[string]string{}
}

func (s *memberStore) get(id string) (Member, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.byID[id]
	if !ok {
		return Member{}, false
	}
	return *m, true
}

func (s *memberStore) byEmailAddr(email string) (Member, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.byEmail[normEmail(email)]
	if !ok {
		return Member{}, false
	}
	return *m, true
}

func (s *memberStore) emailUsed(email string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.canon[emailKey(email)]
	return ok
}

// ofTeam lists a team's current members, oldest first.
func (s *memberStore) ofTeam(teamID string) []Member {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []Member
	for _, m := range s.byID {
		if m.TeamID == teamID && !m.Removed {
			out = append(out, *m)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (a *App) persistMember(m Member) error { return a.wal.Append(store.KindMember, m) }

// updateMember changes a member durably: stored first, then visible.
func (a *App) updateMember(id string, f func(*Member) error) (Member, error) {
	a.memberMu.Lock()
	defer a.memberMu.Unlock()
	cur, ok := a.members.get(id)
	if !ok {
		return Member{}, ErrUnknownMember
	}
	if err := f(&cur); err != nil {
		return Member{}, err
	}
	if err := a.persistMember(cur); err != nil {
		return Member{}, err
	}
	a.members.put(cur)
	return cur, nil
}

// TeamSize is how many people a team may have: the leader plus teammates who join.
func (a *App) TeamSize() int {
	if n := a.RB.Teams.InvestorTeamSize; n > 1 {
		return n
	}
	return 1
}

func newJoinCode() (string, error) {
	b := make([]byte, joinCodeLength)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	for i := range b {
		b[i] = joinCodeAlphabet[int(b[i])%len(joinCodeAlphabet)]
	}
	return string(b), nil
}

func normCode(c string) string {
	return strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(strings.TrimSpace(c), " ", ""), "-", ""))
}

// teamByCode finds the team a join code belongs to.
func (a *App) teamByCode(code string) (User, bool) {
	code = normCode(code)
	if len(code) != joinCodeLength {
		return User{}, false
	}
	for _, u := range a.users.all() {
		if !u.IsAdmin && u.JoinCode != "" && subtle.ConstantTimeCompare([]byte(u.JoinCode), []byte(code)) == 1 {
			return u, true
		}
	}
	return User{}, false
}

// ensureJoinCode gives a team a join code if it has none (teams registered before members existed).
func (a *App) ensureJoinCode(teamID string) (User, error) {
	u, err := a.team(teamID)
	if err != nil || u.JoinCode != "" {
		return u, err
	}
	return a.newTeamCode(teamID)
}

func (a *App) newTeamCode(teamID string) (User, error) {
	for tries := 0; tries < 10; tries++ {
		code, err := newJoinCode()
		if err != nil {
			return User{}, err
		}
		if _, taken := a.teamByCode(code); taken {
			continue
		}
		return a.updateUser(teamID, func(x *User) error { x.JoinCode = code; return nil })
	}
	return User{}, errors.New("app: could not make a unique team code")
}

// JoinTeam registers a teammate with the team code their leader shared. The event code, the approved-email list and
// one mailbox one person apply. Closed registration does not: it stops new teams, but a teammate who arrives late can
// still join a team that already exists and has room.
func (a *App) JoinTeam(code, name, email, password, eventCode string) (Login, error) {
	if a.TeamSize() < 2 {
		return Login{}, bad("joining_off", "Each team has a single login in this event.")
	}
	if ec := a.SignupCode(); ec != "" && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(eventCode)), []byte(ec)) != 1 {
		return Login{}, ErrBadEventCode
	}
	if !a.emailAllowed(email) {
		return Login{}, ErrNotOnList
	}
	display, ok := cleanName(name)
	if n := len([]rune(display)); !ok || n < 2 || n > 40 {
		return Login{}, bad("invalid_display_name", "Use 2 to 40 letters, numbers or common symbols for your name.")
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(email))
	if err != nil || len(addr.Address) > 254 {
		return Login{}, bad("invalid_email", "Enter a valid email address.")
	}
	if password != a.externalPW && (len(password) < 8 || len(password) > 72) {
		return Login{}, bad("invalid_password", "Password must be 8 to 72 characters.")
	}
	team, found := a.teamByCode(code)
	if !found {
		return Login{}, ErrBadTeamCode
	}
	if team.Status == StatusDisqualified || team.Locked {
		return Login{}, ErrAccountLocked
	}
	hash, err := a.passwordHash(password)
	if errors.Is(err, auth.ErrBusy) {
		return Login{}, ErrBusy
	}
	if err != nil {
		return Login{}, err
	}
	m := Member{ID: memberPrefix + ids.New(), TeamID: team.ID, Email: normEmail(addr.Address), Name: display,
		PasswordHash: hash, CreatedAt: a.now()}

	// One lock for every new login, so a teammate and a new team cannot claim the same mailbox at once, and a team
	// cannot grow past its size when several teammates join together.
	a.emailMu.Lock()
	defer a.emailMu.Unlock()
	if _, taken := a.users.byEmailAddr(m.Email); taken || a.users.emailKeyUsed(m.Email) || a.members.emailUsed(m.Email) {
		return Login{}, ErrEmailTaken
	}
	if len(a.members.ofTeam(team.ID))+1 >= a.TeamSize() {
		return Login{}, ErrTeamFull
	}
	a.memberMu.Lock()
	err = a.persistMember(m)
	if err == nil {
		a.members.put(m)
	}
	a.memberMu.Unlock()
	if err != nil {
		return Login{}, err
	}
	a.Hub.ToAccount(team.ID, "portfolio", map[string]any{"reason": "team"})
	return Login{Team: team, Member: &m}, nil
}

// Login is who is signed in: always a team (or an organiser), and, for a teammate, which person.
type Login struct {
	Team   User
	Member *Member
}

// Name is the person's own name: the teammate's, or the leader's (the team's name if the leader never gave one).
func (l Login) Name() string {
	if l.Member != nil {
		return l.Member.Name
	}
	if l.Team.LeaderName != "" {
		return l.Team.LeaderName
	}
	return l.Team.DisplayName
}

// Authenticate checks an email and password against team logins and teammates' logins.
func (a *App) Authenticate(email, password string) (Login, error) {
	if _, ok := a.users.byEmailAddr(email); ok {
		u, err := a.Login(email, password)
		return Login{Team: u}, err
	}
	m, ok := a.members.byEmailAddr(email)
	if !ok {
		return Login{}, ErrInvalidCredentials // no password work for an unknown address
	}
	okPw, err := auth.TryCheckPassword(m.PasswordHash, password)
	if err != nil {
		return Login{}, ErrBusy
	}
	if !okPw {
		return Login{}, ErrInvalidCredentials
	}
	return a.memberLogin(m)
}

func (a *App) memberLogin(m Member) (Login, error) {
	team, ok := a.users.get(m.TeamID)
	if !ok {
		return Login{}, ErrInvalidCredentials
	}
	if m.Locked || team.Locked {
		return Login{}, ErrAccountLocked
	}
	return Login{Team: team, Member: &m}, nil
}

// memberVer ties a teammate's login to both their own session and their team's: signing out the whole team (or
// locking it) ends every teammate's login too.
func memberVer(team User, m Member) int {
	return team.SessionVersion<<memberTokenVerShift | (m.SessionVersion & (1<<memberTokenVerShift - 1))
}

// IssueToken makes the sign-in token for a login.
func (a *App) IssueToken(l Login) (string, error) {
	if l.Member != nil {
		return a.Signer.Issue(l.Member.ID, memberVer(l.Team, *l.Member))
	}
	return a.Signer.Issue(l.Team.ID, l.Team.SessionVersion)
}

// Resolve turns a verified token's subject and version into the current login, or false if it is no longer valid.
func (a *App) Resolve(id string, ver int) (Login, bool) {
	if strings.HasPrefix(id, memberPrefix) {
		m, ok := a.members.get(id)
		if !ok || m.Removed || m.Locked {
			return Login{}, false
		}
		team, ok := a.users.get(m.TeamID)
		if !ok || team.Locked || ver != memberVer(team, m) {
			return Login{}, false
		}
		return Login{Team: team, Member: &m}, true
	}
	u, ok := a.users.get(id)
	if !ok || !u.sessionOK(ver) {
		return Login{}, false
	}
	return Login{Team: u}, true
}

// CanTrade says whether this login may trade or move the team's money. Organisers are not limited here.
func (a *App) CanTrade(l Login) bool {
	if l.Team.IsAdmin {
		return true
	}
	if l.Team.TraderMember == "" {
		return l.Member == nil
	}
	return l.Member != nil && l.Member.ID == l.Team.TraderMember
}

// traderName is who trades for a team, for showing on screen.
func (a *App) traderName(team User) string {
	if team.TraderMember != "" {
		if m, ok := a.members.get(team.TraderMember); ok && !m.Removed {
			return m.Name
		}
	}
	if team.LeaderName != "" {
		return team.LeaderName
	}
	return "the team leader"
}

// ---- what a team sees ----

type TeamMemberView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Email    string `json:"email,omitempty"` // only the leader and organisers see teammates' emails
	Trader   bool   `json:"trader"`
	JoinedAt int64  `json:"joinedAt"`
	Locked   bool   `json:"locked,omitempty"`
	Online   bool   `json:"online"`
}

type TeamView struct {
	LeaderOnline bool             `json:"leaderOnline"`
	TeamName     string           `json:"teamName"`
	LeaderName   string           `json:"leaderName"`
	IsLeader     bool             `json:"isLeader"`
	YouTrade     bool             `json:"youTrade"`
	LeaderTrades bool             `json:"leaderTrades"`
	TraderName   string           `json:"traderName"`
	JoinCode     string           `json:"joinCode,omitempty"` // only the leader sees it
	TeamSize     int              `json:"teamSize"`
	Members      []TeamMemberView `json:"members"`
}

func (a *App) teamMembersView(team User, withEmails bool) []TeamMemberView {
	out := []TeamMemberView{}
	on := a.Hub.LoginsOnline(team.ID)
	for _, m := range a.members.ofTeam(team.ID) {
		v := TeamMemberView{ID: m.ID, Name: m.Name, Trader: team.TraderMember == m.ID, JoinedAt: m.CreatedAt.UnixMilli(), Locked: m.Locked, Online: on[m.ID] > 0}
		if withEmails {
			v.Email = m.Email
		}
		out = append(out, v)
	}
	return out
}

// MyTeam is the team page for a signed-in participant.
func (a *App) MyTeam(l Login) (TeamView, error) {
	team := l.Team
	leader := l.Member == nil
	if leader && a.TeamSize() > 1 {
		var err error
		if team, err = a.ensureJoinCode(team.ID); err != nil {
			return TeamView{}, err
		}
	}
	v := TeamView{LeaderOnline: a.Hub.LoginsOnline(team.ID)[""] > 0, TeamName: team.DisplayName, LeaderName: team.LeaderName, IsLeader: leader, YouTrade: a.CanTrade(Login{Team: team, Member: l.Member}),
		LeaderTrades: team.TraderMember == "", TraderName: a.traderName(team), TeamSize: a.TeamSize(),
		Members: a.teamMembersView(team, leader)}
	if leader {
		v.JoinCode = team.JoinCode
	}
	return v, nil
}

// NewTeamCode replaces a team's join code (the old one stops working). The leader may do it, as may organisers.
func (a *App) NewTeamCode(l Login) (string, error) {
	if l.Member != nil {
		return "", bad("leader_only", "Only the team leader can change the team code.")
	}
	u, err := a.newTeamCode(l.Team.ID)
	return u.JoinCode, err
}

// SetTrader chooses who trades for a team: memberID "" means the leader. The leader may do it for their team;
// organisers use SetTraderAdmin.
func (a *App) SetTrader(l Login, memberID string) error {
	if l.Member != nil {
		return bad("leader_only", "Only the team leader can choose who trades.")
	}
	return a.setTrader(l.Team.ID, memberID)
}

func (a *App) setTrader(teamID, memberID string) error {
	if memberID != "" {
		m, ok := a.members.get(memberID)
		if !ok || m.Removed || m.TeamID != teamID {
			return ErrUnknownMember
		}
	}
	if _, err := a.updateUser(teamID, func(x *User) error { x.TraderMember = memberID; return nil }); err != nil {
		return err
	}
	a.Hub.ToAccount(teamID, "portfolio", map[string]any{"reason": "team"}) // every teammate's screen updates now
	return nil
}

// ---- organiser controls ----

// TeamMembersAdmin is every member of a team with their emails, and the team's code.
type TeamMembersAdmin struct {
	LeaderOnline bool             `json:"leaderOnline"`
	JoinCode     string           `json:"joinCode"`
	LeaderName   string           `json:"leaderName"`
	TeamSize     int              `json:"teamSize"`
	TraderName   string           `json:"traderName"`
	LeaderTrades bool             `json:"leaderTrades"`
	Members      []TeamMemberView `json:"members"`
}

func (a *App) TeamMembers(teamID string) (TeamMembersAdmin, error) {
	u, err := a.team(teamID)
	if err != nil {
		return TeamMembersAdmin{}, err
	}
	if a.TeamSize() > 1 {
		if u, err = a.ensureJoinCode(teamID); err != nil {
			return TeamMembersAdmin{}, err
		}
	}
	return TeamMembersAdmin{LeaderOnline: a.Hub.LoginsOnline(teamID)[""] > 0, JoinCode: u.JoinCode, LeaderName: u.LeaderName, TeamSize: a.TeamSize(), TraderName: a.traderName(u), LeaderTrades: u.TraderMember == "",
		Members: a.teamMembersView(u, true)}, nil
}

func (a *App) SetTraderAdmin(actor User, reason, teamID, memberID string) error {
	u, err := a.team(teamID)
	if err != nil {
		return err
	}
	who := "the team leader"
	if memberID != "" {
		if m, ok := a.members.get(memberID); ok {
			who = m.Name
		}
	}
	return a.Do(actor, "Chose who trades: "+who, u.DisplayName, reason, func() error { return a.setTrader(teamID, memberID) })
}

func (a *App) memberOf(teamID, memberID string) (Member, error) {
	m, ok := a.members.get(memberID)
	if !ok || m.Removed || m.TeamID != teamID {
		return Member{}, ErrUnknownMember
	}
	return m, nil
}

// RemoveMember takes a teammate out of a team: their login stops working and their place frees up (a
// substitution). If they were trading for the team, the leader trades again.
func (a *App) RemoveMember(actor User, reason, teamID, memberID string) error {
	u, err := a.team(teamID)
	if err != nil {
		return err
	}
	m, err := a.memberOf(teamID, memberID)
	if err != nil {
		return err
	}
	return a.Do(actor, "Removed teammate "+m.Name, u.DisplayName, reason, func() error {
		if u.TraderMember == memberID {
			if err := a.setTrader(teamID, ""); err != nil {
				return err
			}
		}
		if _, err := a.updateMember(memberID, func(x *Member) error { x.Removed = true; x.SessionVersion++; return nil }); err != nil {
			return err
		}
		a.Hub.DisconnectAccount(teamID, wsapi.CloseTooMany, "team changed") // teammates reconnect; the removed person cannot
		return nil
	})
}

// SignOutMember ends one teammate's logins.
func (a *App) SignOutMember(actor User, reason, teamID, memberID string) error {
	u, err := a.team(teamID)
	if err != nil {
		return err
	}
	m, err := a.memberOf(teamID, memberID)
	if err != nil {
		return err
	}
	return a.Do(actor, "Signed out teammate "+m.Name, u.DisplayName, reason, func() error {
		if _, err := a.updateMember(memberID, func(x *Member) error { x.SessionVersion++; return nil }); err != nil {
			return err
		}
		// Their open page loses its live feed at once; teammates' pages reconnect by themselves in a moment.
		a.Hub.DisconnectAccount(teamID, wsapi.CloseTooMany, "team changed")
		return nil
	})
}

// NewTeamCodeAdmin replaces a team's join code for an organiser.
func (a *App) NewTeamCodeAdmin(actor User, reason, teamID string) error {
	u, err := a.team(teamID)
	if err != nil {
		return err
	}
	return a.Do(actor, "New team code", u.DisplayName, reason, func() error {
		_, err := a.newTeamCode(teamID)
		return err
	})
}

// AccountFor is the signed-in person's view of their account: the team's figures, and who they are in it.
func (a *App) AccountFor(l Login) dto.Account {
	d := a.Account(l.Team)
	d.LoginName, d.IsLeader, d.CanTrade = l.Name(), l.Member == nil, a.CanTrade(l)
	d.TraderName, d.TeamSize = a.traderName(l.Team), a.TeamSize()
	d.LoginEmail = l.Team.Email
	if l.Member != nil {
		d.MemberID, d.LoginEmail = l.Member.ID, l.Member.Email
	}
	return d
}

// KnownLogin reports whether an email already signs in here (a team's own login or a teammate's).
func (a *App) KnownLogin(email string) bool {
	if _, ok := a.users.byEmailAddr(email); ok {
		return true
	}
	_, ok := a.members.byEmailAddr(email)
	return ok
}

// EmailAllowed reports whether an email may register (the approved-email list, if the organisers set one).
func (a *App) EmailAllowed(email string) bool { return a.emailAllowed(email) }
