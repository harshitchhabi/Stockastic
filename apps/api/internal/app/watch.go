package app

import (
	"strings"

	"stockastic/api/internal/store"
)

// A team's watchlist: the companies it has starred. It belongs to the team, not to one browser, so every teammate
// sees the same list on every device and a change shows on everyone's screen at once. Anyone in the team may change
// it: it moves no money.

// Watchlist is one team's starred companies, as stored.
type Watchlist struct {
	Team    string   `json:"team"`
	Symbols []string `json:"symbols"`
}

// WatchlistOf returns a team's starred companies.
func (a *App) WatchlistOf(team string) []string {
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	out := append([]string{}, a.watch[team]...)
	return out
}

// SetWatchlist replaces a team's starred companies. Unknown companies and repeats are dropped.
func (a *App) SetWatchlist(team string, symbols []string) ([]string, error) {
	seen := map[string]bool{}
	clean := []string{}
	for _, s := range symbols {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" || seen[s] || !a.HasSymbol(s) {
			continue
		}
		seen[s] = true
		clean = append(clean, s)
	}
	a.watchMu.Lock()
	defer a.watchMu.Unlock()
	if err := a.wal.Append(store.KindWatch, Watchlist{Team: team, Symbols: clean}); err != nil {
		return nil, err
	}
	a.watch[team] = clean
	a.Hub.ToAccount(team, "watchlist", map[string]any{"symbols": clean})
	return clean, nil
}

// SetLeaderName records the team leader's own name (the team account's login belongs to them).
func (a *App) SetLeaderName(l Login, name string) error {
	if l.Member != nil {
		return bad("leader_only", "Only the team leader can change the leader's name.")
	}
	n, ok := cleanName(name)
	if r := []rune(n); !ok || len(r) < 2 || len(r) > 40 {
		return bad("invalid_display_name", "Use 2 to 40 letters, numbers or common symbols for your name.")
	}
	if _, err := a.updateUser(l.Team.ID, func(x *User) error { x.LeaderName = n; return nil }); err != nil {
		return err
	}
	a.Hub.ToAccount(l.Team.ID, "portfolio", map[string]any{"reason": "team"})
	return nil
}
