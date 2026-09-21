// Package ledger is the source of truth for every account's cash and share holdings.
//
// Invariants it enforces itself, whatever the caller does:
//   - cash and share counts never go negative (no leverage, no short selling);
//   - a trade is checked, made durable through the caller's commit function, and applied as one step on
//     one account, so a failed commit changes nothing;
//   - money is integer paise throughout.
//
// Each account has its own lock, so trades by different teams never wait for each other.
package ledger

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"stockastic/api/internal/money"
)

type Kind uint8

const (
	KindTeam Kind = iota + 1
	KindFund
)

func (k Kind) String() string {
	switch k {
	case KindTeam:
		return "team"
	case KindFund:
		return "fund"
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
	ErrInvalid            = errors.New("invalid_amount")
)

// Position is a long holding with its total cost basis (average cost = Cost/Qty).
type Position struct {
	Symbol string
	Qty    int64
	Cost   money.Paise
}

type acct struct {
	mu sync.Mutex
	Account
	cash money.Paise
	pos  map[string]*Position
}

type Ledger struct {
	mu    sync.RWMutex // guards the account map only
	accts map[string]*acct
	log   *slog.Logger
}

func New(log *slog.Logger) *Ledger {
	if log == nil {
		log = slog.Default()
	}
	return &Ledger{accts: make(map[string]*acct), log: log}
}

// Open creates an account with an opening cash balance.
func (l *Ledger) Open(a Account, cash money.Paise) error {
	if a.ID == "" {
		return errors.New("ledger: account id is empty")
	}
	if cash < 0 {
		return fmt.Errorf("%w: opening cash %d", ErrInvalid, cash)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.accts[a.ID]; ok {
		return fmt.Errorf("%w: %s", ErrAccountExists, a.ID)
	}
	l.accts[a.ID] = &acct{Account: a, cash: cash, pos: map[string]*Position{}}
	return nil
}

func (l *Ledger) get(id string) (*acct, error) {
	l.mu.RLock()
	a, ok := l.accts[id]
	l.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrUnknownAccount, id)
	}
	return a, nil
}

// Has reports whether the account exists.
func (l *Ledger) Has(id string) bool { _, err := l.get(id); return err == nil }

