package app

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"stockastic/api/internal/auth"
	"stockastic/api/internal/dto"
	"stockastic/api/internal/engine"
	"stockastic/api/internal/ids"
	"stockastic/api/internal/money"
	"stockastic/api/internal/store"
)

// ---- recent trades per team ----

const maxRecentFills = 100

func (a *App) recordFill(f engine.Fill) {
	a.fillMu.Lock()
	defer a.fillMu.Unlock()
	add := func(id string) {
		l := append(a.recentFills[id], f)
		if len(l) > maxRecentFills {
			l = append(l[:0], l[len(l)-maxRecentFills:]...)
		}
		a.recentFills[id] = l
	}
	add(f.TakerAccountID)
	if f.MakerAccountID != f.TakerAccountID {
		add(f.MakerAccountID)
	}
}

func (a *App) fillsOf(id string) []dto.Fill {
	a.fillMu.Lock()
	src := append([]engine.Fill(nil), a.recentFills[id]...)
	a.fillMu.Unlock()
	out := make([]dto.Fill, 0, len(src))
	for i := len(src) - 1; i >= 0; i-- { // newest first
		out = append(out, dto.FromFill(src[i]))
	}
	return out
}

// ---- a team's wallet, as the organiser sees it ----

type TeamDetail struct {
	Account AdminAccount `json:"account"`
	Wallet  struct {
		Cash      float64 `json:"cash"`
		Reserved  float64 `json:"reserved"`
		Available float64 `json:"available"`
		NetWorth  float64 `json:"netWorth"`
	} `json:"wallet"`
	Holdings []dto.Holding `json:"holdings"`
	Orders   []dto.Order   `json:"orders"`
	Fills    []dto.Fill    `json:"fills"`
	History  []AuditEntry  `json:"history"`
}

func (a *App) Team(id string) (TeamDetail, error) {
	u, ok := a.users.get(id)
	if !ok || u.IsAdmin {
		return TeamDetail{}, ErrUnknownUser
	}
	var d TeamDetail
	d.Account = AdminAccount{ID: u.ID, DisplayName: u.DisplayName, Email: u.Email, Role: u.Role, Status: u.Status, Warnings: u.Warnings, Locked: u.Locked}
	d.Account.Sockets = a.Hub.Sockets(id)
	d.Account.Online, d.Account.LastSeen = a.presenceOf(id)
	pf, err := a.Portfolio(u)
	if err != nil {
		return TeamDetail{}, err
	}
	snap, _ := a.Ledger.Snapshot(id)
	d.Wallet.Cash, d.Wallet.Reserved, d.Wallet.Available, d.Wallet.NetWorth =
		pf.CashBalance, dto.Rupees(snap.ReservedCash), dto.Rupees(snap.AvailableCash), pf.TotalValue
	d.Account.CashBalance, d.Account.PortfolioValue = pf.CashBalance, pf.TotalValue
	d.Holdings = pf.Holdings
	for _, o := range a.LiveOrders(id) {
		d.Orders = append(d.Orders, dto.FromOrder(o))
	}
	if d.Orders == nil {
		d.Orders = []dto.Order{}
	}
	d.Fills = a.fillsOf(id)
	d.History = []AuditEntry{}
	for _, e := range a.AuditLog() {
		if e.Target == u.DisplayName || strings.HasPrefix(e.Target, u.DisplayName+" ") || strings.Contains(e.Target, u.ID) {
			d.History = append(d.History, e)
		}
	}
	return d, nil
}

func (a *App) team(id string) (User, error) {
	u, ok := a.users.get(id)
	if !ok || u.IsAdmin {
		return User{}, ErrUnknownUser
	}
	return u, nil
}

// ---- wallet corrections ----

// CashAdjustment is a credit (positive) or debit (negative) to a team's cash, in paise.
type CashAdjustment struct {
	AccountID string `json:"accountId"`
	Delta     int64  `json:"delta"`
}

const maxAdjustRupees = 100_000_000

// AdjustCash credits or debits a team's cash. A debit cannot reach into cash held back for its working
// orders. The change is applied, stored, and audited; if it cannot be stored it is undone.
func (a *App) AdjustCash(actor User, reason, id string, rupees float64) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	if math.IsNaN(rupees) || math.IsInf(rupees, 0) || math.Abs(rupees) > maxAdjustRupees {
		return bad("invalid_amount", "Enter an amount up to ₹10 crore.")
	}
	delta := dto.Paise(rupees)
	if delta == 0 {
		return bad("invalid_amount", "Enter an amount other than zero.")
	}
	return a.Do(actor, fmt.Sprintf("Adjusted cash by ₹%+.2f", dto.Rupees(delta)), u.DisplayName, reason, func() error {
		return a.applyCash(id, delta)
	})
}

