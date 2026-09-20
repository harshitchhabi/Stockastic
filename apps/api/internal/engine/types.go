// Package engine is the matching engine: one goroutine per symbol owns that symbol's order book
// and is fed by a buffered command channel, so orders within a symbol are processed strictly one
// at a time while symbols run in parallel. Price-time priority, partial fills, cancel, freeze.
//
// Reliability contract:
//   - Write-before-ack: a match is planned without touching the book, committed to the Journal,
//     and only then applied. If the commit fails nothing changed and the caller gets an error.
//   - Idempotency: (accountID, clientOrderID) is processed at most once; retries and concurrent
//     duplicates get the original result.
//   - A panic in a symbol's goroutine is recovered; that symbol is halted (its book may be
//     inconsistent) until Resume rebuilds it from durable state. Other symbols are unaffected.
//
// The engine knows nothing about fees, caps, windows, cash or holdings.
package engine

import (
	"time"

	"stockastic/api/internal/money"
)

type Side uint8

const (
	Buy Side = iota + 1
	Sell
)

func (s Side) String() string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	}
	return "invalid"
}

type Status string

const (
	StatusOpen            Status = "open"
	StatusPartiallyFilled Status = "partially_filled"
	StatusFilled          Status = "filled"
	StatusCancelled       Status = "cancelled"
)

// Live reports whether the order is still resting in the book.
func (s Status) Live() bool { return s == StatusOpen || s == StatusPartiallyFilled }

// OrderType is how an order prices itself.
type OrderType uint8

const (
	// TypeLimit trades at its limit price or better.
	TypeLimit OrderType = iota + 1
	// TypeMarket takes the best available prices. It never rests: whatever is not filled immediately is
	// cancelled. Its Price is a PROTECTION price — the worst price it may trade at — so a market order
	// can never spend more than the cash reserved for it.
	TypeMarket
)

func (t OrderType) String() string {
	switch t {
	case TypeLimit:
		return "limit"
	case TypeMarket:
		return "market"
	}
	return "invalid"
}

// TimeInForce says what happens to the part of an order that cannot fill immediately.
type TimeInForce uint8

const (
	// GTC (good till cancelled) rests the remainder in the book. Limit orders only.
	GTC TimeInForce = iota + 1
	// IOC (immediate or cancel) fills what it can now and cancels the rest.
	IOC
	// FOK (fill or kill) fills completely now or not at all.
	FOK
)

func (t TimeInForce) String() string {
	switch t {
	case GTC:
		return "gtc"
	case IOC:
		return "ioc"
	case FOK:
		return "fok"
	}
	return "invalid"
}

// NewOrder is an order request. Prices are integer paise; quantities are whole shares. The zero Type
// means TypeLimit and the zero TIF means GTC for a limit order (IOC for a market order).
type NewOrder struct {
	Type OrderType
	TIF  TimeInForce
	// ClientOrderID is the client-generated idempotency key, unique per account.
	ClientOrderID string
	AccountID     string
	Symbol        string
	Side          Side
	Price         money.Paise
	Qty           int64
}

type Order struct {
	Type          OrderType
	TIF           TimeInForce
	ID            string
	ClientOrderID string
	AccountID     string
	Symbol        string
	Side          Side
	Price         money.Paise
	Qty           int64
	// Remaining is the unfilled quantity. A cancelled order keeps what was unfilled at cancel time,
	// so Qty-Remaining is always the filled quantity.
	Remaining int64
	Status    Status
	// Seq is the per-symbol time-priority sequence number.
	Seq       uint64
	CreatedAt time.Time
}

type Fill struct {
	ID             string
	Symbol         string
	Price          money.Paise // always the resting (maker) order's price
	Qty            int64
	TakerOrderID   string
	MakerOrderID   string
	TakerAccountID string
	MakerAccountID string
	TakerSide      Side
	At             time.Time
}

// Notional is price x quantity.
func (f Fill) Notional() money.Paise { return f.Price * money.Paise(f.Qty) }

type Level struct {
	Price  money.Paise
	Qty    int64
	Orders int
}

type Depth struct {
	Symbol string
	Bids   []Level // best (highest) first
	Asks   []Level // best (lowest) first
}

// Result is the outcome of a submit or cancel.
type Result struct {
	Order Order
	Fills []Fill
	// Deduped is true when this is a replay of an earlier identical submission.
	Deduped bool
}

type BatchKind uint8

const (
	KindSubmit BatchKind = iota + 1
	KindCancel
)

// Batch is everything one matching step changes. It is committed to the Journal atomically
// BEFORE it is applied to the book.
type Batch struct {
	Kind   BatchKind
	Symbol string
	// Order is the submitted order after matching (KindSubmit) or the cancelled order (KindCancel).
	Order Order
	Fills []Fill
	// Makers are the resting orders touched by Fills, in their post-fill state.
	Makers []Order
}

// Applied is a Batch that has been committed and applied, plus the resulting book snapshot.
type Applied struct {
	Batch
	Depth Depth
	At    time.Time
}
