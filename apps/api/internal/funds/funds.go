// Package funds is the state of Phase 2's investment funds: who runs each fund, who holds its units, the
// allocation caps, the NAV and value series used for risk scores, the checkpoint fees and the strategy logs.
//
// It is pure state with no clock, disk or network. Every change is an Event; the app stores each event in
// the durable log and applies it here, and a restart rebuilds the same state by applying them in order.
package funds

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"sync"

	"stockastic/api/internal/money"
)

// Event operations.
const (
	OpFormed     = "formed"     // the 20 qualifying teams became 10 funds
	OpProfile    = "profile"    // a fund published or changed its name and profile
	OpAlloc      = "alloc"      // an investor put money into a fund
	OpRedeem     = "redeem"     // an investor took money out of a fund
	OpSeries     = "series"     // a once-a-minute sample of values, used for risk scores
	OpCheckpoint = "checkpoint" // a window closed (or the event ended): NAV, AUM and fees
	OpLog        = "log"        // an investor's strategy log entry (Prize 3)
	OpScore      = "score"      // a judge's Prize 3 scores for one investor
	OpFundDQ     = "fundDQ"     // a fund lost eligibility for Prize 1
)

var (
	ErrUnknownFund = errors.New("unknown_fund")
	ErrBadEvent    = errors.New("funds: bad event")
)

type Profile struct {
	Name       string `json:"name"`
	Philosophy string `json:"philosophy"`
	Risk       string `json:"risk"`
	Strategy   string `json:"strategy"`
}

// Formed is one fund at creation: two Phase 1 teams merged (Section 6).
type Formed struct {
	ID      string    `json:"id"`
	Number  int       `json:"number"`
	Account string    `json:"account"`
	Members [2]string `json:"members"` // the stronger team, then the weaker
	Ranks   [2]int    `json:"ranks"`
}

// RankRow is one team's place in the Phase 1 ranking, kept so the result can be shown and audited.
type RankRow struct {
	Account   string `json:"account"`
	Rank      int    `json:"rank"`
	Value     int64  `json:"value"`
	DecidedBy string `json:"decidedBy"`
}

// FundCheckpoint is one fund's figures at a checkpoint (Section 12). Fees are a separate scoring track and are
// never taken out of what investors hold.
type FundCheckpoint struct {
	FundID    string  `json:"fundId"`
	NAV       float64 `json:"nav"`
	AUM       int64   `json:"aum"`
	AvgAUM    int64   `json:"avgAum"`
	Units     float64 `json:"units"`
	MgmtFee   int64   `json:"mgmtFee"`
	PerfFee   int64   `json:"perfFee"`
	HWMBefore float64 `json:"hwmBefore"`
	HWMAfter  float64 `json:"hwmAfter"`
}

type Checkpoint struct {
	Name  string           `json:"name"`
	At    int64            `json:"at"`
	Funds []FundCheckpoint `json:"funds"`
}

type StrategyLog struct {
	Account    string `json:"account"`
	Checkpoint int    `json:"checkpoint"` // 1..3
	Text       string `json:"text"`
	At         int64  `json:"at"`
}

// Event is one durable change. Only the fields for its Op are set.
type Event struct {
	Op string `json:"op"`
	At int64  `json:"at"`

	Funds   []Formed  `json:"funds,omitempty"`
	Ranking []RankRow `json:"ranking,omitempty"`
	Seed    int64     `json:"seed,omitempty"`

	FundID  string   `json:"fundId,omitempty"`
	Profile *Profile `json:"profile,omitempty"`

	Window   int     `json:"window,omitempty"`
	Investor string  `json:"investor,omitempty"`
	Amount   int64   `json:"amount,omitempty"`
	Units    float64 `json:"units,omitempty"`
	NAV      float64 `json:"nav,omitempty"`

	Values map[string]int64   `json:"values,omitempty"` // investor -> total value (series)
	NAVs   map[string]float64 `json:"navs,omitempty"`   // fund -> NAV (series)
	AUMs   map[string]int64   `json:"aums,omitempty"`   // fund -> AUM (series)

	Checkpoint *Checkpoint        `json:"checkpoint,omitempty"`
	Log        *StrategyLog       `json:"log,omitempty"`
	Scores     map[string]float64 `json:"scores,omitempty"` // criterion -> score (score)
}

type Fund struct {
	Formed
	Profile
	Units, MintedUnits, RedeemedUnits float64
	HWM                               float64
	Disqualified                      bool
	peakNAV, maxDD                    float64
	aumSum                            float64
	aumN                              int
	firstNAV                          float64
}

// Holding is one investor's position in one fund.
type Holding struct {
	Units       float64
	Contributed money.Paise
	Redeemed    money.Paise
}

type risk struct {
	first, peak int64
	maxDD       float64
}

