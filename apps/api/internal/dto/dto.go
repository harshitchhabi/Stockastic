// Package dto is the JSON shape the web app and the Go server agree on. Money is held as integer paise
// everywhere inside the server and converted to rupees (a JSON number) only here, at the boundary, so
// the web app's rupee amounts keep working and no float ever takes part in accounting.
package dto

import (
	"time"

	"stockastic/api/internal/money"
	"stockastic/api/internal/trading"
)

func Rupees(p money.Paise) float64 { return p.Rupees() }
func Paise(r float64) money.Paise  { return money.FromRupees(r) }

func MS(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

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

// Trade is one executed trade, at the price it happened at.
type Trade struct {
	ID            string  `json:"id"`
	ClientTradeID string  `json:"clientTradeId"`
	Symbol        string  `json:"symbol"`
	Side          string  `json:"side"`
	Qty           int64   `json:"qty"`
	Price         float64 `json:"price"`
	Value         float64 `json:"value"`
	Timestamp     int64   `json:"timestamp"`
}

func FromTrade(t trading.Trade) Trade {
	return Trade{
		ID: t.ID, ClientTradeID: t.ClientTradeID, Symbol: t.Symbol, Side: t.Side.String(), Qty: t.Qty,
		Price: Rupees(t.Price), Value: Rupees(t.Notional()), Timestamp: MS(t.At),
	}
}

// PriceTick is one company's price.
type PriceTick struct {
	Symbol string  `json:"symbol"`
	Price  float64 `json:"price"`
}

// PricesUpdate is pushed to every browser whenever prices change.
type PricesUpdate struct {
	At     int64       `json:"at"`
	Prices []PriceTick `json:"prices"`
}

type Holding struct {
	Symbol        string  `json:"symbol"`
	Qty           int64   `json:"qty"`
	AvgPrice      float64 `json:"avgPrice"`
	MarketValue   float64 `json:"marketValue"`
	UnrealizedPnl float64 `json:"unrealizedPnl"`
}

// FundPosition is an investor's units in one fund at the current NAV.
type FundPosition struct {
	FundID      string  `json:"fundId"`
	Name        string  `json:"name"`
	Units       float64 `json:"units"`
	NAV         float64 `json:"nav"`
	Value       float64 `json:"value"`
	Contributed float64 `json:"contributed"`
	Pnl         float64 `json:"pnl"`
}

type Portfolio struct {
	FundID        string         `json:"fundId,omitempty"` // set when this is a fund's portfolio
	FundPositions []FundPosition `json:"fundPositions"`
	AccountID     string         `json:"accountId"`
	CashBalance   float64        `json:"cashBalance"`
	Holdings      []Holding      `json:"holdings"`
	TotalValue    float64        `json:"totalValue"`
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
