package app

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"math/rand"
	"sort"
	"strings"
	"time"

	"stockastic/api/internal/dto"
	"stockastic/api/internal/funds"
	"stockastic/api/internal/ids"
	"stockastic/api/internal/ledger"
	"stockastic/api/internal/money"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/scoring"
	"stockastic/api/internal/store"
	"stockastic/api/internal/trading"
)

var (
	ErrFundsNotFormed = errors.New("funds_not_formed")
	ErrNotAllowed     = errors.New("not_allowed")
	ErrNotTrader      = errors.New("not_the_trader")
)

// AcctOf is the ledger account a person trades with: their own team's, or, for a fund manager, the fund's.
func (a *App) AcctOf(u User) string {
	if u.Role == RoleFundManager {
		if f, ok := a.Funds.FundOfMember(u.ID); ok {
			return f.Account
		}
	}
	return u.ID
}

// ---- values ----

func (a *App) fundValue(f funds.Fund) money.Paise {
	v, err := a.Ledger.DirectValue(f.Account, a.Market.Price)
	if err != nil {
		return 0
	}
	return v
}

// navOf is a fund's NAV per unit: everything the fund holds, over the units outstanding. A fund with no
// units is at its launch NAV.
func (a *App) navOf(f funds.Fund) float64 {
	if f.Units <= 0 {
		return a.Funds.LaunchNAV()
	}
	return a.fundValue(f).Rupees() / f.Units
}

func (a *App) navs() map[string]float64 {
	out := map[string]float64{}
	for _, f := range a.Funds.Funds() {
		out[f.ID] = a.navOf(f)
	}
	return out
}

func unitsValue(h map[string]funds.Holding, navs map[string]float64) money.Paise {
	var t money.Paise
	for id, p := range h {
		if p.Units > 0 {
			t += money.FromRupees(p.Units * navs[id])
		}
	}
	return t
}

// TotalValue is cash, direct holdings and fund units at the current NAV.
func (a *App) totalValue(id string, navs map[string]float64) money.Paise {
	v, err := a.Ledger.DirectValue(id, a.Market.Price)
	if err != nil {
		return 0
	}
	return v + unitsValue(a.Funds.Holdings(id), navs)
}

// ---- restore and journal ----

func (a *App) fundAppend(ev funds.Event) error {
	if err := a.cfg.Disk.Check(); err != nil {
		return err
	}
	return a.wal.Append(store.KindFund, ev)
}

// applyFundEvent applies an event that is already stored. On restart (replay) it also moves the cash that
// the live operation had moved before storing the event.
func (a *App) applyFundEvent(ev funds.Event, replay bool) error {
	if replay {
		switch ev.Op {
		case funds.OpAlloc, funds.OpRedeem:
			f, ok := a.Funds.Fund(ev.FundID)
			if !ok {
				return funds.ErrUnknownFund
			}
			from, to := ev.Investor, f.Account
			if ev.Op == funds.OpRedeem {
				from, to = to, from
			}
			if err := a.Ledger.ReplayMove(from, to, money.Paise(ev.Amount)); err != nil {
				return err
			}
		}
	}
	var gone []funds.Fund
	if ev.Op == funds.OpDissolve {
		gone = a.Funds.Funds()
	}
	if err := a.Funds.Apply(ev); err != nil {
		return err
	}
	for _, f := range gone {
		a.Ledger.Remove(f.Account)
	}
	if ev.Op == funds.OpFormed {
		for _, f := range ev.Funds {
			err := a.Ledger.Open(ledger.Account{ID: f.Account, Name: "Fund " + f.ID, Kind: ledger.KindFund}, money.FromRupees(a.RB.Fund.SeedCapital))
			if err != nil && !errors.Is(err, ledger.ErrAccountExists) {
				return err
			}
		}
	}
	return nil
}

// ---- qualification and formation (Sections 5 and 6) ----

type QualRow struct {
	Rank      int     `json:"rank"`
	AccountID string  `json:"accountId"`
	Team      string  `json:"team"`
	Value     float64 `json:"value"`
	Peak      float64 `json:"peak"`
	Trades    int     `json:"trades"`
	DecidedBy string  `json:"decidedBy"`
	Qualifies bool    `json:"qualifies"`
	Fund      string  `json:"fund,omitempty"`
}

type Qualification struct {
	Ready       bool      `json:"ready"` // the Phase 1 freeze has happened
	Done        bool      `json:"done"`  // the funds have been formed
	Cutoff      int       `json:"cutoff"`
	Rows        []QualRow `json:"rows"`
	BoundaryTie bool      `json:"boundaryTie"` // the last qualifying place was decided by a tie-break
	CoinToss    bool      `json:"coinToss"`    // and that tie-break was the coin toss, which the organiser must witness
}

func coinKey(seed int64) func(string) int64 {
	return func(account string) int64 {
		h := fnv.New64a()
		fmt.Fprintf(h, "%d/%s", seed, account)
		return int64(h.Sum64() >> 1)
	}
}

func (a *App) phase1Standings() ([]scoring.Standing, FreezeSnapshot, bool) {
	snap, ok := a.Snapshot("phase1")
	if !ok {
		return nil, snap, false
	}
	var st []scoring.Standing
	for _, u := range a.users.all() {
		if u.IsAdmin || u.Status == StatusDisqualified {
			continue
		}
		v, ok := snap.Values[u.ID]
		if !ok {
			continue
		}
		st = append(st, scoring.Standing{AccountID: u.ID, FinalValue: money.Paise(v), PeakValue: money.Paise(snap.Peaks[u.ID]), Transactions: snap.Trades[u.ID]})
	}
	return st, snap, true
}

