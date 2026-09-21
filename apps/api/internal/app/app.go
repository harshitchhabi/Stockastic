// Package app is the platform core. It owns every component (matching engine, ledger, event clock, news,
// rate limiter, disputes, price tracker, WebSocket hub, durable log) and is the only place they are
// wired together, so the HTTP layer stays a thin translation of requests into calls on App.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"stockastic/api/internal/auth"
	"stockastic/api/internal/disputes"
	"stockastic/api/internal/dto"
	"stockastic/api/internal/engine"
	"stockastic/api/internal/eventclock"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/market"
	"stockastic/api/internal/money"
	"stockastic/api/internal/news"
	"stockastic/api/internal/ratelimit"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/store"
	"stockastic/api/internal/universe"
	"stockastic/api/internal/wsapi"
)

type Config struct {
	Rulebook       *rulebook.Rulebook
	Log            *slog.Logger
	WAL            store.Log
	Universe       []universe.Company
	Signer         *auth.Signer
	AllowSignup    bool
	AllowedOrigins []string
	// Autostart starts the event clock on boot if it has never been started (development only).
	Autostart bool
	Now       func() time.Time
}

type App struct {
	cfg Config
	RB  *rulebook.Rulebook
	log *slog.Logger
	wal store.Log
	now func() time.Time

	Engine   *engine.Engine
	Ledger   *ledger.Ledger
	Clock    *eventclock.Clock
	News     *news.Dispatcher
	Limiter  *ratelimit.Limiter
	Disputes *disputes.Tracker
	Market   *market.Tracker
	Hub      *wsapi.Hub
	Signer   *auth.Signer

	users     *userStore
	companies map[string]universe.Company
	symbols   []string
	started   time.Time
	dummyHash string

	// live indexes every working order by account, kept current from committed matching steps.
	liveMu sync.RWMutex
	live   map[string]map[string]engine.Order

	auditMu sync.Mutex
	audit   []AuditEntry

	ticketMu sync.Mutex
	tickets  map[string]*TicketRec

	// persistMu serialises writes of the event clock's state so an older snapshot cannot overwrite a newer one.
	persistMu sync.Mutex

	ordersPM, tradesPM perMinute
	commits            *durations
	journalErrors      atomic.Int64
	errs               *errRing

	haltMu    sync.Mutex
	haltSince map[string]time.Time

	// recentFills keeps each team's latest trades for the organiser's team page.
	fillMu      sync.Mutex
	recentFills map[string][]engine.Fill

	annMu         sync.Mutex
	announcements []Announcement

	pauseMu sync.RWMutex
	paused  map[string]bool

	lbMu    sync.Mutex
	lbAt    time.Time
	lbCache []dto.LeaderRow

	cancelRun context.CancelFunc
	runDone   chan struct{}
}