// Book is all fund state.
type Book struct {
	mu        sync.RWMutex
	launchNAV float64
	funds     []*Fund
	byID      map[string]*Fund
	holdings  map[string]map[string]*Holding // investor -> fund -> holding
	inflow    map[int]map[string]money.Paise // window -> fund -> net inflow
	ranking   []RankRow
	seed      int64
	formed    bool
	risks     map[string]*risk
	checks    []Checkpoint
	logs      []StrategyLog
	scores    map[string]map[string]float64
}

func NewBook(launchNAV float64) *Book {
	return &Book{
		launchNAV: launchNAV, byID: map[string]*Fund{}, holdings: map[string]map[string]*Holding{},
		inflow: map[int]map[string]money.Paise{}, risks: map[string]*risk{}, scores: map[string]map[string]float64{},
	}
}

func (b *Book) LaunchNAV() float64 { return b.launchNAV }

func (b *Book) Formed() bool { b.mu.RLock(); defer b.mu.RUnlock(); return b.formed }

// Apply applies one event. It is used live (after the event is stored) and again on restart.
func (b *Book) Apply(ev Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch ev.Op {
	case OpFormed:
		if b.formed {
			return fmt.Errorf("%w: funds are already formed", ErrBadEvent)
		}
		for _, f := range ev.Funds {
			fd := &Fund{Formed: f, HWM: b.launchNAV, firstNAV: b.launchNAV, peakNAV: b.launchNAV}
			fd.Profile.Name = fmt.Sprintf("Fund %d", f.Number)
			b.funds = append(b.funds, fd)
			b.byID[f.ID] = fd
		}
		b.ranking, b.seed, b.formed = ev.Ranking, ev.Seed, true
	case OpProfile:
		f, ok := b.byID[ev.FundID]
		if !ok || ev.Profile == nil {
			return ErrUnknownFund
		}
		f.Profile = *ev.Profile
	case OpAlloc, OpRedeem:
		f, ok := b.byID[ev.FundID]
		if !ok {
			return ErrUnknownFund
		}
		h := b.holdings[ev.Investor]
		if h == nil {
			h = map[string]*Holding{}
			b.holdings[ev.Investor] = h
		}
		pos := h[ev.FundID]
		if pos == nil {
			pos = &Holding{}
			h[ev.FundID] = pos
		}
		in := b.inflow[ev.Window]
		if in == nil {
			in = map[string]money.Paise{}
			b.inflow[ev.Window] = in
		}
		if ev.Op == OpAlloc {
			pos.Units += ev.Units
			pos.Contributed += money.Paise(ev.Amount)
			f.Units += ev.Units
			f.MintedUnits += ev.Units
			in[ev.FundID] += money.Paise(ev.Amount)
		} else {
			pos.Units = math.Max(0, pos.Units-ev.Units)
			pos.Redeemed += money.Paise(ev.Amount)
			f.Units = math.Max(0, f.Units-ev.Units)
			f.RedeemedUnits += ev.Units
		}
	case OpSeries:
		for id, v := range ev.Values {
			r := b.risks[id]
			if r == nil {
				r = &risk{first: v, peak: v}
				b.risks[id] = r
			}
			if v > r.peak {
				r.peak = v
			}
			if r.peak > 0 {
				r.maxDD = math.Max(r.maxDD, float64(r.peak-v)/float64(r.peak))
			}
		}
		for id, nav := range ev.NAVs {
			f := b.byID[id]
			if f == nil {
				continue
			}
			if nav > f.peakNAV {
				f.peakNAV = nav
			}
			if f.peakNAV > 0 {
				f.maxDD = math.Max(f.maxDD, (f.peakNAV-nav)/f.peakNAV)
			}
		}
		for id, aum := range ev.AUMs {
			if f := b.byID[id]; f != nil {
				f.aumSum += float64(aum)
				f.aumN++
			}
		}
	case OpCheckpoint:
		if ev.Checkpoint == nil {
			return ErrBadEvent
		}
		for _, fc := range ev.Checkpoint.Funds {
			if f := b.byID[fc.FundID]; f != nil {
				f.HWM = fc.HWMAfter
				f.aumSum, f.aumN = 0, 0
			}
		}
		b.checks = append(b.checks, *ev.Checkpoint)
	case OpLog:
		if ev.Log == nil {
			return ErrBadEvent
		}
		b.logs = append(b.logs, *ev.Log)
	case OpScore:
		if ev.Investor == "" {
			return ErrBadEvent
		}
		b.scores[ev.Investor] = ev.Scores
	case OpFundDQ:
		f, ok := b.byID[ev.FundID]
		if !ok {
			return ErrUnknownFund
		}
		f.Disqualified = true
	default:
		return fmt.Errorf("%w: unknown op %q", ErrBadEvent, ev.Op)
	}
	return nil
}

// ---- reads ----

func (b *Book) Funds() []Fund {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Fund, len(b.funds))
	for i, f := range b.funds {
		out[i] = *f
	}
	return out
}

