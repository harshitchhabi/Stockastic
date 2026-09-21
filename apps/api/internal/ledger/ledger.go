// Package ledger owns accounts, cash, holdings and open-order reservations. Pre-trade checks
// forbid shorting and leverage (an order must be covered by unreserved cash or unreserved shares),
// fills are applied to both counterparties, and portfolios can be valued at any set of prices.
//
// State here is a pure fold over durable events (fills, cash movements), so the store rebuilds it
// exactly at startup by replaying them; nothing in this package is the system of record.
package ledger

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"stockastic/api/internal/engine"
	"stockastic/api/internal/money"
)

// Kind is the type of account.
type Kind uint8

const (
	// KindTeam is a login team of up to 3 (an individual investor team, or a Phase-1 team).
	KindTeam Kind = iota + 1
	// KindFund is a Fund Management Team's shared trading account (one account, one rate limit,
	// however many members — Sec 11).
	KindFund
	// KindMarketMaker is a system liquidity account. SCAFFOLD ONLY: whether it is exempt from the
	// no-shorting / cash-cover checks, how it is seeded, and who drives it are open organiser
	// decisions, so today it is checked exactly like every other account.
	KindMarketMaker
)

func (k Kind) String() string {
	switch k {
	case KindTeam:
		return "team"
	case KindFund:
		return "fund"
	case KindMarketMaker:
		return "market_maker"
	}
	return "unknown"
}

type Account struct {
	ID   string
	Name string
	Kind Kind
}

var (
	ErrUnknownAccount     = errors.New("unknown_account")
	ErrAccountExists      = errors.New("account_exists")
	ErrInsufficientCash   = errors.New("insufficient_cash")
	ErrInsufficientShares = errors.New("insufficient_shares")
)

// Position is a long holding with its total cost basis (average cost = Cost/Qty).
type Position struct {
	Symbol string
	Qty    int64
	Cost   money.Paise
}

type reservation struct {
	side      engine.Side
	symbol    string
	price     money.Paise
	remaining int64
}

type account struct {
	Account
	cash money.Paise
	pos  map[string]*Position
	// res is keyed by clientOrderID: the reservation exists from before submission until the order is
	// no longer live.
	res map[string]reservation
}

type Ledger struct {
	mu        sync.RWMutex
	accts     map[string]*account
	anomalies int
	log       *slog.Logger
}

func New(log *slog.Logger) *Ledger {
	if log == nil {
		log = slog.Default()
	}
	return &Ledger{accts: make(map[string]*account), log: log}
}

// Open creates an account with an opening cash balance.
func (l *Ledger) Open(a Account, cash money.Paise) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.accts[a.ID]; ok {
		return fmt.Errorf("%w: %s", ErrAccountExists, a.ID)
	}
	l.accts[a.ID] = &account{Account: a, cash: cash, pos: map[string]*Position{}, res: map[string]reservation{}}
	return nil
}

func (l *Ledger) get(id string) (*account, error) {
	a, ok := l.accts[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAccount, id)
	}
	return a, nil
}

func (a *account) reservedCash() (total money.Paise) {
	for _, r := range a.res {
		if r.side == engine.Buy {
			total += r.price * money.Paise(r.remaining)
		}
	}
	return total
}

func (a *account) reservedShares(symbol string) (total int64) {
	for _, r := range a.res {
		if r.side == engine.Sell && r.symbol == symbol {
			total += r.remaining
		}
	}
	return total
}

// Reserve is the pre-trade check. A buy must be covered by unreserved cash at its limit price; a
// sell by unreserved shares (no shorting). It reserves atomically with the check, so concurrent
// orders can never jointly overspend. It returns created=false if this clientOrderID already holds
// a reservation (a retry), in which case nothing new was reserved.
//
// Call Release if the order is then not processed (rejected, frozen, journal failure, or a deduped
// replay of an already-finished order and created is true).
func (l *Ledger) Reserve(n engine.NewOrder) (created bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.get(n.AccountID)
	if err != nil {
		return false, err
	}
	if _, dup := a.res[n.ClientOrderID]; dup {
		return false, nil
	}
	switch n.Side {
	case engine.Buy:
		need := n.Price * money.Paise(n.Qty)
		if a.cash-a.reservedCash() < need {
			return false, ErrInsufficientCash
		}
	case engine.Sell:
		have := int64(0)
		if p := a.pos[n.Symbol]; p != nil {
			have = p.Qty
		}
		if have-a.reservedShares(n.Symbol) < n.Qty {
			return false, ErrInsufficientShares
		}
	default:
		return false, engine.ErrInvalidOrder
	}
	a.res[n.ClientOrderID] = reservation{side: n.Side, symbol: n.Symbol, price: n.Price, remaining: n.Qty}
	return true, nil
}

// Release drops a reservation that will not be followed by a live order.
func (l *Ledger) Release(accountID, clientOrderID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if a, ok := l.accts[accountID]; ok {
		delete(a.res, clientOrderID)
	}
}

// OnApplied implements engine.Sink: it applies committed fills to both counterparties and shrinks
// or releases reservations to match each order's post-match state.
func (l *Ledger) OnApplied(ap engine.Applied) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if ap.Kind == engine.KindSubmit {
		for _, f := range ap.Fills {
			l.applyFillLocked(f)
		}
		l.syncReservationLocked(ap.Order)
		for _, m := range ap.Makers {
			l.syncReservationLocked(m)
		}
		return
	}
	l.syncReservationLocked(ap.Order) // cancel: no longer live -> released
}