// Qualification shows the Phase 1 ranking and who qualifies, before or after the funds are formed.
func (a *App) Qualification() Qualification {
	q := Qualification{Cutoff: a.RB.Qualification.QualifyingTeams, Rows: []QualRow{}, Done: a.Funds.Formed()}
	st, snap, ok := a.phase1Standings()
	if !ok {
		return q
	}
	q.Ready = true
	seed := a.Funds.Seed()
	ranked := scoring.RankPhase1(st, a.RB.Qualification.TieBreak, coinKey(seed))
	fundOf := map[string]string{}
	for _, f := range a.Funds.Funds() {
		fundOf[f.Members[0]], fundOf[f.Members[1]] = f.ID, f.ID
	}
	for _, r := range ranked {
		u, _ := a.users.get(r.AccountID)
		row := QualRow{Rank: r.Rank, AccountID: r.AccountID, Team: u.DisplayName, Value: dto.Rupees(r.FinalValue), Peak: dto.Rupees(r.PeakValue),
			Trades: r.Transactions, DecidedBy: string(r.DecidedBy), Qualifies: r.Rank <= q.Cutoff, Fund: fundOf[r.AccountID]}
		q.Rows = append(q.Rows, row)
		if r.Rank == q.Cutoff && r.DecidedBy != scoring.DecidedByValue {
			q.BoundaryTie = true
			q.CoinToss = r.DecidedBy == scoring.DecidedByCoinToss || r.DecidedBy == scoring.DecidedByUnresolved
		}
	}
	_ = snap
	return q
}

// FormFunds creates the funds. With no pairs given it ranks the Phase 1 result, takes the top teams and pairs
// them mirror-style (rank k with rank N+1-k). With pairs given, the organiser has chosen who is paired with whom:
// each pair is two teams, the first of which places the fund's trades. It can be done once (until dissolved).
func (a *App) FormFunds(actor User, pairs [][]string) error {
	action := "Formed the funds from the Phase 1 result"
	if len(pairs) > 0 {
		action = "Formed the funds from chosen pairs"
	}
	return a.Do(actor, action, "funds", "", func() error {
		a.evMu.RLock()
		defer a.evMu.RUnlock()
		a.fundMu.Lock()
		defer a.fundMu.Unlock()
		if a.Funds.Formed() {
			return bad("already_formed", "The funds have already been formed.")
		}
		ev := funds.Event{Op: funds.OpFormed, At: a.now().UnixMilli()}
		ranks := map[string]int{}
		st, _, haveSnap := a.phase1Standings()
		if haveSnap {
			ev.Seed = rand.Int63()
			for _, r := range scoring.RankPhase1(st, a.RB.Qualification.TieBreak, coinKey(ev.Seed)) {
				ranks[r.AccountID] = r.Rank
			}
		}
		if len(pairs) == 0 {
			if !haveSnap {
				return bad("phase1_not_frozen", "Phase 1 has not been frozen yet, so there is no result to rank. Choose the pairs yourself, or take the Phase 1 snapshot first.")
			}
			need := a.RB.Qualification.QualifyingTeams
			if len(st) < need {
				return bad("not_enough_teams", fmt.Sprintf("%d teams are needed to form the funds and only %d took part.", need, len(st)))
			}
			ranked := scoring.RankPhase1(st, a.RB.Qualification.TieBreak, coinKey(ev.Seed))
			top := ranked[:need]
			mp, err := scoring.MirrorPairing(top)
			if err != nil {
				return bad("pairing", err.Error())
			}
			for _, r := range top {
				ev.Ranking = append(ev.Ranking, funds.RankRow{Account: r.AccountID, Rank: r.Rank, Value: int64(r.FinalValue), DecidedBy: string(r.DecidedBy)})
			}
			for _, p := range mp {
				pairs = append(pairs, []string{p.Stronger.AccountID, p.Weaker.AccountID})
			}
		}
		if len(pairs) > a.RB.Qualification.FundCount {
			return bad("too_many_funds", fmt.Sprintf("The rulebook has %d funds.", a.RB.Qualification.FundCount))
		}
		used := map[string]bool{}
		for i, p := range pairs {
			if len(p) != 2 || p[0] == p[1] {
				return bad("invalid_pair", fmt.Sprintf("Fund %d needs two different teams.", i+1))
			}
			for _, id := range p {
				u, ok := a.users.get(id)
				if !ok || u.IsAdmin || u.Status == StatusDisqualified {
					return bad("invalid_pair", fmt.Sprintf("Fund %d has a team that does not exist or cannot play.", i+1))
				}
				if used[id] {
					return bad("invalid_pair", fmt.Sprintf("%s appears in more than one fund.", u.DisplayName))
				}
				used[id] = true
			}
			id := fmt.Sprintf("F%d", i+1)
			ev.Funds = append(ev.Funds, funds.Formed{ID: id, Number: i + 1, Account: "fund:" + id, Members: [2]string{p[0], p[1]}, Trader: p[0], Ranks: [2]int{ranks[p[0]], ranks[p[1]]}})
		}
		if err := a.fundAppend(ev); err != nil {
			return err
		}
		if err := a.applyFundEvent(ev, false); err != nil {
			return err
		}
		for _, f := range ev.Funds {
			for _, m := range f.Members {
				if _, err := a.updateUser(m, func(x *User) error { x.Role = RoleFundManager; return nil }); err != nil {
					a.log.Error("could not promote a fund manager", "account", m, "err", err)
				}
			}
		}
		a.Hub.ToAll("fundsFormed", map[string]any{"at": dto.MS(a.now())})
		return nil
	})
}

// SetFundTrader chooses which of a fund's two teams places its trades.
func (a *App) SetFundTrader(actor User, fundID, account string) error {
	f, ok := a.Funds.Fund(fundID)
	if !ok {
		return funds.ErrUnknownFund
	}
	if account != f.Members[0] && account != f.Members[1] {
		return bad("not_a_member", "That team is not one of this fund's two teams.")
	}
	name := account
	if u, ok := a.users.get(account); ok {
		name = u.DisplayName
	}
	return a.Do(actor, "Chose who trades for the fund", f.ID+": "+name, "", func() error {
		a.evMu.RLock()
		defer a.evMu.RUnlock()
		a.fundMu.Lock()
		defer a.fundMu.Unlock()
		ev := funds.Event{Op: funds.OpTrader, At: a.now().UnixMilli(), FundID: fundID, Investor: account}
		if err := a.fundAppend(ev); err != nil {
			return err
		}
		if err := a.Funds.Apply(ev); err != nil {
			return err
		}
		for _, m := range f.Members {
			a.Hub.ToAccount(m, "portfolio", map[string]any{"reason": "trader"})
		}
		return nil
	})
}