// Trade buys or sells qty shares of symbol at price for one account. It checks the account can afford
// it, calls commit (which must make the trade durable), and only if commit succeeds changes the
// balances. If the check or the commit fails nothing changes.
func (l *Ledger) Trade(id, symbol string, buy bool, qty int64, price money.Paise, commit func() error) error {
	if qty <= 0 || price <= 0 || symbol == "" {
		return ErrInvalid
	}
	a, err := l.get(id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	notional := price * money.Paise(qty)
	if buy {
		if a.cash < notional {
			return ErrInsufficientCash
		}
	} else if p := a.pos[symbol]; p == nil || p.Qty < qty {
		return ErrInsufficientShares
	}
	if commit != nil {
		if err := commit(); err != nil {
			return err
		}
	}
	a.applyTrade(symbol, buy, qty, price)
	return nil
}

// ReplayTrade applies a trade that was already accepted, while rebuilding state from the log. It does
// not refuse: the log is the record of what happened.
func (l *Ledger) ReplayTrade(id, symbol string, buy bool, qty int64, price money.Paise) error {
	a, err := l.get(id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applyTrade(symbol, buy, qty, price)
	return nil
}

func (a *acct) applyTrade(symbol string, buy bool, qty int64, price money.Paise) {
	notional := price * money.Paise(qty)
	p := a.pos[symbol]
	if p == nil {
		p = &Position{Symbol: symbol}
		a.pos[symbol] = p
	}
	if buy {
		a.cash -= notional
		p.Qty += qty
		p.Cost += notional
		return
	}
	a.cash += notional
	if p.Qty > 0 {
		p.Cost -= money.Paise(int64(p.Cost) * min(qty, p.Qty) / p.Qty) // sold shares leave at average cost
	}
	p.Qty -= qty
	if p.Qty <= 0 {
		p.Qty, p.Cost = 0, 0
	}
}

// Grant credits shares to an account with no cash movement (an organiser's initial allocation or
// correction). price is the cost basis per share.
func (l *Ledger) Grant(id, symbol string, qty int64, price money.Paise) error {
	if qty <= 0 || price < 0 {
		return ErrInvalid
	}
	a, err := l.get(id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pos[symbol]
	if p == nil {
		p = &Position{Symbol: symbol}
		a.pos[symbol] = p
	}
	p.Qty += qty
	p.Cost += price * money.Paise(qty)
	return nil
}

// Revoke takes qty shares back at average cost. force skips the check and is for replaying the log.
func (l *Ledger) Revoke(id, symbol string, qty int64, force bool) error {
	if qty <= 0 {
		return ErrInvalid
	}
	a, err := l.get(id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	p := a.pos[symbol]
	if p == nil || (!force && p.Qty < qty) {
		return ErrInsufficientShares
	}
	if p.Qty > 0 {
		p.Cost -= money.Paise(int64(p.Cost) * min(qty, p.Qty) / p.Qty)
	}
	p.Qty -= qty
	if p.Qty <= 0 {
		p.Qty, p.Cost = 0, 0
	}
	return nil
}

// AdjustCash changes an account's cash by delta (an organiser's correction). A live change cannot take
// cash below zero; force is for replaying the log.
func (l *Ledger) AdjustCash(id string, delta money.Paise, force bool) error {
	a, err := l.get(id)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !force && a.cash+delta < 0 {
		return ErrInsufficientCash
	}
	a.cash += delta
	return nil
}

// Move takes amount of cash from one account and gives it to another as one step, calling commit first
// so it is durable. Both accounts are locked (in a fixed order, so two moves can never deadlock).
func (l *Ledger) Move(from, to string, amount money.Paise, commit func() error) error {
	if amount <= 0 || from == to {
		return ErrInvalid
	}
	src, err := l.get(from)
	if err != nil {
		return err
	}
	dst, err := l.get(to)
	if err != nil {
		return err
	}
	first, second := src, dst
	if from > to {
		first, second = dst, src
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	second.mu.Lock()
	defer second.mu.Unlock()
	if src.cash < amount {
		return ErrInsufficientCash
	}
	if commit != nil {
		if err := commit(); err != nil {
			return err
		}
	}
	src.cash -= amount
	dst.cash += amount
	return nil
}

// ReplayMove applies a move that was already accepted.
func (l *Ledger) ReplayMove(from, to string, amount money.Paise) error {
	src, err := l.get(from)
	if err != nil {
		return err
	}
	dst, err := l.get(to)
	if err != nil {
		return err
	}
	first, second := src, dst
	if from > to {
		first, second = dst, src
	}
	first.mu.Lock()
	defer first.mu.Unlock()
	second.mu.Lock()
	defer second.mu.Unlock()
	src.cash -= amount
	dst.cash += amount
	return nil
}

type Snapshot struct {
	Account   Account
	Cash      money.Paise
	Positions []Position
}

func (l *Ledger) Snapshot(id string) (Snapshot, error) {
	a, err := l.get(id)
	if err != nil {
		return Snapshot{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	s := Snapshot{Account: a.Account, Cash: a.cash}
	for _, p := range a.pos {
		if p.Qty > 0 {
			s.Positions = append(s.Positions, *p)
		}
	}
	sort.Slice(s.Positions, func(i, j int) bool { return s.Positions[i].Symbol < s.Positions[j].Symbol })
	return s, nil
}

// PriceFn returns the price to value a company at.
type PriceFn func(symbol string) (money.Paise, bool)

// Value is cash plus every holding at the given prices. A holding with no price is carried at cost so a
// company without a price yet does not read as worthless.
func (s Snapshot) Value(price PriceFn) money.Paise {
	total := s.Cash
	for _, p := range s.Positions {
		if px, ok := price(p.Symbol); ok {
			total += px * money.Paise(p.Qty)
		} else {
			total += p.Cost
		}
	}
	return total
}

// DirectValue is an account's cash plus its holdings at the given prices.
func (l *Ledger) DirectValue(id string, price PriceFn) (money.Paise, error) {
	s, err := l.Snapshot(id)
	if err != nil {
		return 0, err
	}
	return s.Value(price), nil
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

// Total is the cash of every account and the number of shares of each company held, for reconciliation.
func (l *Ledger) Total() (cash money.Paise, shares map[string]int64) {
	shares = map[string]int64{}
	l.mu.RLock()
	all := make([]*acct, 0, len(l.accts))
	for _, a := range l.accts {
		all = append(all, a)
	}
	l.mu.RUnlock()
	for _, a := range all {
		a.mu.Lock()
		cash += a.cash
		for s, p := range a.pos {
			shares[s] += p.Qty
		}
		a.mu.Unlock()
	}
	return cash, shares
}