func (l *Ledger) syncReservationLocked(o engine.Order) {
	a, ok := l.accts[o.AccountID]
	if !ok {
		return
	}
	r, ok := a.res[o.ClientOrderID]
	if !ok {
		return
	}
	if !o.Status.Live() {
		delete(a.res, o.ClientOrderID)
		return
	}
	r.remaining = o.Remaining
	a.res[o.ClientOrderID] = r
}

func (l *Ledger) applyFillLocked(f engine.Fill) {
	l.applyLegLocked(f.TakerAccountID, f.TakerSide, f)
	maker := engine.Sell
	if f.TakerSide == engine.Sell {
		maker = engine.Buy
	}
	l.applyLegLocked(f.MakerAccountID, maker, f)
}

func (l *Ledger) applyLegLocked(accountID string, side engine.Side, f engine.Fill) {
	a, ok := l.accts[accountID]
	if !ok {
		// A committed fill for an account we do not know is a data-integrity problem, not something to
		// paper over: count it and log loudly so it fails health checks and tests.
		l.anomalies++
		l.log.Error("ledger: committed fill references unknown account", "account", accountID, "fill", f.ID)
		return
	}
	notional := f.Notional()
	p := a.pos[f.Symbol]
	if p == nil {
		p = &Position{Symbol: f.Symbol}
		a.pos[f.Symbol] = p
	}
	if side == engine.Buy {
		a.cash -= notional
		p.Qty += f.Qty
		p.Cost += notional
		return
	}
	a.cash += notional
	if p.Qty > 0 {
		p.Cost -= money.Paise(int64(p.Cost) * f.Qty / p.Qty) // remove the sold shares at average cost
	}
	p.Qty -= f.Qty
	if p.Qty <= 0 {
		if p.Qty < 0 {
			l.anomalies++
			l.log.Error("ledger: position went negative (shorting)", "account", accountID, "symbol", f.Symbol, "qty", p.Qty)
		}
		p.Cost = 0
	}
}

// Anomalies counts integrity violations seen while applying committed events (should stay 0).
func (l *Ledger) Anomalies() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.anomalies
}

// Transfer moves cash between two accounts atomically (fund allocations/redemptions). It fails,
// changing nothing, if the source lacks unreserved cash.
func (l *Ledger) Transfer(from, to string, amount money.Paise) error {
	if amount <= 0 {
		return errors.New("ledger: transfer amount must be positive")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	src, err := l.get(from)
	if err != nil {
		return err
	}
	dst, err := l.get(to)
	if err != nil {
		return err
	}
	if src.cash-src.reservedCash() < amount {
		return ErrInsufficientCash
	}
	src.cash -= amount
	dst.cash += amount
	return nil
}

// Restore rebuilds live-order reservations from durable state. Call it after replaying fills,
// with every order that is still live.
func (l *Ledger) Restore(fills []engine.Fill, live []engine.Order) {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, f := range fills {
		l.applyFillLocked(f)
	}
	for _, o := range live {
		if a, ok := l.accts[o.AccountID]; ok && o.Status.Live() {
			a.res[o.ClientOrderID] = reservation{side: o.Side, symbol: o.Symbol, price: o.Price, remaining: o.Remaining}
		}
	}
}

type Snapshot struct {
	Account       Account
	Cash          money.Paise
	ReservedCash  money.Paise
	AvailableCash money.Paise
	Positions     []Position
}

func (l *Ledger) Snapshot(accountID string) (Snapshot, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	a, err := l.get(accountID)
	if err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Account: a.Account, Cash: a.cash, ReservedCash: a.reservedCash()}
	s.AvailableCash = s.Cash - s.ReservedCash
	for _, p := range a.pos {
		if p.Qty != 0 {
			s.Positions = append(s.Positions, *p)
		}
	}
	sort.Slice(s.Positions, func(i, j int) bool { return s.Positions[i].Symbol < s.Positions[j].Symbol })
	return s, nil
}

// PriceFn returns the price to mark a symbol at (last trade, or the freeze price at a snapshot).
type PriceFn func(symbol string) (money.Paise, bool)

// DirectValue is cash + holdings at the given prices. A holding with no price is carried at cost so a
// symbol that has not traded yet does not read as worthless.
func (l *Ledger) DirectValue(accountID string, price PriceFn) (money.Paise, error) {
	s, err := l.Snapshot(accountID)
	if err != nil {
		return 0, err
	}
	total := s.Cash
	for _, p := range s.Positions {
		if px, ok := price(p.Symbol); ok {
			total += px * money.Paise(p.Qty)
		} else {
			total += p.Cost
		}
	}
	return total, nil
}

// Grant credits shares to an account with no cash movement. It is the only way inventory enters the
// system (an initial allocation, or a market-maker's stock); every other change of holdings comes from a
// matched trade. price is the cost basis per share.
func (l *Ledger) Grant(accountID, symbol string, qty int64, price money.Paise) error {
	if qty <= 0 || price < 0 {
		return engine.ErrInvalidOrder
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	a, err := l.get(accountID)
	if err != nil {
		return err
	}
	p := a.pos[symbol]
	if p == nil {
		p = &Position{Symbol: symbol}
		a.pos[symbol] = p
	}
	p.Qty += qty
	p.Cost += price * money.Paise(qty)
	return nil
}

// Accounts lists every account id (sorted).
func (l *Ledger) Accounts() []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]string, 0, len(l.accts))
	for id := range l.accts {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}