// DissolveFunds takes the funds apart so they can be formed again. It is refused once anyone has invested,
// because their money would have nowhere to go.
func (a *App) DissolveFunds(actor User) error {
	return a.Do(actor, "Dissolved the funds", "funds", "", func() error {
		a.evMu.RLock()
		defer a.evMu.RUnlock()
		a.fundMu.Lock()
		defer a.fundMu.Unlock()
		if !a.Funds.Formed() {
			return bad("not_formed", "There are no funds to dissolve.")
		}
		if len(a.Funds.Investors()) > 0 {
			return bad("has_investors", "Investors have already put money into the funds. Reset the event instead.")
		}
		members := []string{}
		for _, f := range a.Funds.Funds() {
			members = append(members, f.Members[0], f.Members[1])
		}
		ev := funds.Event{Op: funds.OpDissolve, At: a.now().UnixMilli()}
		if err := a.fundAppend(ev); err != nil {
			return err
		}
		if err := a.applyFundEvent(ev, false); err != nil {
			return err
		}
		for _, m := range members {
			if _, err := a.updateUser(m, func(x *User) error { x.Role = RoleInvestor; return nil }); err != nil {
				a.log.Error("could not move a team back to investor", "account", m, "err", err)
			}
		}
		a.Hub.ToAll("fundsFormed", map[string]any{"at": dto.MS(a.now())})
		return nil
	})
}

// ---- fund profile (Section 8) ----

type ProfileRequest struct {
	Name       string `json:"name"`
	Philosophy string `json:"philosophy"`
	Risk       string `json:"risk"`
	Strategy   string `json:"strategy"`
}

var riskProfiles = map[string]bool{"Conservative": true, "Balanced": true, "Aggressive": true}

func (a *App) SetFundProfile(u User, r ProfileRequest) error {
	f, ok := a.Funds.FundOfMember(u.ID)
	if !ok || u.Role != RoleFundManager {
		return ErrNotAllowed
	}
	p := funds.Profile{Name: strings.TrimSpace(r.Name), Philosophy: strings.TrimSpace(r.Philosophy), Risk: strings.TrimSpace(r.Risk), Strategy: strings.TrimSpace(r.Strategy)}
	switch {
	case p.Name == "" || len([]rune(p.Name)) > 40:
		return bad("invalid_name", "Give the fund a name of up to 40 characters.")
	case len([]rune(p.Philosophy)) > 400 || len([]rune(p.Strategy)) > 60:
		return bad("invalid_profile", "Keep the philosophy under 400 characters and the strategy under 60.")
	case !riskProfiles[p.Risk]:
		return bad("invalid_risk", "Choose Conservative, Balanced or Aggressive.")
	}
	a.fundMu.Lock()
	defer a.fundMu.Unlock()
	ev := funds.Event{Op: funds.OpProfile, At: a.now().UnixMilli(), FundID: f.ID, Profile: &p}
	if err := a.fundAppend(ev); err != nil {
		return err
	}
	return a.applyFundEvent(ev, false)
}

// ---- allocation and redemption (Section 10) ----

// OpenWindow is the allocation window that is open right now.
func (a *App) OpenWindow() (int, bool) {
	for w := range a.Clock.Overrides().Windows {
		if a.Clock.WindowOpen(w) {
			return w, true
		}
	}
	return 0, false
}

type AllocationRequest struct {
	Amount float64 `json:"amount"`
	All    bool    `json:"all"` // redeem only: take everything out
}

type AllocationResult struct {
	FundID string  `json:"fundId"`
	Units  float64 `json:"units"`
	NAV    float64 `json:"nav"`
	Amount float64 `json:"amount"`
}

func (a *App) investorCheck(u User) error {
	if u.Role != RoleInvestor || u.IsAdmin {
		return bad("investors_only", "Only individual investors can put money into funds.")
	}
	if u.Status == StatusDisqualified {
		return ErrDisqualified
	}
	if !a.Funds.Formed() {
		return ErrFundsNotFormed
	}
	return nil
}

func floorUnits(x float64) float64 { return math.Floor(x*1e6) / 1e6 }

// capRoom is how much more a fund may take right now under the equal-cap rule (Section 9): the mandatory pool
// is split equally across the funds; a fund that has filled its share waits until every fund has, then all rise.
func (a *App) capRooms(window int, navs map[string]float64) map[string]money.Paise {
	var values []money.Paise
	for _, u := range a.users.all() {
		if !u.IsAdmin && u.Role == RoleInvestor && u.Status != StatusDisqualified {
			values = append(values, a.totalValue(u.ID, navs))
		}
	}
	pool := scoring.MandatoryPool(values, a.RB.Fund.MandatoryAllocationPercent)
	var cs []scoring.CapFund
	all := a.Funds.Funds()
	for _, f := range all {
		in := a.Funds.Inflow(window, f.ID)
		cs = append(cs, scoring.CapFund{FundID: f.ID, Headcount: 1, InflowThisWindow: in, Active: !f.Disqualified})
	}
	_, states := scoring.AllocationCaps(cs, pool, len(all), 1)
	out := map[string]money.Paise{}
	for _, s := range states {
		out[s.FundID] = s.Room
	}
	return out
}