func (b *Book) Fund(id string) (Fund, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	f, ok := b.byID[id]
	if !ok {
		return Fund{}, false
	}
	return *f, true
}

// FundOfMember is the fund a Phase 1 team now manages, if any.
func (b *Book) FundOfMember(account string) (Fund, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, f := range b.funds {
		if f.Members[0] == account || f.Members[1] == account {
			return *f, true
		}
	}
	return Fund{}, false
}

func (b *Book) Holding(investor, fund string) Holding {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if h := b.holdings[investor][fund]; h != nil {
		return *h
	}
	return Holding{}
}

// Holdings is every fund position of one investor (only funds where units are held or money was put in).
func (b *Book) Holdings(investor string) map[string]Holding {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := map[string]Holding{}
	for id, h := range b.holdings[investor] {
		out[id] = *h
	}
	return out
}

// Investors lists every account that has ever put money into any fund.
func (b *Book) Investors() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.holdings))
	for id := range b.holdings {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Inflow is what investors put into a fund during one allocation window (redemptions do not reduce it: the
// equal-cap rule counts how much each fund has taken in).
func (b *Book) Inflow(window int, fund string) money.Paise {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.inflow[window][fund]
}

func (b *Book) Ranking() []RankRow {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]RankRow(nil), b.ranking...)
}

func (b *Book) Seed() int64 { b.mu.RLock(); defer b.mu.RUnlock(); return b.seed }

func (b *Book) Checkpoints() []Checkpoint {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]Checkpoint(nil), b.checks...)
}

func (b *Book) Logs(account string) []StrategyLog {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var out []StrategyLog
	for _, l := range b.logs {
		if account == "" || l.Account == account {
			out = append(out, l)
		}
	}
	return out
}

func (b *Book) Scores(investor string) map[string]float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := map[string]float64{}
	for k, v := range b.scores[investor] {
		out[k] = v
	}
	return out
}

// Risk is an investor's first and highest value seen in Phase 2 and their largest fall from a peak.
func (b *Book) Risk(investor string) (first, peak int64, maxDrawdown float64, ok bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	r := b.risks[investor]
	if r == nil {
		return 0, 0, 0, false
	}
	return r.first, r.peak, r.maxDD, true
}

// MaxDrawdown is a fund's largest fall in NAV from a peak during Phase 2.
func (b *Book) MaxDrawdown(fund string) float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if f := b.byID[fund]; f != nil {
		return f.maxDD
	}
	return 0
}

// Retention is the share of units ever issued that were still invested (1 = nobody left). It counts units,
// not rupees, so a rising or falling NAV does not change it.
func (b *Book) Retention(fund string) float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	f := b.byID[fund]
	if f == nil || f.MintedUnits <= 0 {
		return 1
	}
	return math.Max(0, 1-f.RedeemedUnits/f.MintedUnits)
}

// Profitability is the share of a fund's investors whose position is in net profit at the given NAV: what
// they hold now plus what they took out, against what they put in.
func (b *Book) Profitability(fund string, nav float64) float64 {
	b.mu.RLock()
	defer b.mu.RUnlock()
	var total, won int
	for _, byFund := range b.holdings {
		h := byFund[fund]
		if h == nil || h.Contributed <= 0 {
			continue
		}
		total++
		now := money.FromRupees(h.Units * nav)
		if now+h.Redeemed > h.Contributed {
			won++
		}
	}
	if total == 0 {
		return 0
	}
	return float64(won) / float64(total)
}

// PlanCheckpoint works out every fund's checkpoint figures without changing anything. The management fee
// is a percentage of the average AUM since the previous checkpoint (charged whatever the performance); the
// performance fee is a percentage of new profit per unit above the high-water mark, on the units then held.
func (b *Book) PlanCheckpoint(name string, at int64, navs map[string]float64, aums map[string]int64, mgmtPct, perfPct float64) Checkpoint {
	b.mu.RLock()
	defer b.mu.RUnlock()
	cp := Checkpoint{Name: name, At: at}
	for _, f := range b.funds {
		nav := navs[f.ID]
		if nav <= 0 {
			nav = b.launchNAV
		}
		avg := float64(aums[f.ID])
		if f.aumN > 0 {
			avg = f.aumSum / float64(f.aumN)
		}
		fc := FundCheckpoint{FundID: f.ID, NAV: nav, AUM: aums[f.ID], AvgAUM: int64(math.Round(avg)), Units: f.Units,
			HWMBefore: f.HWM, HWMAfter: f.HWM}
		fc.MgmtFee = int64(math.Round(avg * mgmtPct / 100))
		if nav > f.HWM {
			fc.PerfFee = int64(money.FromRupees((nav - f.HWM) * perfPct / 100 * f.Units))
			fc.HWMAfter = nav
		}
		cp.Funds = append(cp.Funds, fc)
	}
	return cp
}
