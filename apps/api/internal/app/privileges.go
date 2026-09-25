package app

import (
	"crypto/subtle"
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"stockastic/api/internal/dto"
	"stockastic/api/internal/store"
	"stockastic/api/internal/trading"
	"stockastic/api/internal/wsapi"
)

// ---- removing a team from the event ----

// Eject removes a team from the event in one step: it is disqualified, its account is locked, and every open
// page is sent to the sign-in screen. Its trades so far stand. Readmit undoes it.
func (a *App) Eject(actor User, id string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	return a.Do(actor, "Removed from the event", u.DisplayName, "", func() error {
		if _, err := a.updateUser(id, func(x *User) error {
			x.Status, x.Locked = StatusDisqualified, true
			x.SessionVersion++
			return nil
		}); err != nil {
			return err
		}
		a.Hub.DisconnectAccount(id, wsapi.CloseUnauthenticated, "removed from the event")
		return nil
	})
}

// Readmit lets a removed team back in: the account is unlocked and no longer disqualified.
func (a *App) Readmit(actor User, id string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	return a.Do(actor, "Let back into the event", u.DisplayName, "", func() error {
		_, err := a.updateUser(id, func(x *User) error {
			x.Locked = false
			x.Status = StatusActive
			if x.Warnings > 0 {
				x.Status = StatusWarned
			}
			return nil
		})
		return err
	})
}

// ---- messages to one team ----

// Message shows a private notice to one team's open pages. It is not stored.
func (a *App) Message(actor User, id, text string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	text = strings.TrimSpace(text)
	if n := len([]rune(text)); n < 1 || n > 300 {
		return bad("invalid_message", "Write a message of up to 300 characters.")
	}
	return a.Do(actor, "Sent a private message", u.DisplayName, "", func() error {
		a.Hub.ToAccount(id, "news", dto.NewsItem{ID: "msg-" + strconv.FormatInt(a.now().UnixNano(), 36), Kind: "notice", Headline: text, CreatedAt: dto.MS(a.now())})
		return nil
	})
}

// ---- registration ----

// Setting is one organiser switch that lives in the log.
type Setting struct {
	Key   string `json:"key"`
	Value bool   `json:"value"`
	Text  string `json:"text,omitempty"`
}

const (
	settingSignup = "signup"
	settingCode   = "signupCode"
)

// SignupCode is the event code people must enter to register ("" means none is needed).
func (a *App) SignupCode() string {
	s, _ := a.signupCode.Load().(string)
	return s
}

// CheckSignupCode reports whether the code entered is the event code (or none is needed).
func (a *App) CheckSignupCode(entered string) error {
	if code := a.SignupCode(); code != "" && subtle.ConstantTimeCompare([]byte(strings.TrimSpace(entered)), []byte(code)) != 1 {
		return ErrBadEventCode
	}
	return nil
}

// SetSignupCode sets or clears the event code. Teams that already have accounts are not affected.
func (a *App) SetSignupCode(actor User, code string) error {
	code = strings.TrimSpace(code)
	if len(code) > 40 {
		return bad("invalid_code", "An event code is up to 40 characters.")
	}
	label := "Changed the event code for registration"
	if code == "" {
		label = "Removed the event code for registration"
	}
	return a.Do(actor, label, "registration", "", func() error {
		if err := a.wal.Append(store.KindSetting, Setting{Key: settingCode, Text: code}); err != nil {
			return err
		}
		a.signupCode.Store(code)
		return nil
	})
}

// SignupOpen reports whether new teams may register themselves.
func (a *App) SignupOpen() bool { return a.signupOpen.Load() }

// SetSignup opens or closes self-registration, whatever the server was started with.
func (a *App) SetSignup(actor User, open bool) error {
	label := "Closed registration"
	if open {
		label = "Opened registration"
	}
	return a.Do(actor, label, "registration", "", func() error {
		if err := a.wal.Append(store.KindSetting, Setting{Key: settingSignup, Value: open}); err != nil {
			return err
		}
		a.signupOpen.Store(open)
		return nil
	})
}

// ---- every trade ----

// AdminTrade is one trade as the organiser sees it, with names.
type AdminTrade struct {
	ID        string  `json:"id"`
	At        int64   `json:"at"`
	AccountID string  `json:"accountId"`
	Team      string  `json:"team"`
	Symbol    string  `json:"symbol"`
	Side      string  `json:"side"`
	Qty       int64   `json:"qty"`
	Price     float64 `json:"price"`
	Value     float64 `json:"value"`
	Stage     string  `json:"stage"`
}