// Allocate puts cash into a fund during an open window. Units = amount / NAV at that moment.
func (a *App) Allocate(u User, fundID string, req AllocationRequest) (AllocationResult, error) {
	if err := a.investorCheck(u); err != nil {
		return AllocationResult{}, err
	}
	if math.IsNaN(req.Amount) || math.IsInf(req.Amount, 0) || req.Amount <= 0 || req.Amount > maxAdjustRupees {
		return AllocationResult{}, bad("invalid_amount", "Enter an amount to invest.")
	}
	window, open := a.OpenWindow()
	if !open {
		return AllocationResult{}, bad("window_closed", "Funds can only be entered or left while an allocation window is open.")
	}
	a.evMu.RLock()
	defer a.evMu.RUnlock()
	a.fundMu.Lock()
	defer a.fundMu.Unlock()
	f, ok := a.Funds.Fund(fundID)
	if !ok {
		return AllocationResult{}, funds.ErrUnknownFund
	}
	if f.Disqualified {
		return AllocationResult{}, bad("fund_unavailable", "This fund is not accepting investment.")
	}
	navs := a.navs()
	nav := navs[f.ID]
	amount := money.FromRupees(req.Amount)
	wallet := a.totalValue(u.ID, navs)
	snap, err := a.Ledger.Snapshot(u.ID)
	if err != nil {
		return AllocationResult{}, ErrNoAccount
	}
	if amount > snap.Cash {
		return AllocationResult{}, bad("insufficient_cash", "You do not have that much cash. Sell some shares first.")
	}
	if min := scoring.MinInvestment(wallet, a.RB.Fund); amount < min {
		return AllocationResult{}, bad("below_minimum", fmt.Sprintf("The smallest investment in a fund is ₹%.0f.", dto.Rupees(min)))
	}
	held := money.FromRupees(a.Funds.Holding(u.ID, f.ID).Units * nav)
	limit := money.Paise(math.Round(float64(wallet) * a.RB.Fund.MaxSingleFundWalletPercent / 100))
	if held+amount > limit {
		room := limit - held
		if room < 0 {
			room = 0
		}
		return AllocationResult{}, bad("exceeds_single_fund_cap", fmt.Sprintf("At most %.0f%% of your wallet can be in one fund: you can add up to ₹%.2f more.", a.RB.Fund.MaxSingleFundWalletPercent, dto.Rupees(room)))
	}
	if room := a.capRooms(window, navs)[f.ID]; amount > room {
		return AllocationResult{}, bad("fund_at_cap", fmt.Sprintf("This fund has reached its share of this window. It can take up to ₹%.2f now, and more once every fund has reached the same level.", dto.Rupees(room)))
	}
	units := floorUnits(amount.Rupees() / nav)
	if units <= 0 {
		return AllocationResult{}, bad("invalid_amount", "That amount is too small to buy any units.")
	}
	ev := funds.Event{Op: funds.OpAlloc, At: a.now().UnixMilli(), FundID: f.ID, Investor: u.ID, Window: window, Amount: int64(amount), Units: units, NAV: nav}
	if err := a.Ledger.Move(u.ID, f.Account, amount, func() error { return a.fundAppend(ev) }); err != nil {
		if errors.Is(err, ledger.ErrInsufficientCash) {
			return AllocationResult{}, bad("insufficient_cash", "You do not have that much cash. Sell some shares first.")
		}
		return AllocationResult{}, err
	}
	if err := a.Funds.Apply(ev); err != nil {
		return AllocationResult{}, err
	}
	a.Hub.ToAccount(u.ID, "portfolio", map[string]any{"reason": "fund"})
	return AllocationResult{FundID: f.ID, Units: units, NAV: nav, Amount: dto.Rupees(amount)}, nil
}

// Redeem takes money out of a fund during an open window. If the fund is short of cash it sells a slice of
// every holding at the current prices to pay, so no redemption is ever stuck.
func (a *App) Redeem(ctx context.Context, u User, fundID string, req AllocationRequest) (AllocationResult, error) {
	if err := a.investorCheck(u); err != nil {
		return AllocationResult{}, err
	}
	window, open := a.OpenWindow()
	if !open {
		return AllocationResult{}, bad("window_closed", "Funds can only be entered or left while an allocation window is open.")
	}
	a.evMu.RLock()
	defer a.evMu.RUnlock()
	a.fundMu.Lock()
	defer a.fundMu.Unlock()
	f, ok := a.Funds.Fund(fundID)
	if !ok {
		return AllocationResult{}, funds.ErrUnknownFund
	}
	navs := a.navs()
	nav := navs[f.ID]
	pos := a.Funds.Holding(u.ID, f.ID)
	if pos.Units <= 0 {
		return AllocationResult{}, bad("nothing_to_redeem", "You have no units in this fund.")
	}
	units := pos.Units
	if !req.All {
		if math.IsNaN(req.Amount) || math.IsInf(req.Amount, 0) || req.Amount <= 0 {
			return AllocationResult{}, bad("invalid_amount", "Enter an amount to take out.")
		}
		units = math.Min(pos.Units, floorUnits(req.Amount/nav))
	}
	if units <= 0 {
		return AllocationResult{}, bad("invalid_amount", "That amount is too small.")
	}
	amount := money.FromRupees(units * nav)
	if amount <= 0 {
		return AllocationResult{}, bad("invalid_amount", "That amount is too small.")
	}
	// The 5% minimum in funds stays satisfied after leaving (Section 9).
	wallet := a.totalValue(u.ID, navs)
	all := a.Funds.Holdings(u.ID)
	inFunds := unitsValue(all, navs) - amount
	if wallet > 0 && float64(inFunds)*100 < float64(wallet)*a.RB.Fund.MandatoryAllocationPercent-1e-6 {
		return AllocationResult{}, bad("below_mandatory", fmt.Sprintf("Every team must keep at least %.0f%% of its portfolio in funds, so you cannot take out that much.", a.RB.Fund.MandatoryAllocationPercent))
	}
	if err := a.raiseFundCash(ctx, f, amount); err != nil {
		return AllocationResult{}, err
	}
	ev := funds.Event{Op: funds.OpRedeem, At: a.now().UnixMilli(), FundID: f.ID, Investor: u.ID, Window: window, Amount: int64(amount), Units: units, NAV: nav}
	if err := a.Ledger.Move(f.Account, u.ID, amount, func() error { return a.fundAppend(ev) }); err != nil {
		return AllocationResult{}, err
	}
	if err := a.Funds.Apply(ev); err != nil {
		return AllocationResult{}, err
	}
	a.Hub.ToAccount(u.ID, "portfolio", map[string]any{"reason": "fund"})
	return AllocationResult{FundID: f.ID, Units: units, NAV: nav, Amount: dto.Rupees(amount)}, nil
}

