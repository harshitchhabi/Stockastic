package app

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"stockastic/api/internal/dto"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/money"
	"stockastic/api/internal/trading"
)

var (
	ErrMarketClosed = errors.New("market_closed")
	ErrFrozen       = errors.New("trading_frozen")
	ErrNoAccount    = errors.New("no_trading_account")
	ErrSymbolPaused = errors.New("trading_paused")
)

// RateLimited is returned when an account has used its trades for the current window.
type RateLimited struct{ RetryAfter time.Duration }

func (e *RateLimited) Error() string { return "rate_limited" }

// TradeRequest is a trade as the API receives it: buy or sell a number of shares of one company. There
// is no price or order type: it happens at the company's current price.
type TradeRequest struct {
	ClientTradeID string `json:"clientTradeId"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Qty           int64  `json:"qty"`
	// ExpectedPrice, in rupees, is the price the person saw. If the price has moved the trade is refused
	// and they are shown the new price. Leave it out (or 0) to trade at whatever the price is.
	ExpectedPrice float64 `json:"expectedPrice"`
}

const (
	maxQty      = 10_000_000
	maxClientID = 64
)

func (r TradeRequest) toRequest(account string, stage string) (trading.Request, error) {
	q := trading.Request{ClientTradeID: strings.TrimSpace(r.ClientTradeID), AccountID: account, Symbol: r.Symbol, Qty: r.Qty, Stage: stage}
	if q.ClientTradeID == "" || len(q.ClientTradeID) > maxClientID {
		return q, bad("invalid_client_trade_id", "Every trade needs a client trade id of up to 64 characters.")
	}
	switch strings.ToLower(r.Side) {
	case "buy":
		q.Side = trading.Buy
	case "sell":
		q.Side = trading.Sell
	default:
		return q, bad("invalid_side", "Choose buy or sell.")
	}
	if r.Qty < 1 || r.Qty > maxQty {
		return q, bad("invalid_quantity", "Quantity must be a whole number of at least 1.")
	}
	if r.ExpectedPrice != 0 {
		if math.IsNaN(r.ExpectedPrice) || math.IsInf(r.ExpectedPrice, 0) || r.ExpectedPrice < 0 {
			return q, bad("invalid_price", "That price is not valid.")
		}
		q.ExpectedPrice = money.FromRupees(r.ExpectedPrice)
	}
	return q, nil
}

// Trade runs one trade end to end: who may trade, whether the market is open, the two-trades-a-minute
// limit, then execution at the current price. Every check that refuses a trade leaves nothing behind, and
// a refused trade does not use up one of the team's trades.
func (a *App) Trade(ctx context.Context, u User, req TradeRequest) (trading.Result, error) {
	if err := u.orderable(); err != nil {
		return trading.Result{}, err
	}
	// A fund manager trades the fund's account, and the whole fund shares one two-trades-a-minute allowance.
	acct, key, members := u.ID, u.ID, []string{u.ID}
	if u.Role == RoleFundManager {
		if f, ok := a.Funds.FundOfMember(u.ID); ok {
			acct, key, members = f.Account, f.ID, f.Members[:]
			if f.Disqualified {
				return trading.Result{}, ErrDisqualified
			}
		}
	}
	tr, err := req.toRequest(acct, string(a.stage()))
	if err != nil {
		return trading.Result{}, err
	}
	if !a.HasSymbol(tr.Symbol) {
		return trading.Result{}, trading.ErrUnknownSymbol
	}
	if a.Clock.Overrides().Frozen {
		return trading.Result{}, ErrFrozen
	}
	if !a.Clock.MarketOpen() {
		return trading.Result{}, ErrMarketClosed
	}
	if a.symbolPaused(tr.Symbol) {
		return trading.Result{}, ErrSymbolPaused
	}

	// A repeat of a trade we already made is a retry: it returns the original and does not use up a trade.
	seen := a.Exec.Seen(acct, tr.ClientTradeID)
	if !seen {
		ok, retry := a.Limiter.Allow(key, a.now())
		if !ok {
			return trading.Result{}, &RateLimited{RetryAfter: retry}
		}
	}
	res, err := a.Exec.Execute(ctx, tr)
	if err != nil {
		if !seen {
			a.Limiter.Refund(key)
		}
		if errors.Is(err, ledger.ErrUnknownAccount) {
			return trading.Result{}, ErrNoAccount
		}
		return trading.Result{}, err
	}
	if res.Deduped {
		if !seen {
			a.Limiter.Refund(key) // lost a race with an identical request: nothing new happened
		}
		return res, nil
	}
	a.recordTrade(res.Trade)
	a.tradesPM.Add(1, a.now())
	a.updatePeakFor(acct)
	for _, m := range members {
		a.Hub.ToAccount(m, "trade", dto.FromTrade(res.Trade))
	}
	return res, nil
}

func (a *App) updatePeakFor(id string) {
	if v, err := a.Ledger.DirectValue(id, a.Market.Price); err == nil {
		a.statMu.Lock()
		if v > a.peaks[id] {
			a.peaks[id] = v
		}
		a.statMu.Unlock()
	}
}

// MyTrades are a team's most recent trades, newest first.
func (a *App) MyTrades(id string) []dto.Trade {
	a.statMu.Lock()
	src := append([]trading.Trade(nil), a.recent[id]...)
	a.statMu.Unlock()
	out := make([]dto.Trade, 0, len(src))
	for i := len(src) - 1; i >= 0; i-- {
		out = append(out, dto.FromTrade(src[i]))
	}
	return out
}

// ---- reads ----

// Companies lists every company with its current and opening price.
func (a *App) Companies() []dto.Company {
	out := make([]dto.Company, 0, len(a.symbols))
	for _, s := range a.symbols {
		c := a.companies[s]
		d := dto.Company{Symbol: c.Symbol, DisplayName: c.Name, Sector: c.Sector}
		if p, ok := a.Market.Price(s); ok {
			v := dto.Rupees(p)
			d.LastPrice = &v
		}
		if p, ok := a.Market.Open(s); ok {
			v := dto.Rupees(p)
			d.OpenPrice = &v
		}
		out = append(out, d)
	}
	return out
}

func (a *App) HasSymbol(s string) bool { _, ok := a.companies[s]; return ok }

func (a *App) History(symbol string) []dto.PricePoint {
	pts := a.Market.History(symbol)
	out := make([]dto.PricePoint, len(pts))
	for i, p := range pts {
		out[i] = dto.PricePoint{Price: dto.Rupees(p.Price), Timestamp: dto.MS(p.At)}
	}
	return out
}

// Account is a user's profile with their cash.
func (a *App) Account(u User) dto.Account {
	d := dto.Account{ID: u.ID, DisplayName: u.DisplayName, Email: u.Email, Role: u.Role, IsAdmin: u.IsAdmin}
	if snap, err := a.Ledger.Snapshot(a.AcctOf(u)); err == nil {
		d.CashBalance = dto.Rupees(snap.Cash)
	}
	return d
}

// Portfolio values holdings at the companies' current prices.
func (a *App) Portfolio(u User) (dto.Portfolio, error) {
	acct := a.AcctOf(u)
	p, err := a.portfolioOf(acct)
	if err != nil {
		return p, err
	}
	if acct != u.ID {
		p.FundID = strings.TrimPrefix(acct, "fund:")
		return p, nil
	}
	if u.Role == RoleInvestor {
		navs := a.navs()
		total := money.FromRupees(p.TotalValue)
		for id, h := range a.Funds.Holdings(u.ID) {
			if h.Units <= 0 {
				continue
			}
			f, _ := a.Funds.Fund(id)
			val := money.FromRupees(h.Units * navs[id])
			total += val
			p.FundPositions = append(p.FundPositions, dto.FundPosition{FundID: id, Name: f.Profile.Name, Units: h.Units, NAV: navs[id],
				Value: dto.Rupees(val), Contributed: dto.Rupees(h.Contributed), Pnl: dto.Rupees(val + h.Redeemed - h.Contributed)})
		}
		sort.Slice(p.FundPositions, func(i, j int) bool { return p.FundPositions[i].FundID < p.FundPositions[j].FundID })
		p.TotalValue = dto.Rupees(total)
	}
	return p, nil
}

// portfolioOf values one ledger account's cash and shares at the current prices.
func (a *App) portfolioOf(id string) (dto.Portfolio, error) {
	snap, err := a.Ledger.Snapshot(id)
	if err != nil {
		return dto.Portfolio{}, ErrNoAccount
	}
	p := dto.Portfolio{AccountID: id, CashBalance: dto.Rupees(snap.Cash), Holdings: []dto.Holding{}, FundPositions: []dto.FundPosition{}}
	total := snap.Cash
	for _, pos := range snap.Positions {
		px, ok := a.Market.Price(pos.Symbol)
		mv := pos.Cost
		if ok {
			mv = px * money.Paise(pos.Qty)
		}
		total += mv
		p.Holdings = append(p.Holdings, dto.Holding{
			Symbol: pos.Symbol, Qty: pos.Qty, AvgPrice: dto.Rupees(pos.Cost) / float64(pos.Qty),
			MarketValue: dto.Rupees(mv), UnrealizedPnl: dto.Rupees(mv - pos.Cost),
		})
	}
	p.TotalValue = dto.Rupees(total)
	return p, nil
}

// Leaderboard is a periodic snapshot (rulebook leaderboard.refreshSeconds), never a live feed.
func (a *App) Leaderboard() []dto.LeaderRow {
	ttl := time.Duration(a.RB.Leaderboard.RefreshSeconds) * time.Second
	a.lbMu.Lock()
	defer a.lbMu.Unlock()
	if a.lbCache != nil && a.now().Sub(a.lbAt) < ttl {
		return a.lbCache
	}
	start := a.RB.StartingCapital()
	rows := make([]dto.LeaderRow, 0)
	navs := a.navs()
	for _, u := range a.users.all() {
		if u.IsAdmin || u.Status == StatusDisqualified {
			continue
		}
		if u.Role == RoleFundManager {
			continue // fund managers are ranked as funds, not as investors
		}
		v := a.totalValue(u.ID, navs)
		ret := 0.0
		if start > 0 {
			ret = float64(v-start) / float64(start) * 100
		}
		rows = append(rows, dto.LeaderRow{AccountID: u.ID, DisplayName: u.DisplayName, PortfolioValue: dto.Rupees(v), PercentReturn: ret})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].PortfolioValue != rows[j].PortfolioValue {
			return rows[i].PortfolioValue > rows[j].PortfolioValue
		}
		return rows[i].DisplayName < rows[j].DisplayName
	})
	for i := range rows {
		rows[i].Rank = i + 1
	}
	a.lbCache, a.lbAt = rows, a.now()
	return rows
}
