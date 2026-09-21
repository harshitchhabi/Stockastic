package app

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"stockastic/api/internal/disputes"
	"stockastic/api/internal/dto"
	"stockastic/api/internal/ids"
	"stockastic/api/internal/money"
	"stockastic/api/internal/news"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/sim"
	"stockastic/api/internal/store"
	"stockastic/api/internal/trading"
)

// ---- audit ----

// AuditEntry records one organiser action: who did what to what, and why.
type AuditEntry struct {
	ID     string    `json:"id"`
	At     time.Time `json:"at"`
	Actor  string    `json:"actor"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Reason string    `json:"reason"`
	OK     bool      `json:"ok"`
}

// tidyReason cleans an optional note. The console only asks for confirmation, so a reason is usually
// empty; an API caller that supplies one has it kept in the audit entry.
func tidyReason(reason string) string {
	r := strings.TrimSpace(reason)
	if rs := []rune(r); len(rs) > 500 {
		r = string(rs[:500])
	}
	return r
}

// Audit writes one entry. The entry is stored before the caller reports success.
func (a *App) Audit(actor User, action, target, reason string, ok bool) {
	e := AuditEntry{ID: ids.New(), At: a.now(), Actor: actor.DisplayName, Action: action, Target: target, Reason: reason, OK: ok}
	if err := a.wal.Append(store.KindAudit, e); err != nil {
		a.log.Error("could not persist an audit entry", "err", err, "action", action)
	}
	a.auditMu.Lock()
	a.audit = append(a.audit, e)
	a.auditMu.Unlock()
}

func (a *App) AuditLog() []AuditEntry {
	a.auditMu.Lock()
	defer a.auditMu.Unlock()
	out := append([]AuditEntry(nil), a.audit...)
	sort.Slice(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out
}

// Do runs one organiser action and audits the outcome either way: who did it, what, to what, and whether it worked.
func (a *App) Do(actor User, action, target, reason string, f func() error) error {
	err := f()
	a.Audit(actor, action, target, tidyReason(reason), err == nil)
	return err
}

// ---- clock and switches ----

func (a *App) ClockStart(actor User, reason string) error {
	return a.Do(actor, "Started the event", "clock", reason, func() error {
		if err := a.Clock.Start(); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

func (a *App) ClockPause(actor User, reason string) error {
	return a.Do(actor, "Paused the event", "clock", reason, func() error {
		if err := a.Clock.Pause(); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

func (a *App) ClockResume(actor User, reason, compressBlockID string) error {
	target := "clock"
	if compressBlockID != "" {
		target = "clock, shortening " + compressBlockID
	}
	return a.Do(actor, "Resumed the event", target, reason, func() error {
		if _, _, err := a.Clock.Resume(compressBlockID); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

func (a *App) ClockNudge(actor User, reason string, minutes int) error {
	if minutes < -60 || minutes > 60 || minutes == 0 {
		return bad("invalid_minutes", "Move the clock by 1 to 60 minutes.")
	}
	return a.Do(actor, fmt.Sprintf("Moved the clock %+d min", minutes), "clock", reason, func() error {
		if err := a.Clock.Nudge(time.Duration(minutes) * time.Minute); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

func (a *App) ClockJump(actor User, reason, blockID string) error {
	return a.Do(actor, "Jumped the clock", blockID, reason, func() error {
		if err := a.Clock.JumpTo(blockID); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

func (a *App) SetFrozen(actor User, reason string, frozen bool) error {
	action := "Resumed trading"
	if frozen {
		action = "Froze trading"
	}
	return a.Do(actor, action, "all symbols", reason, func() error {
		a.Clock.SetFrozen(frozen)
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

func (a *App) SetMarketOverride(actor User, reason string, open *bool) error {
	return a.Do(actor, "Market override: "+overrideName(open), "market", reason, func() error {
		a.Clock.SetMarketOverride(open)
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

func (a *App) SetWindowOverride(actor User, reason string, w int, open *bool) error {
	return a.Do(actor, fmt.Sprintf("Window %d override: %s", w, overrideName(open)), fmt.Sprintf("window %d", w), reason, func() error {
		if err := a.Clock.SetWindowOverride(w, open); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

func overrideName(o *bool) string {
	switch {
	case o == nil:
		return "schedule"
	case *o:
		return "open"
	}
	return "closed"
}

// ---- accounts ----

type AdminAccount struct {
	ID             string  `json:"id"`
	DisplayName    string  `json:"displayName"`
	Email          string  `json:"email"`
	Role           string  `json:"role"`
	IsAdmin        bool    `json:"isAdmin"`
	Status         string  `json:"status"`
	Warnings       int     `json:"warnings"`
	CashBalance    float64 `json:"cashBalance"`
	PortfolioValue float64 `json:"portfolioValue"`
	Positions      int     `json:"positions"`
	Locked         bool    `json:"locked"`
	Online         bool    `json:"online"`
	Sockets        int     `json:"sockets"`
	LastSeen       int64   `json:"lastSeen"`
}

func (a *App) AdminAccounts() []AdminAccount {
	users := a.users.all()
	out := make([]AdminAccount, 0, len(users))
	navs := a.navs()
	for _, u := range users {
		if u.IsAdmin {
			continue
		}
		row := AdminAccount{ID: u.ID, DisplayName: u.DisplayName, Email: u.Email, Role: u.Role, IsAdmin: u.IsAdmin, Status: u.Status, Warnings: u.Warnings}
		row.Locked = u.Locked
		row.Sockets = a.Hub.Sockets(u.ID)
		row.Online, row.LastSeen = a.presenceOf(u.ID)
		if snap, err := a.Ledger.Snapshot(u.ID); err == nil {
			row.CashBalance = dto.Rupees(snap.Cash)
			row.Positions = len(snap.Positions)
		}
		row.PortfolioValue = dto.Rupees(a.totalValue(u.ID, navs))
		out = append(out, row)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PortfolioValue > out[j].PortfolioValue })
	return out
}

func (a *App) Promote(actor User, reason, id string) error {
	u, ok := a.users.get(id)
	if !ok {
		return ErrUnknownUser
	}
	return a.Do(actor, "Promoted to fund manager", u.DisplayName, reason, func() error {
		_, err := a.updateUser(id, func(x *User) error { x.Role = RoleFundManager; return nil })
		return err
	})
}

func (a *App) Warn(actor User, reason, id string) error {
	u, ok := a.users.get(id)
	if !ok {
		return ErrUnknownUser
	}
	return a.Do(actor, "Formal warning", u.DisplayName, reason, func() error {
		_, err := a.updateUser(id, func(x *User) error {
			x.Warnings++
			if x.Status == StatusActive {
				x.Status = StatusWarned
			}
			return nil
		})
		return err
	})
}

// Disqualify removes a team from trading. Its trades so far stand (trades are final).
func (a *App) Disqualify(actor User, reason, id string) error {
	u, ok := a.users.get(id)
	if !ok {
		return ErrUnknownUser
	}
	return a.Do(actor, "Disqualified", u.DisplayName, reason, func() error {
		_, err := a.updateUser(id, func(x *User) error { x.Status = StatusDisqualified; return nil })
		return err
	})
}

// ---- news ----

type NewsRelease struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	Headline      string `json:"headline"`
	Body          string `json:"body,omitempty"`
	CreatedAt     int64  `json:"createdAt"`
	FundManagerAt int64  `json:"fundManagerAt"`
	PublicAt      int64  `json:"publicAt"`
}

type NewsDesk struct {
	PublicDelaySeconds int           `json:"publicDelaySeconds"`
	Items              []NewsRelease `json:"items"`
}

func (a *App) NewsDesk() NewsDesk {
	rels := a.News.Releases()
	d := NewsDesk{PublicDelaySeconds: a.RB.News.FundManagerLeadSeconds, Items: make([]NewsRelease, 0, len(rels))}
	for _, r := range rels {
		d.Items = append(d.Items, NewsRelease{
			ID: r.Item.ID, Kind: string(r.Item.Kind), Headline: r.Item.Headline, Body: r.Item.Body,
			CreatedAt: dto.MS(r.Item.CreatedAt), FundManagerAt: dto.MS(r.FundManagerAt), PublicAt: dto.MS(r.PublicAt),
		})
		// A public release still waiting is shown with the time it is expected.
		if r.PublicAt.IsZero() && r.Item.Kind == news.KindNews {
			d.Items[len(d.Items)-1].PublicAt = dto.MS(r.Item.CreatedAt.Add(a.RB.News.Lead()))
		}
	}
	sort.Slice(d.Items, func(i, j int) bool { return d.Items[i].CreatedAt > d.Items[j].CreatedAt })
	return d
}

func (a *App) PublishNews(actor User, reason, kind, headline, body string) error {
	headline, body = strings.TrimSpace(headline), strings.TrimSpace(body)
	if n := len([]rune(headline)); n < 3 || n > 200 {
		return bad("invalid_headline", "A headline is 3 to 200 characters.")
	}
	if len(body) > 4000 {
		return bad("invalid_body", "The detail is too long.")
	}
	var k news.Kind
	switch kind {
	case "news":
		k = news.KindNews
	case "regime":
		k = news.KindRegime
	default:
		return bad("invalid_kind", "Kind must be news or regime.")
	}
	return a.Do(actor, "Published "+kind, headline, reason, func() error {
		a.News.Publish(k, headline, body)
		return nil
	})
}

// ---- disputes ----

// TicketRec is a dispute as stored: the ticket plus who raised it and how it ended.
type TicketRec struct {
	Ticket      disputes.Ticket
	AccountName string
	Status      string
	Resolution  string
}

func (a *App) saveTicket(t *TicketRec) error { return a.wal.Append(store.KindTicket, t) }

// RaiseDispute lets a team file a dispute (Section 22). It is never refused for being late.
func (a *App) RaiseDispute(u User, category, summary string, incidentAt time.Time) (TicketRec, error) {
	summary = strings.TrimSpace(summary)
	if n := len([]rune(summary)); n < 10 || n > 1000 {
		return TicketRec{}, bad("invalid_summary", "Describe the issue in 10 to 1000 characters.")
	}
	cat := disputes.Category(category)
	if !cat.Valid() {
		return TicketRec{}, bad("invalid_category", "Unknown dispute category.")
	}
	now := a.now()
	if incidentAt.IsZero() || incidentAt.After(now) {
		incidentAt = now
	}
	rec := &TicketRec{Ticket: a.Dispute.Raise(u.ID, cat, summary, incidentAt, now), AccountName: u.DisplayName, Status: "open"}
	if err := a.saveTicket(rec); err != nil {
		return TicketRec{}, err
	}
	a.ticketMu.Lock()
	a.tickets[rec.Ticket.ID] = rec
	a.ticketMu.Unlock()
	return *rec, nil
}

type AdminTicket struct {
	ID          string `json:"id"`
	AccountName string `json:"accountName"`
	Category    string `json:"category"`
	Summary     string `json:"summary"`
	RaisedAt    int64  `json:"raisedAt"`
	IncidentAt  int64  `json:"incidentAt"`
	Late        bool   `json:"late"`
	DueBy       int64  `json:"dueBy"`
	Status      string `json:"status"`
	Resolution  string `json:"resolution,omitempty"`
}

func adminTicket(r *TicketRec) AdminTicket {
	t := r.Ticket
	return AdminTicket{ID: t.ID, AccountName: r.AccountName, Category: string(t.Category), Summary: t.Summary,
		RaisedAt: dto.MS(t.RaisedAt), IncidentAt: dto.MS(t.IncidentAt), Late: t.Late, DueBy: dto.MS(t.DueBy),
		Status: r.Status, Resolution: r.Resolution}
}

func (a *App) AdminTickets() []AdminTicket {
	a.ticketMu.Lock()
	defer a.ticketMu.Unlock()
	out := make([]AdminTicket, 0, len(a.tickets))
	for _, t := range a.tickets {
		out = append(out, adminTicket(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RaisedAt > out[j].RaisedAt })
	return out
}

func (a *App) TicketsOf(accountID string) []AdminTicket {
	a.ticketMu.Lock()
	defer a.ticketMu.Unlock()
	var out []AdminTicket
	for _, t := range a.tickets {
		if t.Ticket.Account == accountID {
			out = append(out, adminTicket(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RaisedAt > out[j].RaisedAt })
	return out
}

var ErrUnknownTicket = errors.New("unknown_ticket")

func (a *App) ResolveTicket(actor User, reason, id string) error {
	return a.Do(actor, "Resolved a dispute", id, reason, func() error {
		a.ticketMu.Lock()
		defer a.ticketMu.Unlock()
		rec := a.tickets[id]
		if rec == nil {
			return ErrUnknownTicket
		}
		next := *rec
		next.Status, next.Resolution = "resolved", reason
		if err := a.saveTicket(&next); err != nil {
			return err
		}
		*rec = next
		return nil
	})
}

// RecordAdjustment notes a correction against a trade. Trades are final: the trade itself is never changed.
func (a *App) RecordAdjustment(actor User, reason, fillID, note string) error {
	fillID, note = strings.TrimSpace(fillID), strings.TrimSpace(note)
	if fillID == "" || note == "" {
		return bad("invalid_adjustment", "Give the trade id and what the correction is.")
	}
	return a.Do(actor, "Recorded a trade correction: "+note, fillID, reason, func() error { return nil })
}

// ---- overview, systems, rulebook ----

type Block struct {
	ID               string  `json:"id"`
	Label            string  `json:"label"`
	StartMin         float64 `json:"startMin"`
	DurationMin      float64 `json:"durationMin"`
	Stage            string  `json:"stage"`
	MarketOpen       bool    `json:"marketOpen"`
	AllocationWindow *int    `json:"allocationWindow"`
}

type Overview struct {
	Clock struct {
		Status      string `json:"status"`
		ElapsedMs   int64  `json:"elapsedMs"`
		TotalMs     int64  `json:"totalMs"`
		BlockIndex  int    `json:"blockIndex"`
		IntoMs      int64  `json:"intoMs"`
		RemainingMs int64  `json:"remainingMs"`
	} `json:"clock"`
	Timeline []Block `json:"timeline"`
	Control  struct {
		TradingFrozen   bool      `json:"tradingFrozen"`
		PausedSymbols   []string  `json:"pausedSymbols"`
		MarketOverride  *string   `json:"marketOverride"`
		MarketOpen      bool      `json:"marketOpen"`
		WindowOverrides []*string `json:"windowOverrides"`
		WindowsOpen     []bool    `json:"windowsOpen"`
	} `json:"control"`
}

func ovString(o *bool) *string {
	if o == nil {
		return nil
	}
	s := "closed"
	if *o {
		s = "open"
	}
	return &s
}

func (a *App) Overview() Overview {
	var o Overview
	pos := a.Clock.Position()
	switch {
	case pos.Ended:
		o.Clock.Status = "ended"
	case !pos.Started:
		o.Clock.Status = "not_started"
	case pos.Paused:
		o.Clock.Status = "paused"
	default:
		o.Clock.Status = "running"
	}
	o.Clock.ElapsedMs, o.Clock.TotalMs = pos.Elapsed.Milliseconds(), a.Clock.Total().Milliseconds()
	o.Clock.BlockIndex, o.Clock.IntoMs, o.Clock.RemainingMs = pos.Index, pos.Into.Milliseconds(), pos.Remaining.Milliseconds()

	sched := a.Clock.Schedule()
	o.Timeline = make([]Block, len(sched))
	for i, b := range sched {
		o.Timeline[i] = Block{ID: b.Block.ID, Label: b.Block.Label, StartMin: round1(b.Start.Minutes()), DurationMin: round1(b.Duration.Minutes()),
			Stage: string(b.Block.Stage), MarketOpen: b.Block.MarketOpen, AllocationWindow: b.Block.AllocationWindow}
	}
	ov := a.Clock.Overrides()
	o.Control.TradingFrozen, o.Control.MarketOverride, o.Control.MarketOpen = ov.Frozen, ovString(ov.MarketOpen), a.Clock.MarketOpen()
	o.Control.PausedSymbols = a.PausedSymbols()
	n := a.RB.WindowCount()
	o.Control.WindowOverrides, o.Control.WindowsOpen = make([]*string, n), make([]bool, n)
	for i := 0; i < n; i++ {
		if i < len(ov.Windows) {
			o.Control.WindowOverrides[i] = ovString(ov.Windows[i])
		}
		o.Control.WindowsOpen[i] = a.Clock.WindowOpen(i)
	}
	return o
}

type ErrorRow struct {
	At      int64  `json:"at"`
	Message string `json:"message"`
}

type Systems struct {
	UptimeSec     int64      `json:"uptimeSec"`
	DBOK          bool       `json:"dbOk"`
	Connected     int        `json:"connected"`
	TradesPerMin  int64      `json:"tradesPerMin"`
	CommitP50Ms   int64      `json:"commitP50Ms"`
	CommitP99Ms   int64      `json:"commitP99Ms"`
	JournalErrors int64      `json:"journalErrors"`
	SymbolsTotal  int        `json:"symbolsTotal"`
	PriceTicks    int        `json:"priceTicks"`
	TickSeconds   int        `json:"tickSeconds"`
	LastPriceAt   int64      `json:"lastPriceAt"`
	DiskFreeMB    int64      `json:"diskFreeMb"`
	DiskLow       bool       `json:"diskLow"`
	RecentErrors  []ErrorRow `json:"recentErrors"`
}

func (a *App) Systems() Systems {
	now := a.now()
	p50, p99 := a.commits.Percentiles()
	st := a.Sim.Status()
	s := Systems{
		UptimeSec: int64(now.Sub(a.started).Seconds()), DBOK: true, Connected: a.Hub.Count(),
		TradesPerMin: a.tradesPM.Sum(now), CommitP50Ms: p50.Milliseconds(), CommitP99Ms: p99.Milliseconds(),
		JournalErrors: a.journalErrors.Load(), SymbolsTotal: len(a.symbols), PriceTicks: st.Ticks, TickSeconds: st.TickSeconds,
		LastPriceAt: a.lastTick.Load(), RecentErrors: []ErrorRow{},
	}
	if free, ok := a.cfg.Disk.Free(); ok {
		s.DiskFreeMB = int64(free >> 20)
		s.DiskLow = a.cfg.Disk.Check() != nil
	}
	for _, e := range a.errs.recent(20) {
		s.RecentErrors = append(s.RecentErrors, ErrorRow{At: dto.MS(e.At), Message: e.Message})
	}
	return s
}

// SimStatus is what the price simulation is doing and which events are scheduled.
func (a *App) SimStatus() sim.Status { return a.Sim.Status() }

// FireSimEvent releases a scheduled market event (or bull/bear run) right now.
func (a *App) FireSimEvent(actor User, reason, id string) error {
	return a.Do(actor, "Released a market event early", id, reason, func() error {
		if err := a.Sim.FireEvent(id, a.now()); err != nil {
			return bad("cannot_release", err.Error())
		}
		return nil
	})
}

type ProvenanceRow struct {
	Path    string `json:"path"`
	Status  string `json:"status"`
	Section string `json:"section"`
	Note    string `json:"note,omitempty"`
}

type RulebookView struct {
	Version    string          `json:"version"`
	Source     string          `json:"source"`
	Provenance []ProvenanceRow `json:"provenance"`
	Values     any             `json:"values"`
}

func (a *App) RulebookView(source string) RulebookView {
	v := RulebookView{Version: a.RB.Version, Source: source, Values: a.RB, Provenance: []ProvenanceRow{}}
	for path, p := range a.RB.Provenance {
		v.Provenance = append(v.Provenance, ProvenanceRow{Path: path, Status: string(p.Status), Section: p.Section, Note: p.Note})
	}
	sort.Slice(v.Provenance, func(i, j int) bool { return v.Provenance[i].Path < v.Provenance[j].Path })
	return v
}

// ---- share grants ----

// Grant is shares credited to a team without a trade: how inventory first enters the market.
type Grant struct {
	AccountID string      `json:"accountId"`
	Symbol    string      `json:"symbol"`
	Qty       int64       `json:"qty"`
	Price     money.Paise `json:"price"` // cost basis per share
}

// GrantShares credits qty shares of symbol to one team, or to every team when accountID is "*".
// Each grant is stored before it takes effect, and the whole action is audited. It returns how many
// teams were credited.
func (a *App) GrantShares(actor User, reason, accountID, symbol string, qty int64, priceRupees float64) (int, error) {
	if !a.HasSymbol(symbol) {
		return 0, trading.ErrUnknownSymbol
	}
	if qty < 1 || qty > maxQty {
		return 0, bad("invalid_quantity", "Quantity must be a whole number of at least 1.")
	}
	price := dto.Paise(priceRupees)
	if priceRupees <= 0 || price <= 0 {
		return 0, bad("invalid_price", "Give the cost basis per share (the price to value the shares at).")
	}
	var targets []string
	if accountID == "*" {
		for _, u := range a.users.all() {
			if !u.IsAdmin && u.Status != StatusDisqualified {
				targets = append(targets, u.ID)
			}
		}
		sort.Strings(targets)
	} else {
		u, ok := a.users.get(accountID)
		if !ok || u.IsAdmin {
			return 0, ErrUnknownUser
		}
		targets = []string{accountID}
	}
	n := 0
	target := fmt.Sprintf("%d x %s to %s", qty, symbol, accountID)
	if accountID == "*" {
		target = fmt.Sprintf("%d x %s to every team", qty, symbol)
	}
	err := a.Do(actor, "Granted shares", target, reason, func() error {
		for _, id := range targets {
			if err := a.giveShares(id, symbol, qty, price); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}

func round1(x float64) float64 { return math.Round(x*10) / 10 }

// ClockSetBlockDuration changes how long one block lasts, so the event can run shorter or longer than planned.
func (a *App) ClockSetBlockDuration(actor User, reason, blockID string, minutes int) error {
	if minutes < 1 || minutes > 24*60 {
		return bad("invalid_minutes", "A block lasts from 1 minute to 24 hours.")
	}
	return a.Do(actor, fmt.Sprintf("Set a block to %d min", minutes), blockID, reason, func() error {
		if err := a.Clock.SetBlockDuration(blockID, time.Duration(minutes)*time.Minute); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

// ClockEnd finishes the event right now.
func (a *App) ClockEnd(actor User, reason string) error {
	return a.Do(actor, "Ended the event", "clock", reason, func() error {
		if err := a.Clock.End(); err != nil {
			return err
		}
		a.persistClock()
		a.broadcastControl()
		return nil
	})
}

// PublicSchedule is the schedule participants may see, with the lengths the organiser has set now.
func (a *App) PublicSchedule() ([]rulebook.PublicBlock, int) {
	sched := a.Clock.Schedule()
	out := make([]rulebook.PublicBlock, len(sched))
	var total time.Duration
	for i, b := range sched {
		out[i] = rulebook.PublicBlock{ID: b.Block.ID, Label: b.Block.PublicLabel, StartMin: int(math.Round(b.Start.Minutes())),
			DurationMin: int(math.Round(b.Duration.Minutes())), Stage: b.Block.Stage, MarketOpen: b.Block.MarketOpen, AllocationWindow: b.Block.AllocationWindow}
		total += b.Duration
	}
	return out, int(math.Round(total.Minutes()))
}

// DiskLow reports whether the disk is too full to keep accepting trades.
func (a *App) DiskLow() bool { return a.cfg.Disk.Check() != nil }
