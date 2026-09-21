package engine

import (
	"context"
	"sync"

	"stockastic/api/internal/money"
)

// fingerprint is what must match for a reused idempotency key to be a genuine retry.
type fingerprint struct {
	typ    OrderType
	tif    TimeInForce
	symbol string
	side   Side
	price  money.Paise
	qty    int64
}

func fingerprintOf(n NewOrder) fingerprint {
	return fingerprint{n.Type, n.TIF, n.Symbol, n.Side, n.Price, n.Qty}
}

type idemEntry struct {
	fp   fingerprint
	done chan struct{}
	res  Result
	err  error
}

// wait returns the owner's outcome as a deduped replay (or its error).
func (e *idemEntry) wait(ctx context.Context, n NewOrder) (Result, error) {
	if e.fp != fingerprintOf(n) {
		return Result{}, ErrIdempotencyMismatch
	}
	select {
	case <-e.done:
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
	if e.err != nil {
		return Result{}, e.err
	}
	r := e.res
	r.Deduped = true
	return r, nil
}

// idemStore dedupes (accountID, clientOrderID). It is global across symbols so the same key can
// never be used to place two orders in different symbols.
type idemStore struct {
	mu sync.Mutex
	m  map[string]*idemEntry
}

func newIdemStore() *idemStore { return &idemStore{m: make(map[string]*idemEntry)} }

func key(accountID, clientOrderID string) string { return accountID + "\x00" + clientOrderID }

// acquire returns the entry for n's key and whether the caller created it (and so must run it).
func (s *idemStore) acquire(n NewOrder) (*idemEntry, bool) {
	k := key(n.AccountID, n.ClientOrderID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.m[k]; ok {
		return e, false
	}
	e := &idemEntry{fp: fingerprintOf(n), done: make(chan struct{})}
	s.m[k] = e
	return e, true
}

// finish records the owner's outcome. A failure that means "not processed" (frozen, journal
// failure, halted) removes the key so a later retry can genuinely run; waiters still see the error.
func (s *idemStore) finish(n NewOrder, e *idemEntry, res Result, err error) {
	if err != nil {
		s.mu.Lock()
		if s.m[key(n.AccountID, n.ClientOrderID)] == e {
			delete(s.m, key(n.AccountID, n.ClientOrderID))
		}
		s.mu.Unlock()
	}
	e.res, e.err = res, err
	close(e.done)
}

// abandon is finish for a command that was never enqueued.
func (s *idemStore) abandon(n NewOrder, e *idemEntry, err error) { s.finish(n, e, Result{}, err) }

// seed registers an already-processed order (from durable state) so retries dedupe.
func (s *idemStore) seed(o Order, fills []Fill) {
	// Orders persisted before types existed carry zero Type/TIF; normalise so retries still match.
	n := normalize(NewOrder{Type: o.Type, TIF: o.TIF, Symbol: o.Symbol, Side: o.Side, Price: o.Price, Qty: o.Qty})
	e := &idemEntry{
		fp:   fingerprintOf(n),
		done: make(chan struct{}),
		res:  Result{Order: o, Fills: fills},
	}
	close(e.done)
	s.mu.Lock()
	s.m[key(o.AccountID, o.ClientOrderID)] = e
	s.mu.Unlock()
}

// has reports whether (accountID, clientOrderID) has already been submitted.
func (s *idemStore) has(accountID, clientOrderID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.m[key(accountID, clientOrderID)]
	return ok
}