// raiseFundCash makes sure the fund has at least this much cash, selling the same slice of every holding.
func (a *App) raiseFundCash(ctx context.Context, f funds.Fund, need money.Paise) error {
	snap, err := a.Ledger.Snapshot(f.Account)
	if err != nil {
		return err
	}
	if snap.Cash >= need {
		return nil
	}
	var held money.Paise
	for _, p := range snap.Positions {
		if px, ok := a.Market.Price(p.Symbol); ok {
			held += px * money.Paise(p.Qty)
		}
	}
	if held <= 0 {
		return bad("fund_illiquid", "The fund cannot pay that out right now. Try a smaller amount.")
	}
	frac := math.Min(1, float64(need-snap.Cash)/float64(held)*1.001+1e-9)
	for _, p := range snap.Positions {
		qty := int64(math.Ceil(float64(p.Qty) * frac))
		if qty > p.Qty {
			qty = p.Qty
		}
		if qty <= 0 {
			continue
		}
		res, err := a.Exec.Execute(ctx, trading.Request{ClientTradeID: "liq-" + ids.New(), AccountID: f.Account, Symbol: p.Symbol, Side: trading.Sell, Qty: qty, Stage: string(a.stage())})
		if err != nil {
			return err
		}
		a.recordTrade(res.Trade)
		snap2, _ := a.Ledger.Snapshot(f.Account)
		if snap2.Cash >= need {
			break
		}
	}
	return nil
}

// ---- checkpoints, fees and the once-a-minute series ----

func (a *App) fundAUMs() map[string]int64 {
	out := map[string]int64{}
	for _, f := range a.Funds.Funds() {
		out[f.ID] = int64(a.fundValue(f))
	}
	return out
}

// takeCheckpoint records every fund's NAV, AUM and fees at a window close or the final close. It runs once
// per name.
func (a *App) takeCheckpoint(name string) {
	if !a.Funds.Formed() {
		return
	}
	a.fundMu.Lock()
	defer a.fundMu.Unlock()
	for _, c := range a.Funds.Checkpoints() {
		if c.Name == name {
			return
		}
	}
	cp := a.Funds.PlanCheckpoint(name, a.now().UnixMilli(), a.navs(), a.fundAUMs(), a.RB.Fees.ManagementFeePercent, a.RB.Fees.PerformanceFeePercent)
	ev := funds.Event{Op: funds.OpCheckpoint, At: cp.At, Checkpoint: &cp}
	if err := a.fundAppend(ev); err != nil {
		a.log.Error("could not save a fund checkpoint", "name", name, "err", err)
		return
	}
	_ = a.Funds.Apply(ev)
	a.log.Info("fund checkpoint taken", "name", name)
}

// sampleSeries records every investor's value and every fund's NAV and AUM. The risk scores (largest fall
// from a peak) and average AUM are built from these once-a-minute samples.
func (a *App) sampleSeries() {
	if !a.Funds.Formed() || a.stage() != rulebook.StagePhase2 {
		return
	}
	if p := a.Clock.Position(); !p.Started || p.Paused || p.Ended {
		return
	}
	a.fundMu.Lock()
	defer a.fundMu.Unlock()
	navs := a.navs()
	ev := funds.Event{Op: funds.OpSeries, At: a.now().UnixMilli(), NAVs: navs, AUMs: a.fundAUMs(), Values: map[string]int64{}}
	for _, u := range a.users.all() {
		if !u.IsAdmin && u.Role == RoleInvestor && u.Status != StatusDisqualified {
			ev.Values[u.ID] = int64(a.totalValue(u.ID, navs))
		}
	}
	if err := a.fundAppend(ev); err != nil {
		a.log.Error("could not save the fund series", "err", err)
		return
	}
	_ = a.Funds.Apply(ev)
}

// fundLoop takes a sample every minute while Phase 2 runs.
func (a *App) fundLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.sampleSeries()
		}
	}
}

// ---- what people see ----

type FundInfo struct {
	ID           string   `json:"id"`
	Number       int      `json:"number"`
	Name         string   `json:"name"`
	Philosophy   string   `json:"philosophy"`
	Risk         string   `json:"risk"`
	Strategy     string   `json:"strategy"`
	Managers     []string `json:"managers"`
	NAV          float64  `json:"nav"`
	ReturnPct    float64  `json:"returnPct"`
	AUM          float64  `json:"aum"`
	Investors    int      `json:"investors"`
	Disqualified bool     `json:"disqualified"`
	// for the person asking
	Room          float64 `json:"room"` // how much more the fund can take now under the cap rule
	MyUnits       float64 `json:"myUnits"`
	MyValue       float64 `json:"myValue"`
	MyContributed float64 `json:"myContributed"`
}

type FundsView struct {
	Formed         bool       `json:"formed"`
	WindowOpen     bool       `json:"windowOpen"`
	Window         int        `json:"window"`
	MandatoryPct   float64    `json:"mandatoryPercent"`
	MinAbsolute    float64    `json:"minAbsolute"`
	MinWalletPct   float64    `json:"minWalletPercent"`
	MaxWalletPct   float64    `json:"maxWalletPercent"`
	MyValueInFunds float64    `json:"myValueInFunds"`
	MyWallet       float64    `json:"myWallet"`
	Compliant      bool       `json:"compliant"`
	Funds          []FundInfo `json:"funds"`
}

