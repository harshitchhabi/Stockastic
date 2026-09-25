// Package app is the platform core. It owns every component (trade executor, ledger, event clock, news,
// price simulation, rate limiter, WebSocket hub, durable log) and is the only place they are wired
// together, so the HTTP layer stays a thin translation of requests into calls on App.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"stockastic/api/internal/auth"
	"stockastic/api/internal/disputes"
	"stockastic/api/internal/dto"
	"stockastic/api/internal/eventclock"
	"stockastic/api/internal/funds"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/market"
	"stockastic/api/internal/money"
	"stockastic/api/internal/news"
	"stockastic/api/internal/ratelimit"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/sim"
	"stockastic/api/internal/store"
	"stockastic/api/internal/trading"
	"stockastic/api/internal/universe"
	"stockastic/api/internal/wsapi"
)

type Config struct {
	Rulebook    *rulebook.Rulebook
	Log         *slog.Logger
	WAL         store.Log
	Universe    []universe.Company
	Scenario    sim.Scenario
	Signer      *auth.Signer
	AllowSignup bool
	// MaxAccounts caps how many accounts can exist (0 means no cap). It bounds the damage of a sign-up flood.
	MaxAccounts int
	// MaxSockets and MaxSocketsPerAccount cap live connections (0 means the built-in defaults).
	MaxSockets, MaxSocketsPerAccount int
	// SignupCode, if set, must be entered to register. Organisers can change it while the event runs.
	SignupCode     string
	AllowedOrigins []string
	// Autostart starts the event clock on boot if it has never been started (development only).
	Autostart bool
	// Disk, if set, refuses new trades while the disk is nearly full and is reported on the Systems page.
	Disk *store.DiskGuard
	// Track turns on the activity and wallet-history records (used with PostgreSQL). History reads them back, and
	// Health reports whether the database is still accepting writes.
	Track   bool
	History store.History
	Health  func() error
	Now     func() time.Time
}

type App struct {
	cfg Config
	RB  *rulebook.Rulebook
	log *slog.Logger
	wal store.Log
	now func() time.Time

	Exec    *trading.Executor
	Ledger  *ledger.Ledger
	Clock   *eventclock.Clock
	News    *news.Dispatcher
	Limiter *ratelimit.Limiter
	// fundOps limits how often one account can put money into or take it out of funds (30 a minute).
	fundOps *ratelimit.Limiter
	// profileAt is when each fund last changed its profile.
	profileAt map[string]time.Time
	Dispute   disputes.Config
	Market    *market.Prices
	Sim       *sim.Engine
	Funds     *funds.Book
	Hub       *wsapi.Hub
	Signer    *auth.Signer

	users     *userStore
	companies map[string]universe.Company
	symbols   []string
	started   time.Time
	dummyHash string

	auditMu sync.Mutex
	audit   []AuditEntry

	ticketMu sync.Mutex
	tickets  map[string]*TicketRec

	// persistMu serialises writes of the event clock's state so an older snapshot cannot overwrite a newer one.
	persistMu sync.Mutex

	// per-team records kept for the organiser: recent trades, Phase 1 trade counts and peak portfolio value
	// (the qualification tie-breaks), and the frozen results taken at each freeze block.
	statMu     sync.Mutex
	allTrades  []trading.Trade // every trade of the event, for the organiser
	signupOpen atomic.Bool
	signupCode atomic.Value // string
	// allowed, if not empty, is the list of people who may register (by canonical email).
	allowMu   sync.RWMutex
	allowed   map[string]bool
	recent    map[string][]trading.Trade
	p1Trades  map[string]int
	peaks     map[string]money.Paise
	snapshots map[string]FreezeSnapshot

	annMu         sync.Mutex
	announcements []Announcement

	pauseMu sync.RWMutex
	paused  map[string]bool

	// evMu keeps a reset of the event from running in the middle of a trade or a fund operation: those hold it
	// for reading, a reset holds it for writing.
	evMu sync.RWMutex
	// fundMu serialises fund operations (allocations, redemptions, checkpoints) so units and NAV stay consistent.
	fundMu sync.Mutex

	presMu sync.Mutex
	pres   map[string]presence

	tradesPM      perMinute
	commits       *durations
	journalErrors atomic.Int64
	lastTick      atomic.Int64
	errs          *errRing

	lbMu    sync.Mutex
	lbAt    time.Time
	lbCache []dto.LeaderRow

	simState  *sim.State
	tracker   *tracker
	cancelRun context.CancelFunc
	runDone   chan struct{}
}

