// Package dto is the JSON shape the web app and the Go server agree on. Money is held as integer paise
// everywhere inside the server and converted to rupees (a JSON number) only here, at the boundary, so
// the web app's existing rupee amounts keep working and no float ever takes part in accounting.
package dto

import (
	"time"

	"stockastic/api/internal/engine"
	"stockastic/api/internal/money"
)

func Rupees(p money.Paise) float64 { return p.Rupees() }
func Paise(r float64) money.Paise  { return money.FromRupees(r) }
func ms(t time.Time) int64         { return t.UnixMilli() }

type Account struct {
	ID          string  `json:"id"`
	DisplayName string  `json:"displayName"`
	Email       string  `json:"email"`
	Role        string  `json:"role"`
	IsAdmin     bool    `json:"isAdmin"`
	CashBalance float64 `json:"cashBalance"`
}

type Company struct {
	Symbol      string   `json:"symbol"`
	DisplayName string   `json:"displayName"`
	Sector      string   `json:"sector,omitempty"`
	LastPrice   *float64 `json:"lastPrice"`
	OpenPrice   *float64 `json:"openPrice,omitempty"`
}

type Level struct {
	Price      float64 `json:"price"`
	Qty        int64   `json:"qty"`
	OrderCount int     `json:"orderCount"`
}

type Depth struct {
	Symbol string  `json:"symbol"`
	Bids   []Level `json:"bids"`
	Asks   []Level `json:"asks"`
}

func FromDepth(d engine.Depth) Depth {
	conv := func(in []engine.Level) []Level {
		out := make([]Level, len(in))
		for i, l := range in {
			out[i] = Level{Price: Rupees(l.Price), Qty: l.Qty, OrderCount: l.Orders}
		}
		return out
	}
	return Depth{Symbol: d.Symbol, Bids: conv(d.Bids), Asks: conv(d.Asks)}
}

type Order struct {
	ID            string  `json:"id"`
	ClientOrderID string  `json:"clientOrderId"`
	AccountID     string  `json:"accountId"`
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"`
	Type          string  `json:"type"`
	Price         float64 `json:"price"`
	Qty           int64   `json:"qty"`
	RemainingQty  int64   `json:"remainingQty"`
	Status        string  `json:"status"`
	CreatedAt     int64   `json:"createdAt"`
	Seq           uint64  `json:"seq"`
}

func FromOrder(o engine.Order) Order {
	return Order{
		ID: o.ID, ClientOrderID: o.ClientOrderID, AccountID: o.AccountID, Symbol: o.Symbol,
		Side: o.Side.String(), Type: o.Type.String(), Price: Rupees(o.Price), Qty: o.Qty,
		RemainingQty: o.Remaining, Status: string(o.Status), CreatedAt: ms(o.CreatedAt), Seq: o.Seq,
	}
}

type Fill struct {
	ID             string  `json:"id"`
	Symbol         string  `json:"symbol"`
	Price          float64 `json:"price"`
	Qty            int64   `json:"qty"`
	TakerOrderID   string  `json:"takerOrderId"`
	MakerOrderID   string  `json:"makerOrderId"`
	TakerAccountID string  `json:"takerAccountId"`
	MakerAccountID string  `json:"makerAccountId"`
	TakerSide      string  `json:"takerSide"`
	Timestamp      int64   `json:"timestamp"`
}

func FromFill(f engine.Fill) Fill {
	return Fill{
		ID: f.ID, Symbol: f.Symbol, Price: Rupees(f.Price), Qty: f.Qty,
		TakerOrderID: f.TakerOrderID, MakerOrderID: f.MakerOrderID,
		TakerAccountID: f.TakerAccountID, MakerAccountID: f.MakerAccountID,
		TakerSide: f.TakerSide.String(), Timestamp: ms(f.At),
	}
}

// PublicFill is what every viewer of a symbol may see about a trade: who traded is never published.
type PublicFill struct {
	ID        string  `json:"id"`
	Symbol    string  `json:"symbol"`
	Price     float64 `json:"price"`
	Qty       int64   `json:"qty"`
	TakerSide string  `json:"takerSide"`
	Timestamp int64   `json:"timestamp"`
}

func FromPublicFill(f engine.Fill) PublicFill {
	return PublicFill{ID: f.ID, Symbol: f.Symbol, Price: Rupees(f.Price), Qty: f.Qty, TakerSide: f.TakerSide.String(), Timestamp: ms(f.At)}
}

type Holding struct {
	Symbol        string  `json:"symbol"`
	Qty           int64   `json:"qty"`
	AvgPrice      float64 `json:"avgPrice"`
	MarketValue   float64 `json:"marketValue"`
	UnrealizedPnl float64 `json:"unrealizedPnl"`
}

type Portfolio struct {
	AccountID   string    `json:"accountId"`
	CashBalance float64   `json:"cashBalance"`
	Holdings    []Holding `json:"holdings"`
	TotalValue  float64   `json:"totalValue"`
}

type LeaderRow struct {
	Rank           int     `json:"rank"`
	AccountID      string  `json:"accountId"`
	DisplayName    string  `json:"displayName"`
	PortfolioValue float64 `json:"portfolioValue"`
	PercentReturn  float64 `json:"percentReturn"`
}

type NewsItem struct {
	ID        string `json:"id"`
	Kind      string `json:"kind,omitempty"`
	Headline  string `json:"headline"`
	Body      string `json:"body,omitempty"`
	CreatedAt int64  `json:"createdAt"`
}

type PricePoint struct {
	Price     float64 `json:"price"`
	Timestamp int64   `json:"timestamp"`
}

func MS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return ms(t)
}
