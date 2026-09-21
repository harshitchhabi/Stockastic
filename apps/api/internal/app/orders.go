package app

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"stockastic/api/internal/dto"
	"stockastic/api/internal/engine"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/money"
)

var (
	ErrMarketClosed = errors.New("market_closed")
	ErrNoAccount    = errors.New("no_trading_account")
)

// RateLimited is returned when an account has used its trades for the current window.
type RateLimited struct{ RetryAfter time.Duration }

func (e *RateLimited) Error() string { return "rate_limited" }

// OrderRequest is a new order as the API receives it. Prices are rupees, as on the wire.
type OrderRequest struct {
	ClientOrderID string  `json:"clientOrderId"`
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"`
	Type          string  `json:"type"`
	TimeInForce   string  `json:"timeInForce"`
	Price         float64 `json:"price"`
	Qty           int64   `json:"qty"`
}

const (
	maxQty      = 10_000_000
	maxPriceRs  = 10_000_000.0
	maxClientID = 64
)

func (r OrderRequest) toEngine(account string) (engine.NewOrder, error) {
	n := engine.NewOrder{ClientOrderID: strings.TrimSpace(r.ClientOrderID), AccountID: account, Symbol: r.Symbol, Qty: r.Qty}
	if n.ClientOrderID == "" || len(n.ClientOrderID) > maxClientID {
		return n, bad("invalid_client_order_id", "Every order needs a client order id of up to 64 characters.")
	}
	switch strings.ToLower(r.Side) {
	case "buy":
		n.Side = engine.Buy
	case "sell":
		n.Side = engine.Sell
	default:
		return n, bad("invalid_side", "Side must be buy or sell.")
	}
	switch strings.ToLower(r.Type) {
	case "", "limit":
		n.Type = engine.TypeLimit
	case "market":
		n.Type = engine.TypeMarket
	default:
		return n, bad("invalid_type", "Order type must be limit or market.")
	}
	switch strings.ToLower(r.TimeInForce) {
	case "":
	case "gtc":
		n.TIF = engine.GTC
	case "ioc":
		n.TIF = engine.IOC
	case "fok":
		n.TIF = engine.FOK
	default:
		return n, bad("invalid_time_in_force", "Time in force must be gtc, ioc or fok.")
	}
	if math.IsNaN(r.Price) || math.IsInf(r.Price, 0) || r.Price <= 0 || r.Price > maxPriceRs {
		return n, bad("invalid_price", "Enter a price greater than zero.")
	}
	n.Price = money.FromRupees(r.Price)
	if n.Price <= 0 {
		return n, bad("invalid_price", "Enter a price of at least one paisa.")
	}
	if n.Qty < 1 || n.Qty > maxQty {
		return n, bad("invalid_quantity", "Quantity must be a whole number of at least 1.")
	}
	return n, nil
}

// PlaceOrder runs the full path for one order: who may trade, whether the market is open, the rate
// limit, cash/holdings cover, then matching. Every check that rejects an order leaves nothing behind.
func (a *App) PlaceOrder(ctx context.Context, u User, req OrderRequest) (engine.Result, error) {
	if err := u.orderable(); err != nil {
		return engine.Result{}, err
	}
	n, err := req.toEngine(u.ID)
	if err != nil {
		return engine.Result{}, err
	}
	if _, ok := a.companies[n.Symbol]; !ok {
		return engine.Result{}, engine.ErrUnknownSymbol
	}
	if a.Clock.Overrides().Frozen || a.Engine.IsFrozen() {
		return engine.Result{}, engine.ErrFrozen
	}
	if !a.Clock.MarketOpen() {
		return engine.Result{}, ErrMarketClosed
	}

	// A repeat of an order we already processed is a retry: it returns the original result and must
	// not use up another of the account's trades.
	seen := a.Engine.Seen(u.ID, n.ClientOrderID)
	counted, reserved := false, false
	if !seen {
		ok, retry := a.Limiter.Allow(u.ID, a.now())
		if !ok {
			return engine.Result{}, &RateLimited{RetryAfter: retry}
		}
		counted = true
		created, err := a.Ledger.Reserve(n)
		if err != nil {
			a.Limiter.Refund(u.ID)
			if errors.Is(err, ledger.ErrUnknownAccount) {
				return engine.Result{}, ErrNoAccount
			}
			return engine.Result{}, err
		}
		reserved = created
	}

	res, err := a.Engine.Submit(ctx, n)
	if err != nil {
		if reserved {
			a.Ledger.Release(u.ID, n.ClientOrderID)
		}
		if counted {
			a.Limiter.Refund(u.ID)
		}
		return engine.Result{}, err
	}
	if res.Deduped {
		// Lost a race with a concurrent identical request: nothing new was placed.
		if counted {
			a.Limiter.Refund(u.ID)
		}
		if reserved {
			a.Ledger.Release(u.ID, n.ClientOrderID)
		}
	}
	return res, nil
}

// CancelOrder cancels one of the account's own working orders.
func (a *App) CancelOrder(ctx context.Context, u User, symbol, orderID string) (engine.Result, error) {
	if err := u.orderable(); err != nil {
		return engine.Result{}, err
	}
	return a.Engine.Cancel(ctx, symbol, orderID, u.ID)
}

// ---- reads ----

// Companies lists every company with its last and opening price.
func (a *App) Companies() []dto.Company {
	out := make([]dto.Company, 0, len(a.symbols))
	for _, s := range a.symbols {
		c := a.companies[s]
		d := dto.Company{Symbol: c.Symbol, DisplayName: c.Name, Sector: c.Sector}
		if p, ok := a.Market.Last(s); ok {
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

// Account is a user's profile with spendable cash (cash not held back for working orders).
func (a *App) Account(u User) dto.Account {
	d := dto.Account{ID: u.ID, DisplayName: u.DisplayName, Email: u.Email, Role: u.Role, IsAdmin: u.IsAdmin}
	if snap, err := a.Ledger.Snapshot(u.ID); err == nil {
		d.CashBalance = dto.Rupees(snap.AvailableCash)
	}
	return d
}

// Portfolio marks holdings at the last traded price; a company that has not traded is carried at cost.
func (a *App) Portfolio(u User) (dto.Portfolio, error) {
	snap, err := a.Ledger.Snapshot(u.ID)
	if err != nil {
		return dto.Portfolio{}, ErrNoAccount
	}
	p := dto.Portfolio{AccountID: u.ID, CashBalance: dto.Rupees(snap.Cash), Holdings: []dto.Holding{}}
	total := snap.Cash
	for _, pos := range snap.Positions {
		mv := pos.Cost
		if px, ok := a.Market.Traded(pos.Symbol); ok {
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
	for _, u := range a.users.all() {
		if u.IsAdmin || u.Status == StatusDisqualified {
			continue
		}
		v, err := a.Ledger.DirectValue(u.ID, a.Market.Traded)
		if err != nil {
			continue
		}
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

// Depth is a company's published order book.
func (a *App) Depth(symbol string) (dto.Depth, error) {
	d, err := a.Engine.Depth(symbol)
	if err != nil {
		return dto.Depth{}, err
	}
	return dto.FromDepth(d), nil
}
