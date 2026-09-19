package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"stockastic/api/internal/money"
)

var (
	ErrUnknownSymbol       = errors.New("unknown_symbol")
	ErrInvalidOrder        = errors.New("invalid_order")
	ErrFrozen              = errors.New("trading_frozen")
	ErrStopped             = errors.New("engine_stopped")
	ErrSymbolHalted        = errors.New("symbol_halted")
	ErrJournal             = errors.New("journal_commit_failed")
	ErrIdempotencyMismatch = errors.New("idempotency_key_reused_with_different_order")
	ErrNotFound            = errors.New("order_not_found")
	ErrNotOwner            = errors.New("not_your_order")
	// ErrAlreadyClosed is returned with the order attached when cancelling a filled/cancelled order.
	ErrAlreadyClosed = errors.New("order_already_closed")
)

// Journal makes a matching step durable BEFORE it takes effect. Commit must be atomic: on a nil
// return the order, its fills and the touched maker orders are all durable; on error none are.
// It is called from the symbol's goroutine, so per-symbol throughput is bounded by commit latency —
// acceptable because the rulebook's 2 trades/minute/account cap keeps total load low.
type Journal interface {
	Commit(ctx context.Context, b Batch) error
}

// Sink receives committed-and-applied changes, synchronously on the symbol goroutine. It must not
// block (enqueue and return). A panic in a Sink is recovered and logged; it cannot corrupt the book.
type Sink interface {
	OnApplied(a Applied)
}

// PriceSink is the seam to whichever component owns price simulation. The engine calls
// PriceUpdate with the last trade price after every fill.
//
// SCAFFOLD ONLY: who generates prices (and whether they also flow INTO the engine) is an open
// organiser decision; nothing here generates or interprets prices.
type PriceSink interface {
	PriceUpdate(symbol string, price money.Paise, at time.Time)
}

// PriceUpdateFunc adapts a function to PriceSink.
type PriceUpdateFunc func(symbol string, price money.Paise, at time.Time)

func (f PriceUpdateFunc) PriceUpdate(symbol string, price money.Paise, at time.Time) {
	f(symbol, price, at)
}

type Config struct {
	Symbols []string
	// QueueSize is each symbol's command-channel capacity (backpressure boundary). Default 256.
	QueueSize int
	// DepthLevels is how many levels per side are published in snapshots. Default 20.
	DepthLevels int
	// CommitTimeout bounds one Journal.Commit. Default 5s.
	CommitTimeout time.Duration
	Journal       Journal
	Sink          Sink      // optional
	Prices        PriceSink // optional
	Clock         func() time.Time
	Log           *slog.Logger
}

type cmdKind uint8

const (
	cmdSubmit cmdKind = iota + 1
	cmdCancel
	cmdGetOrder
	cmdResume
)

type command struct {
	kind   cmdKind
	order  NewOrder
	id     string // cancel / getOrder
	acct   string // cancel
	resume []Order
	reply  chan reply // buffered(1): the goroutine never blocks on a departed caller
}

type reply struct {
	res Result
	err error
}

type symbolState struct {
	name   string
	cmds   chan command
	b      *book
	halted atomic.Bool
	depth  atomic.Pointer[Depth]
}

type Engine struct {
	cfg    Config
	syms   map[string]*symbolState
	idem   *idemStore
	frozen atomic.Bool

	mu      sync.RWMutex // guards started/stopped and channel closure against concurrent senders
	started bool
	stopped bool
	wg      sync.WaitGroup
}