// applyCash changes a team's cash and stores the change; if it cannot be stored it is undone.
func (a *App) applyCash(id string, delta money.Paise) error {
	if err := a.Ledger.AdjustCash(id, delta, false); err != nil {
		return err
	}
	if err := a.wal.Append(store.KindCash, CashAdjustment{AccountID: id, Delta: int64(delta)}); err != nil {
		_ = a.Ledger.AdjustCash(id, -delta, true)
		return err
	}
	return nil
}

// SetCash sets a team's cash to an exact amount. It works out the difference from what the team holds at
// that moment; the difference cannot reach into cash held back for working orders.
func (a *App) SetCash(actor User, reason, id string, rupees float64) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	if math.IsNaN(rupees) || math.IsInf(rupees, 0) || rupees < 0 || rupees > maxAdjustRupees {
		return bad("invalid_amount", "Enter an amount from 0 up to ₹10 crore.")
	}
	target := dto.Paise(rupees)
	return a.Do(actor, fmt.Sprintf("Set cash to ₹%.2f", dto.Rupees(target)), u.DisplayName, reason, func() error {
		snap, err := a.Ledger.Snapshot(id)
		if err != nil {
			return err
		}
		if delta := target - snap.Cash; delta != 0 {
			return a.applyCash(id, delta)
		}
		return nil
	})
}

// RevokeShares takes shares back from a team at its average cost. Shares held back for a working sell
// order cannot be taken.
func (a *App) RevokeShares(actor User, reason, id, symbol string, qty int64) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	if !a.HasSymbol(symbol) {
		return engine.ErrUnknownSymbol
	}
	if qty < 1 || qty > maxQty {
		return bad("invalid_quantity", "Quantity must be a whole number of at least 1.")
	}
	return a.Do(actor, fmt.Sprintf("Took back %d x %s", qty, symbol), u.DisplayName, reason, func() error {
		return a.takeShares(id, symbol, qty)
	})
}

func (a *App) takeShares(id, symbol string, qty int64) error {
	var avg int64
	if snap, err := a.Ledger.Snapshot(id); err == nil {
		for _, p := range snap.Positions {
			if p.Symbol == symbol && p.Qty > 0 {
				avg = int64(p.Cost) / p.Qty
			}
		}
	}
	if err := a.Ledger.Revoke(id, symbol, qty, false); err != nil {
		return err
	}
	if err := a.wal.Append(store.KindGrant, Grant{AccountID: id, Symbol: symbol, Qty: -qty}); err != nil {
		_ = a.Ledger.Grant(id, symbol, qty, money.Paise(avg))
		return err
	}
	return nil
}

func (a *App) giveShares(id, symbol string, qty int64, price money.Paise) error {
	if err := a.wal.Append(store.KindGrant, Grant{AccountID: id, Symbol: symbol, Qty: qty, Price: price}); err != nil {
		return err
	}
	return a.Ledger.Grant(id, symbol, qty, price)
}

// SetShares sets a team's holding of one company to an exact number of shares. Shares added are valued at
// the company's last price; shares removed come out at the team's average cost.
func (a *App) SetShares(actor User, reason, id, symbol string, qty int64) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	if !a.HasSymbol(symbol) {
		return engine.ErrUnknownSymbol
	}
	if qty < 0 || qty > maxQty {
		return bad("invalid_quantity", "Enter a whole number of shares from 0 up.")
	}
	return a.Do(actor, fmt.Sprintf("Set %s holding to %d", symbol, qty), u.DisplayName, reason, func() error {
		snap, err := a.Ledger.Snapshot(id)
		if err != nil {
			return err
		}
		var cur int64
		for _, p := range snap.Positions {
			if p.Symbol == symbol {
				cur = p.Qty
			}
		}
		switch {
		case qty > cur:
			price, _ := a.Market.Last(symbol)
			return a.giveShares(id, symbol, qty-cur, price)
		case qty < cur:
			return a.takeShares(id, symbol, cur-qty)
		}
		return nil
	})
}

// ---- orders, standing, credentials ----