func (a *App) teamName(account string) string {
	if strings.HasPrefix(account, "fund:") {
		if f, ok := a.Funds.Fund(strings.TrimPrefix(account, "fund:")); ok {
			return f.Profile.Name
		}
	}
	if u, ok := a.users.get(account); ok {
		return u.DisplayName
	}
	return account
}

// AdminTrades lists trades, newest first, optionally for one team (a fund's trades are its account's) and one
// company. limit caps the rows returned (0 means 200).
func (a *App) AdminTrades(account, symbol string, limit int) []AdminTrade {
	if limit <= 0 || limit > 5000 {
		limit = 200
	}
	a.statMu.Lock()
	src := a.allTrades
	a.statMu.Unlock()
	out := []AdminTrade{}
	for i := len(src) - 1; i >= 0 && len(out) < limit; i-- {
		t := src[i]
		if (account != "" && t.AccountID != account && t.AccountID != "fund:"+account) || (symbol != "" && t.Symbol != symbol) {
			continue
		}
		out = append(out, a.adminTrade(t))
	}
	return out
}

func (a *App) adminTrade(t trading.Trade) AdminTrade {
	return AdminTrade{ID: t.ID, At: dto.MS(t.At), AccountID: t.AccountID, Team: a.teamName(t.AccountID), Symbol: t.Symbol, Side: t.Side.String(), Qty: t.Qty,
		Price: dto.Rupees(t.Price), Value: dto.Rupees(t.Notional()), Stage: t.Stage}
}

// ---- exports ----

// csvSafe stops a spreadsheet from running a name as a formula: a cell starting with = + - @ (or a tab or
// carriage return) gets a leading apostrophe. Team names are typed by participants, so this matters.
func csvSafe(s string) string {
	if s != "" && strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return "'" + s
	}
	return s
}

// WriteTradesCSV writes every trade of the event.
func (a *App) WriteTradesCSV(w io.Writer) error {
	a.statMu.Lock()
	src := append([]trading.Trade(nil), a.allTrades...)
	a.statMu.Unlock()
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"time", "team", "account_id", "company", "side", "shares", "price", "value", "stage", "trade_id"})
	for _, t := range src {
		r := a.adminTrade(t)
		_ = cw.Write([]string{t.At.UTC().Format("2006-01-02T15:04:05Z"), csvSafe(r.Team), r.AccountID, r.Symbol, r.Side, strconv.FormatInt(r.Qty, 10),
			strconv.FormatFloat(r.Price, 'f', 2, 64), strconv.FormatFloat(r.Value, 'f', 2, 64), r.Stage, r.ID})
	}
	cw.Flush()
	return cw.Error()
}

// WriteAccountsCSV writes every team with its status, cash and portfolio value, best first.
func (a *App) WriteAccountsCSV(w io.Writer) error {
	rows := a.AdminAccounts()
	sort.Slice(rows, func(i, j int) bool { return rows[i].PortfolioValue > rows[j].PortfolioValue })
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"rank", "team", "email", "role", "status", "warnings", "cash", "portfolio_value", "positions", "online"})
	for i, r := range rows {
		_ = cw.Write([]string{strconv.Itoa(i + 1), csvSafe(r.DisplayName), csvSafe(r.Email), r.Role, r.Status, strconv.Itoa(r.Warnings),
			strconv.FormatFloat(r.CashBalance, 'f', 2, 64), strconv.FormatFloat(r.PortfolioValue, 'f', 2, 64), strconv.Itoa(r.Positions), fmt.Sprint(r.Online)})
	}
	cw.Flush()
	return cw.Error()
}

// ---- automatic news ----

func (a *App) SetNewsManual(actor User, manual bool) error {
	label := "Turned automatic news on"
	if manual {
		label = "Turned automatic news off: every item is released by hand"
	}
	return a.Do(actor, label, "news", "", func() error { return a.Sim.SetNewsManual(manual) })
}

func (a *App) SkipNewsItem(actor User, id string, skip bool) error {
	label := "Held a news item"
	if !skip {
		label = "Let a held news item go out again"
	}
	return a.Do(actor, label, id, "", func() error {
		if err := a.Sim.SetSkipped(id, skip); err != nil {
			return bad("cannot_change", err.Error())
		}
		return nil
	})
}

func (a *App) EditNewsItem(actor User, id, headline string, atMinute *float64) error {
	headline = strings.TrimSpace(headline)
	if len([]rune(headline)) > 300 {
		return bad("invalid_headline", "A headline is up to 300 characters.")
	}
	return a.Do(actor, "Edited a news item", id, "", func() error {
		if err := a.Sim.EditItem(id, headline, atMinute); err != nil {
			return bad("cannot_change", err.Error())
		}
		return nil
	})
}