func (a *App) memberNames(f funds.Fund) []string {
	var out []string
	for _, m := range f.Members {
		if u, ok := a.users.get(m); ok {
			out = append(out, u.DisplayName)
		}
	}
	return out
}

func (a *App) fundInfo(f funds.Fund, navs map[string]float64, viewer string) FundInfo {
	nav := navs[f.ID]
	d := FundInfo{ID: f.ID, Number: f.Number, Name: f.Profile.Name, Philosophy: f.Philosophy, Risk: f.Risk, Strategy: f.Strategy,
		Managers: a.memberNames(f), NAV: nav, ReturnPct: (nav/a.Funds.LaunchNAV() - 1) * 100, AUM: dto.Rupees(a.fundValue(f)),
		Disqualified: f.Disqualified}
	for _, inv := range a.Funds.Investors() {
		if h := a.Funds.Holding(inv, f.ID); h.Units > 0 {
			d.Investors++
		}
	}
	if viewer != "" {
		h := a.Funds.Holding(viewer, f.ID)
		d.MyUnits, d.MyValue, d.MyContributed = h.Units, dto.Rupees(money.FromRupees(h.Units*nav)), dto.Rupees(h.Contributed)
	}
	return d
}

// FundsFor lists the funds for a signed-in person, with their own position and the room each has.
func (a *App) FundsFor(u User) FundsView {
	v := FundsView{Formed: a.Funds.Formed(), MandatoryPct: a.RB.Fund.MandatoryAllocationPercent, MinAbsolute: a.RB.Fund.MinInvestmentAbsolute,
		MinWalletPct: a.RB.Fund.MinInvestmentWalletPercent, MaxWalletPct: a.RB.Fund.MaxSingleFundWalletPercent, Funds: []FundInfo{}, Compliant: true}
	v.Window, v.WindowOpen = a.OpenWindow()
	if !v.Formed {
		return v
	}
	navs := a.navs()
	viewer := ""
	if u.Role == RoleInvestor {
		viewer = u.ID
		wallet := a.totalValue(u.ID, navs)
		inFunds := unitsValue(a.Funds.Holdings(u.ID), navs)
		v.MyWallet, v.MyValueInFunds = dto.Rupees(wallet), dto.Rupees(inFunds)
		v.Compliant = wallet <= 0 || float64(inFunds)*100+1e-6 >= float64(wallet)*a.RB.Fund.MandatoryAllocationPercent
	}
	rooms := map[string]money.Paise{}
	if v.WindowOpen {
		rooms = a.capRooms(v.Window, navs)
	}
	for _, f := range a.Funds.Funds() {
		d := a.fundInfo(f, navs, viewer)
		d.Room = dto.Rupees(rooms[f.ID])
		v.Funds = append(v.Funds, d)
	}
	return v
}

// MyFund is a fund manager's own fund: its figures, its cash and holdings, and its fees so far.
type MyFund struct {
	Fund        FundInfo         `json:"fund"`
	Cash        float64          `json:"cash"`
	Holdings    []dto.Holding    `json:"holdings"`
	Checkpoints []FundCheckpoint `json:"checkpoints"`
	MaxDrawdown float64          `json:"maxDrawdown"`
	Retention   float64          `json:"retention"`
	// CanTrade is false for the fund's other team: it can see the fund but only the trader places trades.
	CanTrade   bool   `json:"canTrade"`
	TraderName string `json:"traderName"`
}

type FundCheckpoint struct {
	Name    string  `json:"name"`
	NAV     float64 `json:"nav"`
	AUM     float64 `json:"aum"`
	AvgAUM  float64 `json:"avgAum"`
	MgmtFee float64 `json:"mgmtFee"`
	PerfFee float64 `json:"perfFee"`
}

func (a *App) MyFund(u User) (MyFund, error) {
	f, ok := a.Funds.FundOfMember(u.ID)
	if !ok || u.Role != RoleFundManager {
		return MyFund{}, ErrNotAllowed
	}
	navs := a.navs()
	m := MyFund{Fund: a.fundInfo(f, navs, ""), Holdings: []dto.Holding{}, Checkpoints: []FundCheckpoint{},
		MaxDrawdown: a.Funds.MaxDrawdown(f.ID), Retention: a.Funds.Retention(f.ID), CanTrade: f.Trader == u.ID}
	if t, ok := a.users.get(f.Trader); ok {
		m.TraderName = t.DisplayName
	}
	pf, err := a.portfolioOf(f.Account)
	if err != nil {
		return m, err
	}
	m.Cash, m.Holdings = pf.CashBalance, pf.Holdings
	for _, c := range a.Funds.Checkpoints() {
		for _, fc := range c.Funds {
			if fc.FundID == f.ID {
				m.Checkpoints = append(m.Checkpoints, FundCheckpoint{Name: c.Name, NAV: fc.NAV, AUM: dto.Rupees(money.Paise(fc.AUM)), AvgAUM: dto.Rupees(money.Paise(fc.AvgAUM)),
					MgmtFee: dto.Rupees(money.Paise(fc.MgmtFee)), PerfFee: dto.Rupees(money.Paise(fc.PerfFee))})
			}
		}
	}
	return m, nil
}

// ---- strategy log (Prize 3) ----

const maxLogChars = 600

// logCheckpoint is which strategy-log checkpoint it is now: 1 until the first window closes, then 2 and 3.
func (a *App) logCheckpoint() int {
	n := 1
	for _, c := range a.Funds.Checkpoints() {
		if strings.HasPrefix(c.Name, "window ") && c.Name != "window 0" {
			n++
		}
	}
	if n > a.RB.Prizes.Prize3.StrategyLogCheckpoints {
		n = a.RB.Prizes.Prize3.StrategyLogCheckpoints
	}
	return n
}