func New(cfg Config) (*Engine, error) {
	if cfg.Journal == nil {
		return nil, errors.New("engine: Journal is required")
	}
	if len(cfg.Symbols) == 0 {
		return nil, errors.New("engine: no symbols")
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = 256
	}
	if cfg.DepthLevels <= 0 {
		cfg.DepthLevels = 20
	}
	if cfg.CommitTimeout <= 0 {
		cfg.CommitTimeout = 5 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	e := &Engine{cfg: cfg, syms: make(map[string]*symbolState, len(cfg.Symbols)), idem: newIdemStore()}
	for _, s := range cfg.Symbols {
		if _, dup := e.syms[s]; dup {
			return nil, fmt.Errorf("engine: duplicate symbol %q", s)
		}
		st := &symbolState{name: s, cmds: make(chan command, cfg.QueueSize), b: newBook(s)}
		d := st.b.snapshot(cfg.DepthLevels)
		st.depth.Store(&d)
		e.syms[s] = st
	}
	return e, nil
}

// Restore loads persisted orders into their books BEFORE Start. Live orders are restored in Seq
// order (price-time priority); terminal orders are kept so cancel/lookups stay idempotent. It also
// seeds the idempotency cache from every restored order, so a post-crash retry of a pre-crash
// clientOrderID dedupes instead of double-trading.
func (e *Engine) Restore(orders []Order, fillsByTaker map[string][]Fill) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return errors.New("engine: Restore must be called before Start")
	}
	sorted := append([]Order(nil), orders...)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
	for _, o := range sorted {
		st, ok := e.syms[o.Symbol]
		if !ok {
			return fmt.Errorf("engine: restore: %w %q", ErrUnknownSymbol, o.Symbol)
		}
		st.b.restore(o)
		e.idem.seed(o, fillsByTaker[o.ID])
	}
	for _, st := range e.syms {
		d := st.b.snapshot(e.cfg.DepthLevels)
		st.depth.Store(&d)
	}
	return nil
}

// Start launches one goroutine per symbol.
func (e *Engine) Start() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started {
		return
	}
	e.started = true
	for _, st := range e.syms {
		e.wg.Add(1)
		go e.run(st)
	}
}

// Stop stops accepting commands, lets every symbol drain what is already queued, and waits for the
// goroutines to exit (or ctx to expire). Safe to call once, from graceful shutdown.
func (e *Engine) Stop(ctx context.Context) error {
	e.mu.Lock()
	if e.stopped {
		e.mu.Unlock()
		return nil
	}
	e.stopped = true
	for _, st := range e.syms {
		close(st.cmds)
	}
	e.mu.Unlock()

	done := make(chan struct{})
	go func() { e.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("engine: stop: %w", ctx.Err())
	}
}