// New builds the platform and rebuilds its state from the durable log. It does not start matching.
func New(cfg Config) (*App, error) {
	if cfg.Rulebook == nil || cfg.WAL == nil || cfg.Signer == nil {
		return nil, errors.New("app: Rulebook, WAL and Signer are required")
	}
	if len(cfg.Universe) == 0 {
		return nil, errors.New("app: the company universe is empty")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	a := &App{
		cfg: cfg, RB: cfg.Rulebook, wal: cfg.WAL, now: cfg.Now, Signer: cfg.Signer,
		users: newUserStore(), companies: map[string]universe.Company{}, started: cfg.Now(),
		live: map[string]map[string]engine.Order{}, tickets: map[string]*TicketRec{},
		commits: newDurations(1000), errs: &errRing{}, haltSince: map[string]time.Time{},
		recentFills: map[string][]engine.Fill{}, paused: map[string]bool{},
	}
	base := cfg.Log
	if base == nil {
		base = slog.Default()
	}
	a.log = slog.New(captureHandler{inner: base.Handler(), ring: a.errs})

	for _, c := range cfg.Universe {
		a.companies[c.Symbol] = c
		a.symbols = append(a.symbols, c.Symbol)
	}
	sort.Strings(a.symbols)
	var err error
	if a.dummyHash, err = auth.HashPassword("not-a-real-password"); err != nil {
		return nil, err
	}

	a.Limiter = ratelimit.FromRulebook(a.RB.RateLimits)
	a.Ledger = ledger.New(a.log)
	a.Market = market.New(cfg.Universe, cfg.Now())
	a.Clock = eventclock.New(a.RB, cfg.Now, a.log)
	a.Disputes = disputes.New(disputes.FromRulebook(a.RB.Disputes))
	a.News = news.New(news.Config{Lead: a.RB.News.Lead(), Now: cfg.Now, Stage: a.stage, Log: a.log})
	a.Hub = wsapi.New(a.log, a.wsAuth, cfg.AllowedOrigins, a.onWSReady)

	a.Engine, err = engine.New(engine.Config{
		Symbols: a.symbols,
		Journal: store.Journal{Log: a.wal, Observe: a.observeCommit},
		Sink:    sink{a},
		Prices:  a.Market,
		Log:     a.log,
	})
	if err != nil {
		return nil, err
	}

	if err := a.restore(); err != nil {
		return nil, fmt.Errorf("app: rebuilding state from the log: %w", err)
	}
	a.News.Subscribe(a.onNews)
	a.Clock.OnTransition(a.onTransition)
	return a, nil
}

func (a *App) observeCommit(d time.Duration, err error) {
	a.commits.Add(d)
	if err != nil {
		a.journalErrors.Add(1)
		a.log.Error("journal commit failed", "err", err)
	}
}

// stage is the current event stage (phase 1 until the event starts).
func (a *App) stage() rulebook.Stage {
	pos := a.Clock.Position()
	if pos.Started && !pos.Ended && pos.Index >= 0 {
		return pos.Block.Stage
	}
	if pos.Ended {
		return rulebook.StageClosing
	}
	return rulebook.StagePhase1
}

// state is what the log says happened, folded into current values.
type state struct {
	orders map[string]engine.Order
	fills  []engine.Fill
}

// restore replays the log in the order things happened, so every fill, grant and correction lands on
// the ledger exactly as it did live (average cost depends on that order).
func (a *App) restore() error {
	st := state{orders: map[string]engine.Order{}}
	var clockState *eventclock.State
	releases := map[string]news.Release{}

	openAccount := func(u User) error {
		if u.IsAdmin {
			return nil
		}
		err := a.Ledger.Open(ledger.Account{ID: u.ID, Name: u.DisplayName, Kind: ledger.KindTeam}, a.RB.StartingCapital())
		if errors.Is(err, ledger.ErrAccountExists) {
			return nil
		}
		return err
	}

	err := a.wal.Replay(func(kind string, raw json.RawMessage) error {
		switch kind {
		case store.KindUser:
			var u User
			if err := json.Unmarshal(raw, &u); err != nil {
				return err
			}
			if err := a.users.put(u); err != nil {
				return err
			}
			return openAccount(u)
		case store.KindBatch:
			var b engine.Batch
			if err := json.Unmarshal(raw, &b); err != nil {
				return err
			}
			st.orders[b.Order.ID] = b.Order
			for _, m := range b.Makers {
				st.orders[m.ID] = m
			}
			for _, f := range b.Fills {
				a.Ledger.ReplayFill(f)
				a.Market.PriceUpdate(f.Symbol, f.Price, f.At)
				a.recordFill(f)
			}
			st.fills = append(st.fills, b.Fills...)
		case store.KindGrant:
			var g Grant
			if err := json.Unmarshal(raw, &g); err != nil {
				return err
			}
			if g.Qty > 0 {
				return a.Ledger.Grant(g.AccountID, g.Symbol, g.Qty, g.Price)
			}
			return a.Ledger.Revoke(g.AccountID, g.Symbol, -g.Qty, true)
		case store.KindCash:
			var c CashAdjustment
			if err := json.Unmarshal(raw, &c); err != nil {
				return err
			}
			return a.Ledger.AdjustCash(c.AccountID, money.Paise(c.Delta), true)
		case store.KindAnnounce:
			var n Announcement
			if err := json.Unmarshal(raw, &n); err != nil {
				return err
			}
			a.announcements = append(a.announcements, n)
		case store.KindPause:
			var p SymbolPause
			if err := json.Unmarshal(raw, &p); err != nil {
				return err
			}
			if p.Paused {
				a.paused[p.Symbol] = true
			} else {
				delete(a.paused, p.Symbol)
			}
		case store.KindAudit:
			var e AuditEntry
			if err := json.Unmarshal(raw, &e); err != nil {
				return err
			}
			a.audit = append(a.audit, e)
		case store.KindClock:
			var s eventclock.State
			if err := json.Unmarshal(raw, &s); err != nil {
				return err
			}
			clockState = &s
		case store.KindNews:
			var r news.Release
			if err := json.Unmarshal(raw, &r); err != nil {
				return err
			}
			releases[r.Item.ID] = r
		case store.KindTicket:
			var t TicketRec
			if err := json.Unmarshal(raw, &t); err != nil {
				return err
			}
			a.tickets[t.Ticket.ID] = &t
		}
		return nil
	})
	if err != nil {
		return err
	}

	var liveOrders, allOrders []engine.Order
	for _, o := range st.orders {
		allOrders = append(allOrders, o)
		if o.Status.Live() {
			liveOrders = append(liveOrders, o)
			a.indexLive(o)
		}
	}
	a.Ledger.RestoreReservations(liveOrders)
	byTaker := map[string][]engine.Fill{}
	for _, f := range st.fills {
		byTaker[f.TakerOrderID] = append(byTaker[f.TakerOrderID], f)
	}
	if err := a.Engine.Restore(allOrders, byTaker); err != nil {
		return err
	}

	if clockState != nil {
		if err := a.Clock.Restore(*clockState); err != nil {
			return err
		}
	}
	if a.Clock.Overrides().Frozen {
		a.Engine.Freeze()
	}
	rel := make([]news.Release, 0, len(releases))
	for _, r := range releases {
		rel = append(rel, r)
	}
	a.News.Recover(rel)
	a.Disputes.Restore(a.ticketList())
	a.log.Info("state rebuilt from the log", "users", len(a.users.all()), "orders", len(allOrders), "live", len(liveOrders), "fills", len(st.fills))
	return nil
}

// Start begins matching and the event clock.
func (a *App) Start() error {
	a.Engine.Start()
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelRun = cancel
	a.runDone = make(chan struct{})
	go func() {
		defer close(a.runDone)
		a.Clock.Run(ctx, time.Second)
	}()
	if a.cfg.Autostart && !a.Clock.Position().Started {
		if err := a.Clock.Start(); err != nil {
			return err
		}
		a.persistClock()
		a.log.Warn("event clock auto-started (AUTOSTART is on; development only)")
	}
	return nil
}

// Close stops matching (finishing what is queued), closes sockets and the log.
func (a *App) Close(ctx context.Context) error {
	if a.cancelRun != nil {
		a.cancelRun()
		<-a.runDone
	}
	a.Hub.Shutdown()
	err := a.Engine.Stop(ctx)
	a.News.Stop()
	if cerr := a.wal.Close(); err == nil {
		err = cerr
	}
	return err
}

// ---- live-order index ----

func (a *App) indexLive(o engine.Order) {
	a.liveMu.Lock()
	defer a.liveMu.Unlock()
	if !o.Status.Live() {
		if m := a.live[o.AccountID]; m != nil {
			delete(m, o.ID)
			if len(m) == 0 {
				delete(a.live, o.AccountID)
			}
		}
		return
	}
	if a.live[o.AccountID] == nil {
		a.live[o.AccountID] = map[string]engine.Order{}
	}
	a.live[o.AccountID][o.ID] = o
}

// LiveOrders are an account's working orders, oldest first.
func (a *App) LiveOrders(accountID string) []engine.Order {
	a.liveMu.RLock()
	out := make([]engine.Order, 0, len(a.live[accountID]))
	for _, o := range a.live[accountID] {
		out = append(out, o)
	}
	a.liveMu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (a *App) openOrderCount() (n int) {
	a.liveMu.RLock()
	defer a.liveMu.RUnlock()
	for _, m := range a.live {
		n += len(m)
	}
	return n
}

// ---- engine sink: what happens after a matching step is durable ----

type sink struct{ a *App }

func (s sink) OnApplied(ap engine.Applied) {
	a := s.a
	a.Ledger.OnApplied(ap)
	now := a.now()

	a.indexLive(ap.Order)
	for _, m := range ap.Makers {
		a.indexLive(m)
	}

	switch ap.Kind {
	case engine.KindSubmit:
		a.ordersPM.Add(1, now)
		a.Hub.ToAccount(ap.Order.AccountID, "orderAccepted", dto.FromOrder(ap.Order))
		for _, f := range ap.Fills {
			a.tradesPM.Add(1, now)
			a.recordFill(f)
			// Trades are tiny and everyone needs them for prices, so they go to all clients; the much bigger
			// order book below goes only to people looking at that company.
			a.Hub.ToAll("trade", dto.FromPublicFill(f))
			private := dto.FromFill(f)
			a.Hub.ToAccount(f.TakerAccountID, "fill", private)
			if f.MakerAccountID != f.TakerAccountID {
				a.Hub.ToAccount(f.MakerAccountID, "fill", private)
			}
		}
	case engine.KindCancel:
		a.Hub.ToAccount(ap.Order.AccountID, "orderCancelled", dto.FromOrder(ap.Order))
	}
	a.Hub.ToSymbol(ap.Symbol, "bookUpdate", dto.FromDepth(ap.Depth))
}

// ---- websocket ----

func (a *App) wsAuth(token string) (wsapi.Identity, bool) {
	id, err := a.Signer.Parse(token)
	if err != nil {
		return wsapi.Identity{}, false
	}
	u, ok := a.users.get(id)
	if !ok || u.Status == StatusDisqualified {
		return wsapi.Identity{}, false
	}
	role := u.Role
	if u.IsAdmin {
		role = "admin"
	}
	return wsapi.Identity{AccountID: u.ID, Role: role}, true
}

func (a *App) onWSReady(c *wsapi.Client) { a.Hub.Send(c, "controlState", a.ControlState()) }

// ControlState is the live organiser state pushed to every client.
type ControlState struct {
	TradingFrozen   bool              `json:"tradingFrozen"`
	MarketOpen      bool              `json:"marketOpen"`
	WindowOverrides map[string]string `json:"windowOverrides"`
	PausedSymbols   []string          `json:"pausedSymbols"`
}

func (a *App) ControlState() ControlState {
	ov := a.Clock.Overrides()
	cs := ControlState{TradingFrozen: ov.Frozen, MarketOpen: a.Clock.MarketOpen(), WindowOverrides: map[string]string{}, PausedSymbols: a.PausedSymbols()}
	for i, w := range ov.Windows {
		if w != nil {
			cs.WindowOverrides[fmt.Sprintf("Allocation window %d", i)] = map[bool]string{true: "open", false: "closed"}[*w]
		}
	}
	return cs
}

func (a *App) broadcastControl() { a.Hub.ToAll("controlState", a.ControlState()) }

func (a *App) onTransition(t eventclock.Transition) {
	a.log.Info("event block", "index", t.Index, "block", t.Block.ID)
	a.persistClock()
	a.broadcastControl()
}

func (a *App) persistClock() {
	a.persistMu.Lock()
	defer a.persistMu.Unlock()
	if err := a.wal.Append(store.KindClock, a.Clock.Snapshot()); err != nil {
		a.log.Error("could not persist the event clock", "err", err)
	}
}

// ---- news ----

func (a *App) onNews(d news.Delivery) {
	item := dto.NewsItem{ID: d.Item.ID, Kind: string(d.Item.Kind), Headline: d.Item.Headline, Body: d.Item.Body, CreatedAt: dto.MS(d.Item.CreatedAt)}
	if d.Feed == news.FundManagerFeed {
		a.Hub.ToRole(RoleFundManager, "news", item)
	} else {
		a.Hub.ToRole(RoleInvestor, "news", item)
	}
	for _, r := range a.News.Releases() {
		if r.Item.ID == d.Item.ID {
			if err := a.wal.Append(store.KindNews, r); err != nil {
				a.log.Error("could not persist a news release", "err", err)
			}
			break
		}
	}
}

// NewsFor is the news history the account is allowed to see (fund managers see their early feed).
func (a *App) NewsFor(u User) []dto.NewsItem {
	feed := news.PublicFeed
	if u.Role == RoleFundManager {
		feed = news.FundManagerFeed
	}
	items := a.News.History(feed)
	out := make([]dto.NewsItem, 0, len(items))
	for _, it := range items {
		out = append(out, dto.NewsItem{ID: it.ID, Kind: string(it.Kind), Headline: it.Headline, Body: it.Body, CreatedAt: dto.MS(it.CreatedAt)})
	}
	a.annMu.Lock()
	for _, n := range a.announcements {
		out = append(out, dto.NewsItem{ID: n.ID, Kind: "notice", Headline: n.Text, CreatedAt: dto.MS(n.At)})
	}
	a.annMu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}