func (a *App) SubmitLog(u User, text string) error {
	if u.Role != RoleInvestor || u.IsAdmin {
		return bad("investors_only", "Strategy logs are for individual investors.")
	}
	if a.stage() != rulebook.StagePhase2 {
		return bad("not_phase2", "Strategy logs are written during Phase 2.")
	}
	text = strings.TrimSpace(text)
	if text == "" || len([]rune(text)) > maxLogChars {
		return bad("invalid_log", fmt.Sprintf("Write 2 to 3 sentences, up to %d characters.", maxLogChars))
	}
	a.fundMu.Lock()
	defer a.fundMu.Unlock()
	l := funds.StrategyLog{Account: u.ID, Checkpoint: a.logCheckpoint(), Text: text, At: a.now().UnixMilli()}
	ev := funds.Event{Op: funds.OpLog, At: l.At, Log: &l}
	if err := a.fundAppend(ev); err != nil {
		return err
	}
	return a.Funds.Apply(ev)
}

func (a *App) MyLogs(u User) []funds.StrategyLog {
	out := a.Funds.Logs(u.ID)
	if out == nil {
		out = []funds.StrategyLog{}
	}
	return out
}

// ---- organiser: funds, prizes, logs ----

type AdminFund struct {
	FundInfo
	Cash          float64          `json:"cash"`
	MaxDrawdown   float64          `json:"maxDrawdown"`
	Retention     float64          `json:"retention"`
	Profitability float64          `json:"profitability"`
	Ranks         [2]int           `json:"ranks"`
	Members       []string         `json:"memberIds"`
	Trader        string           `json:"trader"` // account id of the team that places the fund's trades
	Checkpoints   []FundCheckpoint `json:"checkpoints"`
}

func (a *App) AdminFunds() []AdminFund {
	navs := a.navs()
	cps := a.Funds.Checkpoints()
	out := []AdminFund{}
	for _, f := range a.Funds.Funds() {
		af := AdminFund{FundInfo: a.fundInfo(f, navs, ""), MaxDrawdown: a.Funds.MaxDrawdown(f.ID), Retention: a.Funds.Retention(f.ID),
			Profitability: a.Funds.Profitability(f.ID, navs[f.ID]), Ranks: f.Ranks, Members: f.Members[:], Trader: f.Trader, Checkpoints: []FundCheckpoint{}}
		if s, err := a.Ledger.Snapshot(f.Account); err == nil {
			af.Cash = dto.Rupees(s.Cash)
		}
		for _, c := range cps {
			for _, fc := range c.Funds {
				if fc.FundID == f.ID {
					af.Checkpoints = append(af.Checkpoints, FundCheckpoint{Name: c.Name, NAV: fc.NAV, AUM: dto.Rupees(money.Paise(fc.AUM)), AvgAUM: dto.Rupees(money.Paise(fc.AvgAUM)),
						MgmtFee: dto.Rupees(money.Paise(fc.MgmtFee)), PerfFee: dto.Rupees(money.Paise(fc.PerfFee))})
				}
			}
		}
		out = append(out, af)
	}
	return out
}

func (a *App) DisqualifyFund(actor User, id string) error {
	return a.Do(actor, "Removed a fund from Prize 1", id, "", func() error {
		a.fundMu.Lock()
		defer a.fundMu.Unlock()
		if _, ok := a.Funds.Fund(id); !ok {
			return funds.ErrUnknownFund
		}
		ev := funds.Event{Op: funds.OpFundDQ, At: a.now().UnixMilli(), FundID: id}
		if err := a.fundAppend(ev); err != nil {
			return err
		}
		return a.Funds.Apply(ev)
	})
}

type PrizeRow struct {
	ID    string  `json:"id"`
	Name  string  `json:"name"`
	Score float64 `json:"score"`
	Rank  int     `json:"rank"`
	Note  string  `json:"note,omitempty"`
}

type Prizes struct {
	Final  bool       `json:"final"` // computed from the final freeze rather than live prices
	Prize1 []PrizeRow `json:"prize1"`
	Prize2 []PrizeRow `json:"prize2"`
	Prize3 []PrizeRow `json:"prize3"`
	Prize4 []PrizeRow `json:"prize4"`
}