// New builds the platform and rebuilds its state from the durable log. It does not start the clocks.
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
		tickets: map[string]*TicketRec{}, commits: newDurations(1000), errs: &errRing{},
		recent: map[string][]trading.Trade{}, p1Trades: map[string]int{}, peaks: map[string]money.Paise{},
		snapshots: map[string]FreezeSnapshot{}, paused: map[string]bool{}, pres: map[string]presence{},
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
	a.fundOps = ratelimit.New(30, time.Minute)
	a.profileAt = map[string]time.Time{}
	a.signupOpen.Store(cfg.AllowSignup)
	a.signupCode.Store(strings.TrimSpace(cfg.SignupCode))
	a.Ledger = ledger.New(a.log)
	a.Funds = funds.NewBook(a.RB.Fund.LaunchNav)
	a.Market = market.New(cfg.Universe, cfg.Now())
	a.Clock = eventclock.New(a.RB, cfg.Now, a.log)
	a.Dispute = disputes.FromRulebook(a.RB.Disputes)
	a.News = news.New(news.Config{Lead: a.RB.News.Lead(), Now: cfg.Now, Stage: a.stage, Log: a.log})
	a.Hub = wsapi.New(a.log, a.wsAuth, cfg.AllowedOrigins, a.onWSReady)
	a.Hub.OnPresence(a.onPresence)
	a.Hub.SetLimits(cfg.MaxSockets, cfg.MaxSocketsPerAccount)
	a.Exec = trading.New(a.Ledger, a.Market, store.Journal{Log: a.wal, Guard: cfg.Disk, Observe: a.observeCommit}, cfg.Now)
	a.Exec.SetGuard(a.concentrationGuard)

	a.Sim, err = sim.New(cfg.Scenario, sim.Deps{
		Prices: a.Market, Companies: cfg.Universe, Clock: clockView{a}, News: a.News, Lead: a.newsLead,
		Save: a.saveSim, OnPrices: a.onPrices, Log: a.log,
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

// CompactRules say which log records a newer one replaces, so the log can be tidied at every start without
// losing anything: only superseded copies of accounts, clock state, news, disputes, company pauses and old
// price records are dropped. Trades, grants, cash changes, announcements, snapshots and the audit log are
// always kept in full.
func CompactRules() map[string]store.Rule {
	field := func(path ...string) func(json.RawMessage) string {
		return func(raw json.RawMessage) string {
			var m map[string]any
			if json.Unmarshal(raw, &m) != nil {
				return ""
			}
			var cur any = m
			for _, p := range path {
				mm, ok := cur.(map[string]any)
				if !ok {
					return ""
				}
				cur = mm[p]
			}
			s, _ := cur.(string)
			return s
		}
	}
	return map[string]store.Rule{
		store.KindUser:    {Latest: 1, Key: field("ID")},
		store.KindClock:   {Latest: 1},
		store.KindNews:    {Latest: 1, Key: field("Item", "ID")},
		store.KindTicket:  {Latest: 1, Key: field("Ticket", "ID")},
		store.KindPause:   {Latest: 1, Key: field("symbol")},
		store.KindSetting: {Latest: 1, Key: field("key")},
		// The price history the charts show only needs the most recent records.
		store.KindPrices: {Latest: market.MaxHistory},
	}
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

// newsLead is how long after fund managers the public sees news: only in Phase 2 (Section 11).
func (a *App) newsLead() time.Duration {
	if a.stage() == rulebook.StagePhase2 {
		return a.RB.News.Lead()
	}
	return 0
}

// clockView shows the event clock to the price simulation.
type clockView struct{ a *App }

func (c clockView) Elapsed() (time.Duration, bool) {
	p := c.a.Clock.Position()
	return p.Elapsed, p.Started
}
func (c clockView) MarketOpen() bool      { return c.a.Clock.MarketOpen() }
func (c clockView) Stage() rulebook.Stage { return c.a.stage() }

// ---- prices ----

func (a *App) saveSim(s sim.State) error { return a.wal.Append(store.KindPrices, s) }

// onPrices runs after every price change: it tells every browser and keeps the peak values current.
func (a *App) onPrices(at time.Time, changed map[string]money.Paise) {
	a.lastTick.Store(at.UnixMilli())
	up := dto.PricesUpdate{At: dto.MS(at), Prices: make([]dto.PriceTick, 0, len(changed))}
	syms := make([]string, 0, len(changed))
	for s := range changed {
		syms = append(syms, s)
	}
	sort.Strings(syms)
	for _, s := range syms {
		up.Prices = append(up.Prices, dto.PriceTick{Symbol: s, Price: dto.Rupees(changed[s])})
	}
	a.Hub.ToAll("prices", up)
	a.updatePeaks()
}

// ---- freeze snapshots and peak values ----

// FreezeSnapshot is every team's result at a freeze block, taken from the freeze-time prices so the
// ranking cannot drift afterwards (Sections 4 and 24).
type FreezeSnapshot struct {
	Name    string
	At      time.Time
	Prices  map[string]int64
	Values  map[string]int64   // portfolio value per team, fund units included
	FundNAV map[string]float64 // each fund's NAV per unit at the freeze
	Peaks   map[string]int64   // highest portfolio value seen so far (a tie-break)
	Trades  map[string]int     // trades made in Phase 1 (a tie-break)
}

func (a *App) teamIDs() []string {
	var ids []string
	for _, u := range a.users.all() {
		if !u.IsAdmin {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

// updatePeaks records each team's highest portfolio value so far.
func (a *App) updatePeaks() {
	for _, id := range a.teamIDs() {
		a.updatePeakFor(id)
	}
}

func (a *App) takeSnapshot(name string) {
	a.statMu.Lock()
	_, done := a.snapshots[name]
	a.statMu.Unlock()
	if done {
		return
	}
	a.updatePeaks()
	navs := a.navs()
	snap := FreezeSnapshot{FundNAV: navs, Name: name, At: a.now(), Prices: map[string]int64{}, Values: map[string]int64{}, Peaks: map[string]int64{}, Trades: map[string]int{}}
	for k, v := range a.Market.All() {
		snap.Prices[k] = int64(v)
	}
	for _, id := range a.teamIDs() {
		snap.Values[id] = int64(a.totalValue(id, navs))
	}
	a.statMu.Lock()
	for id, p := range a.peaks {
		snap.Peaks[id] = int64(p)
	}
	for id, n := range a.p1Trades {
		snap.Trades[id] = n
	}
	a.snapshots[name] = snap
	a.statMu.Unlock()
	if err := a.wal.Append(store.KindSnapshot, snap); err != nil {
		a.log.Error("could not save a freeze snapshot", "name", name, "err", err)
		return
	}
	a.log.Info("freeze snapshot taken", "name", name, "teams", len(snap.Values))
}

// Snapshot returns a freeze snapshot by name ("phase1" or "final"), if one has been taken.
func (a *App) Snapshot(name string) (FreezeSnapshot, bool) {
	a.statMu.Lock()
	defer a.statMu.Unlock()
	s, ok := a.snapshots[name]
	return s, ok
}

func (a *App) recordTrade(t trading.Trade) {
	a.statMu.Lock()
	a.allTrades = append(a.allTrades, t)
	l := append(a.recent[t.AccountID], t)
	if len(l) > 100 {
		l = append(l[:0], l[len(l)-100:]...)
	}
	a.recent[t.AccountID] = l
	if t.Stage == string(rulebook.StagePhase1) {
		a.p1Trades[t.AccountID]++
	}
	a.statMu.Unlock()
}

// ---- restore ----

// restore replays the log in the order things happened, so every trade, grant and correction lands on the
// ledger exactly as it did live (average cost depends on that order).
func (a *App) restore() error {
	var clockState *eventclock.State
	releases := map[string]news.Release{}
	trades := 0

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
	decode := func(raw json.RawMessage, v any) error { return json.Unmarshal(raw, v) }

	err := a.wal.Replay(func(kind string, raw json.RawMessage) error {
		switch kind {
		case store.KindUser:
			var u User
			if err := decode(raw, &u); err != nil {
				return err
			}
			if err := a.users.put(u); err != nil {
				return err
			}
			return openAccount(u)
		case store.KindTrade:
			var t trading.Trade
			if err := decode(raw, &t); err != nil {
				return err
			}
			if err := a.Ledger.ReplayTrade(t.AccountID, t.Symbol, t.Side == trading.Buy, t.Qty, t.Price); err != nil {
				return fmt.Errorf("replaying trade %s: %w", t.ID, err)
			}
			a.Exec.Seed(t)
			a.recordTrade(t)
			trades++
		case store.KindPrices:
			var s sim.State
			if err := decode(raw, &s); err != nil {
				return err
			}
			p := make(map[string]money.Paise, len(s.Prices))
			for k, v := range s.Prices {
				p[k] = money.Paise(v)
			}
			a.Market.SetAll(p, s.At)
			a.updatePeaks()
			st := s
			a.simState = &st
		case store.KindSetting:
			var st Setting
			if err := decode(raw, &st); err != nil {
				return err
			}
			switch st.Key {
			case settingSignup:
				a.signupOpen.Store(st.Value)
			case settingCode:
				a.signupCode.Store(st.Text)
			case settingAllow:
				a.setAllowed(st.Text)
			}
		case store.KindReset:
			a.resetState()
			clockState, releases, trades = nil, map[string]news.Release{}, 0
		case store.KindFund:
			var ev funds.Event
			if err := decode(raw, &ev); err != nil {
				return err
			}
			if err := a.applyFundEvent(ev, true); err != nil {
				return fmt.Errorf("replaying a fund event (%s): %w", ev.Op, err)
			}
		case store.KindSnapshot:
			var s FreezeSnapshot
			if err := decode(raw, &s); err != nil {
				return err
			}
			a.snapshots[s.Name] = s
		case store.KindGrant:
			var g Grant
			if err := decode(raw, &g); err != nil {
				return err
			}
			if g.Qty > 0 {
				return a.Ledger.Grant(g.AccountID, g.Symbol, g.Qty, g.Price)
			}
			return a.Ledger.Revoke(g.AccountID, g.Symbol, -g.Qty, true)
		case store.KindCash:
			var c CashAdjustment
			if err := decode(raw, &c); err != nil {
				return err
			}
			return a.Ledger.AdjustCash(c.AccountID, money.Paise(c.Delta), true)
		case store.KindAnnounce:
			var n Announcement
			if err := decode(raw, &n); err != nil {
				return err
			}
			a.announcements = append(a.announcements, n)
		case store.KindPause:
			var p SymbolPause
			if err := decode(raw, &p); err != nil {
				return err
			}
			if p.Paused {
				a.paused[p.Symbol] = true
			} else {
				delete(a.paused, p.Symbol)
			}
		case store.KindAudit:
			var e AuditEntry
			if err := decode(raw, &e); err != nil {
				return err
			}
			a.audit = append(a.audit, e)
		case store.KindClock:
			var s eventclock.State
			if err := decode(raw, &s); err != nil {
				return err
			}
			clockState = &s
		case store.KindNews:
			var r news.Release
			if err := decode(raw, &r); err != nil {
				return err
			}
			releases[r.Item.ID] = r
		case store.KindTicket:
			var t TicketRec
			if err := decode(raw, &t); err != nil {
				return err
			}
			a.tickets[t.Ticket.ID] = &t
		}
		return nil
	})
	if err != nil {
		return err
	}
	if a.simState != nil {
		a.Sim.Adopt(*a.simState)
	}
	if clockState != nil {
		if err := a.Clock.Restore(*clockState); err != nil {
			return err
		}
	}
	rel := make([]news.Release, 0, len(releases))
	for _, r := range releases {
		rel = append(rel, r)
	}
	a.News.Recover(rel)
	a.log.Info("state rebuilt from the log", "users", len(a.users.all()), "trades", trades)
	return nil
}

// Start begins the event clock and the price simulation.
func (a *App) Start() error {
	ctx, cancel := context.WithCancel(context.Background())
	a.cancelRun = cancel
	a.runDone = make(chan struct{})
	go func() {
		defer close(a.runDone)
		a.Clock.Run(ctx, time.Second)
	}()
	go a.Sim.Run(ctx)
	go a.fundLoop(ctx)
	if a.cfg.Track {
		a.tracker = &tracker{ch: make(chan store.Activity, trackQueue), last: map[string]time.Time{}}
		a.startTracking(ctx)
	}
	if a.cfg.Autostart && !a.Clock.Position().Started {
		if err := a.Clock.Start(); err != nil {
			return err
		}
		a.persistClock()
		a.log.Warn("event clock auto-started (AUTOSTART is on; development only)")
	}
	return nil
}

// Close stops the clocks, closes sockets and the log.
func (a *App) Close(ctx context.Context) error {
	if a.cancelRun != nil {
		a.cancelRun()
		<-a.runDone
	}
	if a.tracker != nil {
		a.tracker.wg.Wait()
	}
	a.Hub.Shutdown()
	a.News.Stop()
	return a.wal.Close()
}

// ---- websocket ----

func (a *App) wsAuth(token string) (wsapi.Identity, bool) {
	id, ver, err := a.Signer.Verify(token)
	if err != nil {
		return wsapi.Identity{}, false
	}
	u, ok := a.users.get(id)
	if !ok || u.Status == StatusDisqualified || !u.sessionOK(ver) {
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
	if t.Block.FreezeSnapshot != "" {
		a.takeSnapshot(t.Block.FreezeSnapshot)
	}
	if t.Index > 0 {
		// A window that has just closed is a checkpoint (Section 12), and so is the final close.
		if blocks := a.Clock.Blocks(); t.Index-1 < len(blocks) && blocks[t.Index-1].AllocationWindow != nil {
			prev := blocks[t.Index-1]
			a.takeCheckpoint(fmt.Sprintf("window %d", *prev.AllocationWindow))
		}
	}
	if t.Block.FreezeSnapshot == "final" {
		a.takeCheckpoint("final")
	}
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