// Freeze rejects every order submission from now on. The check happens when a queued submission
// is executed, not when it is enqueued, so submissions already waiting in a queue are rejected too.
// Cancels are still allowed. Freeze never undoes anything already matched.
func (e *Engine) Freeze()        { e.frozen.Store(true) }
func (e *Engine) Unfreeze()      { e.frozen.Store(false) }
func (e *Engine) IsFrozen() bool { return e.frozen.Load() }
func (e *Engine) Symbols() []string {
	out := make([]string, 0, len(e.syms))
	for s := range e.syms {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Halted reports whether a symbol stopped after a recovered panic and needs Resume.
func (e *Engine) Halted(symbol string) bool {
	st, ok := e.syms[symbol]
	return ok && st.halted.Load()
}

// Depth returns the latest published book snapshot. It never waits behind the command queue.
func (e *Engine) Depth(symbol string) (Depth, error) {
	st, ok := e.syms[symbol]
	if !ok {
		return Depth{}, ErrUnknownSymbol
	}
	return *st.depth.Load(), nil
}

func validate(n NewOrder) error {
	switch {
	case n.ClientOrderID == "", n.AccountID == "":
		return fmt.Errorf("%w: clientOrderID and accountID are required", ErrInvalidOrder)
	case n.Side != Buy && n.Side != Sell:
		return fmt.Errorf("%w: side", ErrInvalidOrder)
	case n.Price <= 0:
		return fmt.Errorf("%w: price must be positive", ErrInvalidOrder)
	case n.Qty <= 0:
		return fmt.Errorf("%w: qty must be positive", ErrInvalidOrder)
	}
	return nil
}

// Submit places a limit order. It returns only after the order has been committed to the Journal
// and applied (write-before-ack). Duplicate (AccountID, ClientOrderID) submissions — sequential or
// concurrent — return the original result with Deduped=true.
func (e *Engine) Submit(ctx context.Context, n NewOrder) (Result, error) {
	if err := validate(n); err != nil {
		return Result{}, err
	}
	st, ok := e.syms[n.Symbol]
	if !ok {
		return Result{}, ErrUnknownSymbol
	}
	entry, owner := e.idem.acquire(n)
	if !owner {
		return entry.wait(ctx, n)
	}
	rep, err := e.send(ctx, st, command{kind: cmdSubmit, order: n})
	if err != nil {
		// Never enqueued (stopped, or ctx expired while the queue was full): safe to forget.
		e.idem.abandon(n, entry, err)
		return Result{}, err
	}
	// Once enqueued the command WILL run even if ctx expires here, so keep the entry: a retry then
	// waits for and receives the real result instead of double-submitting.
	select {
	case r := <-rep:
		e.idem.finish(n, entry, r.res, r.err)
		return r.res, r.err
	case <-ctx.Done():
		go func() {
			r := <-rep
			e.idem.finish(n, entry, r.res, r.err)
		}()
		return Result{}, ctx.Err()
	}
}

// Cancel removes a resting order. Only the owning account may cancel it. Cancels are journaled and
// allowed while frozen (they only reduce exposure).
func (e *Engine) Cancel(ctx context.Context, symbol, orderID, accountID string) (Result, error) {
	st, ok := e.syms[symbol]
	if !ok {
		return Result{}, ErrUnknownSymbol
	}
	rep, err := e.send(ctx, st, command{kind: cmdCancel, id: orderID, acct: accountID})
	if err != nil {
		return Result{}, err
	}
	select {
	case r := <-rep:
		return r.res, r.err
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

// Order looks an order up by id (serialised through the symbol's queue, so it is consistent).
func (e *Engine) Order(ctx context.Context, symbol, orderID string) (Order, error) {
	st, ok := e.syms[symbol]
	if !ok {
		return Order{}, ErrUnknownSymbol
	}
	rep, err := e.send(ctx, st, command{kind: cmdGetOrder, id: orderID})
	if err != nil {
		return Order{}, err
	}
	select {
	case r := <-rep:
		return r.res.Order, r.err
	case <-ctx.Done():
		return Order{}, ctx.Err()
	}
}

// Resume rebuilds a halted (or any) symbol's book from durable state and clears the halt. The
// caller supplies every persisted order for that symbol.
func (e *Engine) Resume(ctx context.Context, symbol string, orders []Order) error {
	st, ok := e.syms[symbol]
	if !ok {
		return ErrUnknownSymbol
	}
	rep, err := e.send(ctx, st, command{kind: cmdResume, resume: orders})
	if err != nil {
		return err
	}
	select {
	case r := <-rep:
		return r.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// send enqueues a command. It blocks only while the symbol's queue is full and only until ctx ends,
// which is the backpressure boundary: callers see a deadline error rather than unbounded memory.
func (e *Engine) send(ctx context.Context, st *symbolState, c command) (<-chan reply, error) {
	c.reply = make(chan reply, 1)
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.stopped || !e.started {
		return nil, ErrStopped
	}
	select {
	case st.cmds <- c:
		return c.reply, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *Engine) run(st *symbolState) {
	defer e.wg.Done()
	for c := range st.cmds {
		e.dispatch(st, c)
	}
}

// dispatch runs one command and converts a panic into a halted symbol instead of a dead goroutine.
func (e *Engine) dispatch(st *symbolState, c command) {
	defer func() {
		if r := recover(); r != nil {
			st.halted.Store(true)
			e.cfg.Log.Error("engine: panic in symbol goroutine; symbol halted until Resume",
				"symbol", st.name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
			c.reply <- reply{err: ErrSymbolHalted}
		}
	}()
	switch c.kind {
	case cmdSubmit:
		c.reply <- e.handleSubmit(st, c.order)
	case cmdCancel:
		c.reply <- e.handleCancel(st, c.id, c.acct)
	case cmdGetOrder:
		if o, ok := st.b.byID[c.id]; ok {
			c.reply <- reply{res: Result{Order: *o}}
		} else {
			c.reply <- reply{err: ErrNotFound}
		}
	case cmdResume:
		st.b = newBook(st.name)
		sorted := append([]Order(nil), c.resume...)
		sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Seq < sorted[j].Seq })
		for _, o := range sorted {
			st.b.restore(o)
		}
		e.publish(st)
		st.halted.Store(false)
		e.cfg.Log.Warn("engine: symbol resumed from durable state", "symbol", st.name, "orders", len(sorted))
		c.reply <- reply{}
	}
}

func (e *Engine) commit(b Batch) error {
	ctx, cancel := context.WithTimeout(context.Background(), e.cfg.CommitTimeout)
	defer cancel()
	if err := e.cfg.Journal.Commit(ctx, b); err != nil {
		e.cfg.Log.Error("engine: journal commit failed; nothing applied", "symbol", b.Symbol, "err", err)
		return fmt.Errorf("%w: %v", ErrJournal, err)
	}
	return nil
}

func (e *Engine) handleSubmit(st *symbolState, n NewOrder) reply {
	if st.halted.Load() {
		return reply{err: ErrSymbolHalted}
	}
	// Checked here, at execution time, so an order queued before Freeze() but not yet run is rejected.
	if e.frozen.Load() {
		return reply{err: ErrFrozen}
	}
	now := e.cfg.Clock()
	p := st.b.planSubmit(n, now)
	fills := p.fillRecords(st.name, now)
	batch := Batch{Kind: KindSubmit, Symbol: st.name, Order: p.taker, Fills: fills, Makers: p.makers}
	if err := e.commit(batch); err != nil {
		return reply{err: err} // durable write failed: the book is untouched
	}
	st.b.apply(p)
	e.afterApply(st, batch, now)
	return reply{res: Result{Order: p.taker, Fills: fills}}
}

func (e *Engine) handleCancel(st *symbolState, id, acct string) reply {
	if st.halted.Load() {
		return reply{err: ErrSymbolHalted}
	}
	o, ok := st.b.byID[id]
	if !ok {
		return reply{err: ErrNotFound}
	}
	if o.AccountID != acct {
		return reply{err: ErrNotOwner}
	}
	if !o.Status.Live() {
		return reply{res: Result{Order: *o}, err: ErrAlreadyClosed}
	}
	cancelled := *o
	cancelled.Status = StatusCancelled
	batch := Batch{Kind: KindCancel, Symbol: st.name, Order: cancelled}
	if err := e.commit(batch); err != nil {
		return reply{err: err}
	}
	st.b.applyCancel(id)
	e.afterApply(st, batch, e.cfg.Clock())
	return reply{res: Result{Order: cancelled}}
}

func (e *Engine) publish(st *symbolState) Depth {
	d := st.b.snapshot(e.cfg.DepthLevels)
	st.depth.Store(&d)
	return d
}

// afterApply publishes the snapshot and notifies sinks. Sink and price-callback panics are
// contained: the batch is already durable and applied, so they must not halt the symbol.
func (e *Engine) afterApply(st *symbolState, b Batch, now time.Time) {
	d := e.publish(st)
	func() {
		defer func() {
			if r := recover(); r != nil {
				e.cfg.Log.Error("engine: sink panicked (book unaffected)", "symbol", st.name, "panic", fmt.Sprint(r))
			}
		}()
		if e.cfg.Sink != nil {
			e.cfg.Sink.OnApplied(Applied{Batch: b, Depth: d, At: now})
		}
		if e.cfg.Prices != nil && len(b.Fills) > 0 {
			last := b.Fills[len(b.Fills)-1]
			e.cfg.Prices.PriceUpdate(st.name, last.Price, last.At)
		}
	}()
}