// CancelTeamOrders cancels one working order (when orderID is given) or all of a team's working orders.
func (a *App) CancelTeamOrders(ctx context.Context, actor User, reason, id, orderID string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	action, target := "Cancelled all working orders", u.DisplayName
	if orderID != "" {
		action = "Cancelled a working order"
	}
	return a.Do(actor, action, target, reason, func() error {
		orders := a.LiveOrders(id)
		found := false
		var firstErr error
		for _, o := range orders {
			if orderID != "" && o.ID != orderID {
				continue
			}
			found = true
			if _, err := a.Engine.Cancel(ctx, o.Symbol, o.ID, id); err != nil && !errors.Is(err, engine.ErrAlreadyClosed) && firstErr == nil {
				firstErr = err
			}
		}
		if orderID != "" && !found {
			return engine.ErrNotFound
		}
		return firstErr
	})
}

// Reinstate lets a disqualified team trade again. Its cancelled orders are not brought back.
func (a *App) Reinstate(actor User, reason, id string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	return a.Do(actor, "Reinstated", u.DisplayName, reason, func() error {
		_, err := a.updateUser(id, func(x *User) error {
			x.Status = StatusActive
			if x.Warnings > 0 {
				x.Status = StatusWarned
			}
			return nil
		})
		return err
	})
}

// ResetPassword sets a new password for a team. The password itself is never written to the audit log.
func (a *App) ResetPassword(actor User, reason, id, password string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	if len(password) < 8 || len(password) > 72 {
		return bad("invalid_password", "Password must be 8 to 72 characters.")
	}
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	return a.Do(actor, "Reset password", u.DisplayName, reason, func() error {
		_, err := a.updateUser(id, func(x *User) error { x.PasswordHash = hash; return nil })
		return err
	})
}

// SetRole moves a team between investor and fund manager, in either direction.
func (a *App) SetRole(actor User, reason, id, role string) error {
	u, err := a.team(id)
	if err != nil {
		return err
	}
	if role != RoleInvestor && role != RoleFundManager {
		return bad("invalid_role", "Role must be investor or fund_manager.")
	}
	return a.Do(actor, "Changed role to "+role, u.DisplayName, reason, func() error {
		_, err := a.updateUser(id, func(x *User) error { x.Role = role; return nil })
		return err
	})
}

// ---- event: announcements and company pauses ----

type Announcement struct {
	ID   string    `json:"id"`
	Text string    `json:"text"`
	At   time.Time `json:"at"`
	By   string    `json:"by"`
}

// Announce shows a message on every participant's screen.
func (a *App) Announce(actor User, reason, text string) error {
	text = strings.TrimSpace(text)
	if n := len([]rune(text)); n < 3 || n > 280 {
		return bad("invalid_announcement", "An announcement is 3 to 280 characters.")
	}
	return a.Do(actor, "Announced to everyone", text, reason, func() error {
		n := Announcement{ID: ids.New(), Text: text, At: a.now(), By: actor.DisplayName}
		if err := a.wal.Append(store.KindAnnounce, n); err != nil {
			return err
		}
		a.annMu.Lock()
		a.announcements = append(a.announcements, n)
		a.annMu.Unlock()
		a.Hub.ToAll("news", dto.NewsItem{ID: n.ID, Kind: "notice", Headline: n.Text, CreatedAt: dto.MS(n.At)})
		return nil
	})
}

// SymbolPause records that trading in one company is paused or resumed.
type SymbolPause struct {
	Symbol string `json:"symbol"`
	Paused bool   `json:"paused"`
}

func (a *App) symbolPaused(symbol string) bool {
	a.pauseMu.RLock()
	defer a.pauseMu.RUnlock()
	return a.paused[symbol]
}

// PausedSymbols lists the companies whose trading is paused, sorted.
func (a *App) PausedSymbols() []string {
	a.pauseMu.RLock()
	out := make([]string, 0, len(a.paused))
	for s := range a.paused {
		out = append(out, s)
	}
	a.pauseMu.RUnlock()
	sort.Strings(out)
	return out
}

// PauseSymbol stops (or resumes) new orders in one company. Working orders stay in the book and can
// still be cancelled.
func (a *App) PauseSymbol(actor User, reason, symbol string, paused bool) error {
	if !a.HasSymbol(symbol) {
		return engine.ErrUnknownSymbol
	}
	action := "Resumed trading in a company"
	if paused {
		action = "Paused trading in a company"
	}
	return a.Do(actor, action, symbol, reason, func() error {
		if err := a.wal.Append(store.KindPause, SymbolPause{Symbol: symbol, Paused: paused}); err != nil {
			return err
		}
		a.pauseMu.Lock()
		if paused {
			a.paused[symbol] = true
		} else {
			delete(a.paused, symbol)
		}
		a.pauseMu.Unlock()
		a.broadcastControl()
		return nil
	})
}