// Prizes applies the Section 16 formulas. After the final freeze it uses the frozen figures; before it, the
// live ones, so the organiser can watch the standings develop.
func (a *App) Prizes() Prizes {
	res := Prizes{Prize1: []PrizeRow{}, Prize2: []PrizeRow{}, Prize3: []PrizeRow{}, Prize4: []PrizeRow{}}
	if !a.Funds.Formed() {
		return res
	}
	navs := a.navs()
	values := map[string]money.Paise{}
	if snap, ok := a.Snapshot("final"); ok {
		res.Final = true
		for k, v := range snap.Values {
			values[k] = money.Paise(v)
		}
		if snap.FundNAV != nil {
			navs = snap.FundNAV
		}
	}
	name := func(id string) string {
		if u, ok := a.users.get(id); ok {
			return u.DisplayName
		}
		return id
	}

	// Prize 1: the fund with the best weighted ranking score, never the biggest fund.
	var p1 []scoring.Prize1Input
	fnames := map[string]string{}
	for _, f := range a.Funds.Funds() {
		fnames[f.ID] = f.Profile.Name
		if f.Disqualified {
			continue
		}
		nav := navs[f.ID]
		p1 = append(p1, scoring.Prize1Input{FundID: f.ID, NavReturnPct: (nav/a.Funds.LaunchNAV() - 1) * 100, MaxDrawdown: a.Funds.MaxDrawdown(f.ID),
			InvestorProfitability: a.Funds.Profitability(f.ID, nav), Retention: a.Funds.Retention(f.ID)})
	}
	for _, s := range scoring.Prize1Scores(p1, a.RB.Prizes.Prize1) {
		res.Prize1 = append(res.Prize1, PrizeRow{ID: s.ID, Name: fnames[s.ID], Score: s.Score, Rank: s.Rank})
	}

	// Prizes 2 and 4 look at individual investors only.
	var p2 []scoring.Prize2Input
	var p4 []scoring.Prize4Input
	for _, u := range a.users.all() {
		if u.IsAdmin || u.Role != RoleInvestor {
			continue
		}
		v, ok := values[u.ID]
		if !ok {
			v = a.totalValue(u.ID, navs)
		}
		eligible := u.Status != StatusDisqualified
		p2 = append(p2, scoring.Prize2Input{AccountID: u.ID, FinalValue: v, Eligible: eligible})
		first, _, dd, has := a.Funds.Risk(u.ID)
		ret := 0.0
		if has && first > 0 {
			ret = (float64(v) - float64(first)) / float64(first) * 100
		}
		var hv []float64
		if snap, err := a.Ledger.Snapshot(u.ID); err == nil {
			for _, p := range snap.Positions {
				if px, ok := a.Market.Price(p.Symbol); ok {
					hv = append(hv, float64(px)*float64(p.Qty))
				}
			}
		}
		for id, h := range a.Funds.Holdings(u.ID) {
			if h.Units > 0 {
				hv = append(hv, h.Units*navs[id]*100)
			}
		}
		p4 = append(p4, scoring.Prize4Input{AccountID: u.ID, ReturnPct: ret, MaxDrawdown: dd, HoldingValues: hv, Eligible: eligible && has})
	}
	for _, s := range scoring.Prize2Ranking(p2) {
		res.Prize2 = append(res.Prize2, PrizeRow{ID: s.ID, Name: name(s.ID), Score: dto.Rupees(money.Paise(s.Score)), Rank: s.Rank})
	}
	for _, s := range scoring.Prize4Scores(p4, a.RB.Prizes.Prize4) {
		res.Prize4 = append(res.Prize4, PrizeRow{ID: s.ID, Name: name(s.ID), Score: s.Score, Rank: s.Rank})
	}
	if len(res.Prize2) > 20 {
		res.Prize2 = res.Prize2[:20]
	}
	if len(res.Prize4) > 20 {
		res.Prize4 = res.Prize4[:20]
	}

	// Prize 3: judges' scores for investors who submitted logs at two or more checkpoints.
	type entry struct {
		id    string
		score float64
	}
	var es []entry
	for _, e := range a.LogEntrants() {
		if e.Eligible && e.Total != nil {
			es = append(es, entry{e.AccountID, *e.Total})
		}
	}
	sort.Slice(es, func(i, j int) bool { return es[i].score > es[j].score })
	for i, e := range es {
		res.Prize3 = append(res.Prize3, PrizeRow{ID: e.id, Name: name(e.id), Score: e.score, Rank: i + 1})
	}
	return res
}

type LogEntrant struct {
	AccountID   string              `json:"accountId"`
	Team        string              `json:"team"`
	Logs        []funds.StrategyLog `json:"logs"`
	Checkpoints int                 `json:"checkpoints"`
	Eligible    bool                `json:"eligible"` // wrote at two or more checkpoints
	Scores      map[string]float64  `json:"scores"`
	Total       *float64            `json:"total"`
}

// Rubric is the judging criteria for Prize 3, with their scale and weight (equal until the organisers set them).
type RubricItem struct {
	Criterion string  `json:"criterion"`
	Weight    float64 `json:"weight"`
	MaxScore  float64 `json:"maxScore"`
}

func (a *App) Rubric() []RubricItem {
	rs := a.RB.Prizes.Prize3.Rubric
	out := make([]RubricItem, len(rs))
	for i, r := range rs {
		it := RubricItem{Criterion: r.Criterion, Weight: 1 / float64(len(rs)), MaxScore: 10}
		if r.Weight != nil {
			it.Weight = *r.Weight
		}
		if r.MaxScore != nil {
			it.MaxScore = *r.MaxScore
		}
		out[i] = it
	}
	return out
}

func (a *App) LogEntrants() []LogEntrant {
	byAcct := map[string][]funds.StrategyLog{}
	for _, l := range a.Funds.Logs("") {
		byAcct[l.Account] = append(byAcct[l.Account], l)
	}
	rubric := a.Rubric()
	out := []LogEntrant{}
	for id, logs := range byAcct {
		u, _ := a.users.get(id)
		seen := map[int]bool{}
		for _, l := range logs {
			seen[l.Checkpoint] = true
		}
		e := LogEntrant{AccountID: id, Team: u.DisplayName, Logs: logs, Checkpoints: len(seen), Eligible: len(seen) >= 2 && u.Status != StatusDisqualified, Scores: a.Funds.Scores(id)}
		if len(e.Scores) > 0 {
			var t float64
			for _, r := range rubric {
				if r.MaxScore > 0 {
					t += 100 * r.Weight * math.Min(e.Scores[r.Criterion], r.MaxScore) / r.MaxScore
				}
			}
			e.Total = &t
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Team < out[j].Team })
	return out
}

// ScoreLog stores a judge's scores (per criterion, up to its maximum) for one investor.
func (a *App) ScoreLog(actor User, account string, scores map[string]float64) error {
	u, ok := a.users.get(account)
	if !ok || u.IsAdmin {
		return ErrUnknownUser
	}
	clean := map[string]float64{}
	for _, r := range a.Rubric() {
		v, has := scores[r.Criterion]
		if !has {
			continue
		}
		if math.IsNaN(v) || v < 0 || v > r.MaxScore {
			return bad("invalid_score", fmt.Sprintf("Scores for %q run from 0 to %.0f.", r.Criterion, r.MaxScore))
		}
		clean[r.Criterion] = v
	}
	return a.Do(actor, "Scored a strategy log", u.DisplayName, "", func() error {
		a.fundMu.Lock()
		defer a.fundMu.Unlock()
		ev := funds.Event{Op: funds.OpScore, At: a.now().UnixMilli(), Investor: account, Scores: clean}
		if err := a.fundAppend(ev); err != nil {
			return err
		}
		return a.Funds.Apply(ev)
	})
}
