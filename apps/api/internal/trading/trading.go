// Package trading executes participants' trades.
//
// The rulebook has no order book and no order types: a team buys or sells shares of a company, and the
// trade happens at that company's current simulated price. So a trade here is instant and always fills
// completely or is refused; there is nothing to queue, rest or cancel.
//
// Reliability contract:
//   - Write-before-ack: a trade is made durable through the Journal before balances change, and only then
//     confirmed. If the journal fails nothing changed.
//   - Idempotent on (account, clientTradeID): a retry, including one after a crash, returns the original
//     trade and never trades twice. Reusing an id for a different trade is refused.
//   - Trades by one account are handled one at a time; different accounts run in parallel.
package trading

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"stockastic/api/internal/ids"
	"stockastic/api/internal/ledger"
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

var (
	ErrInvalid             = errors.New("invalid_trade")
	ErrUnknownSymbol       = errors.New("unknown_symbol")
	ErrNoPrice             = errors.New("no_price")
	ErrIdempotencyMismatch = errors.New("idempotency_key_reused_with_different_trade")
	ErrJournal             = errors.New("journal_commit_failed")
)

// PriceChanged is returned when the caller named the price it expected and the company's price has
// since moved. Current is the price now, so the caller can show it and ask again.
type PriceChanged struct{ Current money.Paise }

func (e *PriceChanged) Error() string { return "price_changed" }

// Prices is where the current price of a company comes from (the price simulation).
type Prices interface {
	Price(symbol string) (money.Paise, bool)
}

// Journal makes a trade durable. Commit must be atomic: on nil the trade survives a crash.
type Journal interface {
	Commit(ctx context.Context, t Trade) error
}

// Request is a participant's instruction: which company, buy or sell, how many shares.
type Request struct {
	// ClientTradeID is the client-generated idempotency key, unique per account.
	ClientTradeID string
	AccountID     string
	Symbol        string
	Side          Side
	Qty           int64
	// ExpectedPrice, if not zero, is the price the person saw. The trade is refused (PriceChanged) if the
	// price has moved, so nobody trades at a price they did not agree to.
	ExpectedPrice money.Paise
	// Stage tags which event stage the trade happened in (for example "phase1"); it is only recorded.
	Stage string
}

// Trade is a completed trade.
type Trade struct {
	ID            string
	ClientTradeID string
	AccountID     string
	Symbol        string
	Side          Side
	Qty           int64
	Price         money.Paise
	Stage         string
	At            time.Time
}

// Notional is price times quantity.
func (t Trade) Notional() money.Paise { return t.Price * money.Paise(t.Qty) }

type Result struct {
	Trade Trade
	// Deduped is true when this is a replay of an earlier identical request.
	Deduped bool
}

type fingerprint struct {
	symbol string
	side   Side
	qty    int64
}

type Executor struct {
	ledger  *ledger.Ledger
	prices  Prices
	journal Journal
	now     func() time.Time

	locks sync.Map // account id -> *sync.Mutex

	mu   sync.Mutex
	seen map[string]entry
}

type entry struct {
	fp fingerprint
	tr Trade
}

func New(l *ledger.Ledger, p Prices, j Journal, now func() time.Time) *Executor {
	if now == nil {
		now = time.Now
	}
	return &Executor{ledger: l, prices: p, journal: j, now: now, seen: map[string]entry{}}
}

func key(account, clientID string) string { return account + "\x00" + clientID }

func (e *Executor) lockFor(account string) *sync.Mutex {
	m, _ := e.locks.LoadOrStore(account, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Seen reports whether this client trade id was already used by the account (a retry).
func (e *Executor) Seen(account, clientID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.seen[key(account, clientID)]
	return ok
}

// Seed registers a trade from durable state so a retry of it after a restart is recognised.
func (e *Executor) Seed(t Trade) {
	e.mu.Lock()
	e.seen[key(t.AccountID, t.ClientTradeID)] = entry{fp: fingerprint{t.Symbol, t.Side, t.Qty}, tr: t}
	e.mu.Unlock()
}

// Execute runs one trade.
func (e *Executor) Execute(ctx context.Context, r Request) (Result, error) {
	if r.AccountID == "" || r.ClientTradeID == "" || r.Symbol == "" || (r.Side != Buy && r.Side != Sell) || r.Qty <= 0 {
		return Result{}, ErrInvalid
	}
	mu := e.lockFor(r.AccountID)
	mu.Lock()
	defer mu.Unlock()

	fp := fingerprint{r.Symbol, r.Side, r.Qty}
	e.mu.Lock()
	prev, dup := e.seen[key(r.AccountID, r.ClientTradeID)]
	e.mu.Unlock()
	if dup {
		if prev.fp != fp {
			return Result{}, ErrIdempotencyMismatch
		}
		return Result{Trade: prev.tr, Deduped: true}, nil
	}

	price, ok := e.prices.Price(r.Symbol)
	if !ok {
		return Result{}, ErrUnknownSymbol
	}
	if price <= 0 {
		return Result{}, ErrNoPrice
	}
	if r.ExpectedPrice != 0 && r.ExpectedPrice != price {
		return Result{}, &PriceChanged{Current: price}
	}

	t := Trade{
		ID: ids.New(), ClientTradeID: r.ClientTradeID, AccountID: r.AccountID, Symbol: r.Symbol,
		Side: r.Side, Qty: r.Qty, Price: price, Stage: r.Stage, At: e.now(),
	}
	err := e.ledger.Trade(r.AccountID, r.Symbol, r.Side == Buy, r.Qty, price, func() error {
		if err := e.journal.Commit(ctx, t); err != nil {
			return fmt.Errorf("%w: %v", ErrJournal, err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	e.Seed(t)
	return Result{Trade: t}, nil
}

// Reset forgets every trade id, so a new event starts with a clean slate.
func (e *Executor) Reset() {
	e.mu.Lock()
	e.seen = map[string]entry{}
	e.mu.Unlock()
}
