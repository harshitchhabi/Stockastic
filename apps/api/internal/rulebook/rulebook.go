// Package rulebook exposes the event-rule constants (timeline, fees, caps, prize weights,
// rate limits) to the backend. The single source of truth is rulebook.json in this directory,
// embedded in the binary (so it cannot be missing or stale at deploy time) and overridable with a
// file path for an organiser edit that should not need a rebuild. This package is the only place the
// backend reads it, so a rulebook change never touches engine or ledger code. Decoding is strict and
// validated: the process refuses to start on a bad rulebook rather than run on wrong rules.
package rulebook

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"stockastic/api/internal/money"
)

type Stage string

const (
	StagePhase1     Stage = "phase1"
	StageTransition Stage = "transition"
	StagePhase2     Stage = "phase2"
	StageClosing    Stage = "closing"
)

type TieBreakStep string

const (
	TieBreakPeakValue         TieBreakStep = "peak_portfolio_value"
	TieBreakFewerTransactions TieBreakStep = "fewer_transactions"
	TieBreakCoinToss          TieBreakStep = "coin_toss"
)

// Status says how final a rulebook value is. A path with no Provenance entry is fixed by the rulebook.
type Status string

const (
	StatusRecommended Status = "recommended"
	StatusTBF         Status = "tbf"
	StatusAssumption  Status = "assumption"
)

type Provenance struct {
	Status  Status `json:"status"`
	Section string `json:"section"`
	Note    string `json:"note,omitempty"`
}

// Rulebook mirrors rulebook.json. Decoding is strict: an unknown key is an error,
// so the JSON and this struct cannot drift apart silently.
type Rulebook struct {
	Version       string                `json:"version"`
	Source        string                `json:"source"`
	Roles         []string              `json:"roles"`
	Event         Event                 `json:"event"`
	Market        Market                `json:"market"`
	Teams         Teams                 `json:"teams"`
	Accounts      Accounts              `json:"accounts"`
	Qualification Qualification         `json:"qualification"`
	Fund          Fund                  `json:"fund"`
	Fees          Fees                  `json:"fees"`
	News          News                  `json:"news"`
	RateLimits    RateLimits            `json:"rateLimits"`
	Leaderboard   Leaderboard           `json:"leaderboard"`
	Disputes      Disputes              `json:"disputes"`
	Prizes        Prizes                `json:"prizes"`
	Technical     Technical             `json:"technical"`
	Provenance    map[string]Provenance `json:"provenance"`
}

type Event struct {
	// TotalMinutes is the length the rulebook plans for. It is informational only: the real length is the
	// sum of the timeline's blocks, and the organiser may change block lengths during the event.
	TotalMinutes int          `json:"totalMinutes,omitempty"`
	Participants Participants `json:"participants"`
	Timeline     []Block      `json:"timeline"`
}

type Participants struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type Block struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	PublicLabel      string `json:"publicLabel"`
	DurationMin      int    `json:"durationMin"`
	Stage            Stage  `json:"stage"`
	MarketOpen       bool   `json:"marketOpen"`
	AllocationWindow *int   `json:"allocationWindow"`
	FreezeSnapshot   string `json:"freezeSnapshot,omitempty"`
	RegimeEvent      bool   `json:"regimeEvent,omitempty"`
}

func (b Block) Duration() time.Duration { return time.Duration(b.DurationMin) * time.Minute }

type Market struct {
	SymbolCount int `json:"symbolCount"`
}

type Teams struct {
	InvestorTeamSize int `json:"investorTeamSize"`
	FundTeamSize     int `json:"fundTeamSize"`
	FundManagerSeats int `json:"fundManagerSeats"`
}

type Accounts struct {
	StartingCapital float64 `json:"startingCapital"`
	Currency        string  `json:"currency"`
}

type Qualification struct {
	QualifyingTeams int            `json:"qualifyingTeams"`
	FundCount       int            `json:"fundCount"`
	Pairing         string         `json:"pairing"`
	TieBreak        []TieBreakStep `json:"tieBreak"`
}

type Fund struct {
	LaunchNav                  float64 `json:"launchNav"`
	MinInvestmentAbsolute      float64 `json:"minInvestmentAbsolute"`
	MinInvestmentWalletPercent float64 `json:"minInvestmentWalletPercent"`
	MaxSingleFundWalletPercent float64 `json:"maxSingleFundWalletPercent"`
	MandatoryAllocationPercent float64 `json:"mandatoryAllocationPercent"`
	SeedCapital                float64 `json:"seedCapital"`
}

type Fees struct {
	ManagementFeePercent  float64 `json:"managementFeePercent"`
	PerformanceFeePercent float64 `json:"performanceFeePercent"`
	ManagementFeeBasis    string  `json:"managementFeeBasis"`
}

type News struct {
	FundManagerLeadSeconds int `json:"fundManagerLeadSeconds"`
}

func (n News) Lead() time.Duration { return time.Duration(n.FundManagerLeadSeconds) * time.Second }

type RateLimits struct {
	TradesPerWindow int    `json:"tradesPerWindow"`
	WindowSeconds   int    `json:"windowSeconds"`
	Scope           string `json:"scope"`
}

func (r RateLimits) Window() time.Duration { return time.Duration(r.WindowSeconds) * time.Second }

type Leaderboard struct {
	RefreshSeconds   int      `json:"refreshSeconds"`
	NavSampleSeconds int      `json:"navSampleSeconds"`
	InvestorFields   []string `json:"investorFields"`
	FundFields       []string `json:"fundFields"`
	ExposeHoldings   bool     `json:"exposeHoldings"`
	// VisibleToParticipants says whether teams see the standings at all. Section 15 describes a public
	// board, but the organiser has asked for standings to be organiser-only, so this defaults to false.
	VisibleToParticipants bool `json:"visibleToParticipants"`
}

type Disputes struct {
	ExpeditedPerPhase          int    `json:"expeditedPerPhase"`
	OverflowQueue              string `json:"overflowQueue"`
	RaiseWithinMinutes         int    `json:"raiseWithinMinutes"`
	ExpeditedTurnaroundMinutes int    `json:"expeditedTurnaroundMinutes"`
}

type Prizes struct {
	Prize1 Prize1 `json:"prize1"`
	Prize4 Prize4 `json:"prize4"`
	Prize3 Prize3 `json:"prize3"`
}

type Prize1 struct {
	Performance           float64 `json:"performance"`
	RiskManagement        float64 `json:"riskManagement"`
	InvestorProfitability float64 `json:"investorProfitability"`
	Retention             float64 `json:"retention"`
}

type Prize4 struct {
	RiskAdjustedReturn         float64 `json:"riskAdjustedReturn"`
	DrawdownControl            float64 `json:"drawdownControl"`
	Diversification            float64 `json:"diversification"`
	DiversificationCapHoldings float64 `json:"diversificationCapHoldings"`
}

type Prize3 struct {
	StrategyLogCheckpoints int               `json:"strategyLogCheckpoints"`
	Rubric                 []RubricCriterion `json:"rubric"`
}

type RubricCriterion struct {
	Criterion string   `json:"criterion"`
	Weight    *float64 `json:"weight"`
	MaxScore  *float64 `json:"maxScore"`
}

type Technical struct {
	MinimumDeviceRequirements *string `json:"minimumDeviceRequirements"`
}

//go:embed rulebook.json
var embedded []byte

// Default parses and validates the rulebook embedded in the binary.
func Default() (*Rulebook, error) { return Parse(embedded) }

// LoadOrDefault uses the file at path when one is given, else the embedded rulebook. The backend
// calls it once at startup and refuses to run on any error.
func LoadOrDefault(path string) (*Rulebook, error) {
	if path == "" {
		return Default()
	}
	return Load(path)
}

// Load reads, strictly decodes and validates a rulebook file.
func Load(path string) (*Rulebook, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read rulebook: %w", err)
	}
	return Parse(raw)
}

// Parse strictly decodes and validates rulebook JSON.
func Parse(raw []byte) (*Rulebook, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var rb Rulebook
	if err := dec.Decode(&rb); err != nil {
		return nil, fmt.Errorf("decode rulebook: %w", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, fmt.Errorf("decode rulebook: %w", err)
	}
	if err := rb.validate(generic); err != nil {
		return nil, fmt.Errorf("invalid rulebook: %w", err)
	}
	return &rb, nil
}

func (r *Rulebook) validate(generic map[string]any) error {
	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	if len(r.Event.Timeline) == 0 {
		bad("timeline is empty")
	}
	total, seen := 0, map[string]bool{}
	var windows []int
	freezes := map[string]int{}
	regime := 0
	for _, b := range r.Event.Timeline {
		total += b.DurationMin
		if b.ID == "" || seen[b.ID] {
			bad("timeline block id %q empty or duplicated", b.ID)
		}
		seen[b.ID] = true
		if b.DurationMin <= 0 {
			bad("block %q has non-positive duration", b.ID)
		}
		switch b.Stage {
		case StagePhase1, StageTransition, StagePhase2, StageClosing:
		default:
			bad("block %q has unknown stage %q", b.ID, b.Stage)
		}
		if b.AllocationWindow != nil {
			windows = append(windows, *b.AllocationWindow)
		}
		if b.FreezeSnapshot != "" {
			freezes[b.FreezeSnapshot]++
		}
		if b.RegimeEvent {
			regime++
		}
	}
	if total <= 0 {
		bad("the timeline has no duration")
	}
	if len(windows) == 0 {
		bad("no allocation windows in timeline")
	}
	for i, w := range windows {
		if w != i {
			bad("allocation windows must be numbered 0..N-1 in timeline order, got %v", windows)
			break
		}
	}
	if freezes["phase1"] != 1 || freezes["final"] != 1 || len(freezes) != 2 {
		bad("timeline needs exactly one phase1 and one final freezeSnapshot block, got %v", freezes)
	}
	if regime > 1 {
		bad("at most one block may carry the confidential regimeEvent marker, got %d", regime)
	}
	if r.Event.Participants.Min > r.Event.Participants.Max {
		bad("participants.min > participants.max")
	}

	if r.Qualification.QualifyingTeams != 2*r.Qualification.FundCount {
		bad("qualification.qualifyingTeams (%d) must be 2 x fundCount (%d)", r.Qualification.QualifyingTeams, r.Qualification.FundCount)
	}
	if r.Teams.FundManagerSeats != r.Qualification.FundCount*r.Teams.FundTeamSize {
		bad("teams.fundManagerSeats (%d) must be fundCount x fundTeamSize", r.Teams.FundManagerSeats)
	}
	tb := map[TieBreakStep]bool{}
	for _, s := range r.Qualification.TieBreak {
		switch s {
		case TieBreakPeakValue, TieBreakFewerTransactions, TieBreakCoinToss:
		default:
			bad("unknown tie-break step %q", s)
		}
		if tb[s] {
			bad("duplicate tie-break step %q", s)
		}
		tb[s] = true
	}
	if r.Market.SymbolCount <= 0 {
		bad("market.symbolCount must be positive")
	}
	if r.Accounts.StartingCapital <= 0 {
		bad("accounts.startingCapital must be positive")
	}
	if r.RateLimits.TradesPerWindow <= 0 || r.RateLimits.WindowSeconds <= 0 {
		bad("rateLimits must be positive")
	}
	if r.Fund.LaunchNav <= 0 {
		bad("fund.launchNav must be positive")
	}
	for name, v := range map[string]float64{
		"fund.minInvestmentWalletPercent": r.Fund.MinInvestmentWalletPercent,
		"fund.maxSingleFundWalletPercent": r.Fund.MaxSingleFundWalletPercent,
		"fund.mandatoryAllocationPercent": r.Fund.MandatoryAllocationPercent,
		"fees.managementFeePercent":       r.Fees.ManagementFeePercent,
		"fees.performanceFeePercent":      r.Fees.PerformanceFeePercent,
	} {
		if v < 0 || v > 100 {
			bad("%s = %v is outside 0-100", name, v)
		}
	}
	p1 := r.Prizes.Prize1
	if s := p1.Performance + p1.RiskManagement + p1.InvestorProfitability + p1.Retention; math.Abs(s-1) > 1e-9 {
		bad("prize1 weights sum to %v, expected 1", s)
	}
	p4 := r.Prizes.Prize4
	if s := p4.RiskAdjustedReturn + p4.DrawdownControl + p4.Diversification; math.Abs(s-1) > 1e-9 {
		bad("prize4 weights sum to %v, expected 1", s)
	}

	for path, pv := range r.Provenance {
		if !pathExists(generic, path) {
			bad("provenance path %q does not exist in the rulebook", path)
		}
		switch pv.Status {
		case StatusRecommended, StatusTBF, StatusAssumption:
		default:
			bad("provenance %q has unknown status %q", path, pv.Status)
		}
	}
	return errors.Join(errs...)
}

func pathExists(root map[string]any, dotted string) bool {
	var cur any = root
	for _, k := range strings.Split(dotted, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return false
		}
		if cur, ok = m[k]; !ok {
			return false
		}
	}
	return true
}

// WindowCount is how many allocation windows the timeline defines (4 in v1.1). Code derives it from
// here rather than assuming a number, so a final rulebook with a different count needs no code edit.
func (r *Rulebook) WindowCount() int {
	n := 0
	for _, b := range r.Event.Timeline {
		if b.AllocationWindow != nil {
			n++
		}
	}
	return n
}

// StartingCapital is the identical Phase-1 capital every team begins with (Sec 4).
func (r *Rulebook) StartingCapital() money.Paise { return money.FromRupees(r.Accounts.StartingCapital) }

// Status reports how final the value at a dotted path is; ok=false means it is fixed by the rulebook.
func (r *Rulebook) Status(path string) (Provenance, bool) {
	p, ok := r.Provenance[path]
	return p, ok
}

// BlockOffsets returns each timeline block's start offset from event start (T+00:00).
func (r *Rulebook) BlockOffsets() []time.Duration {
	out := make([]time.Duration, len(r.Event.Timeline))
	var acc time.Duration
	for i, b := range r.Event.Timeline {
		out[i] = acc
		acc += b.Duration()
	}
	return out
}

// TotalDuration is the scheduled event length: the sum of the timeline's blocks. It is not fixed at five
// hours; a shorter or longer schedule is valid.
func (r *Rulebook) TotalDuration() time.Duration {
	var d time.Duration
	for _, b := range r.Event.Timeline {
		d += b.Duration()
	}
	return d
}

// PublicBlock is the participant-facing view of a timeline block (Sec 13/17): start and duration
// only. It has no regime-event marker and no internal label, by construction.
type PublicBlock struct {
	ID               string `json:"id"`
	Label            string `json:"label"`
	StartMin         int    `json:"startMin"`
	DurationMin      int    `json:"durationMin"`
	Stage            Stage  `json:"stage"`
	MarketOpen       bool   `json:"marketOpen"`
	AllocationWindow *int   `json:"allocationWindow"`
}

func (r *Rulebook) PublicTimeline() []PublicBlock {
	out := make([]PublicBlock, len(r.Event.Timeline))
	start := 0
	for i, b := range r.Event.Timeline {
		out[i] = PublicBlock{ID: b.ID, Label: b.PublicLabel, StartMin: start, DurationMin: b.DurationMin,
			Stage: b.Stage, MarketOpen: b.MarketOpen, AllocationWindow: b.AllocationWindow}
		start += b.DurationMin
	}
	return out
}

// PublicConfig is what participants may see, served by GET /api/config. It is built field by field
// (never by serialising Rulebook) so a confidential field added to the rulebook later stays private
// until someone consciously adds it here. Organiser-only: the internal block labels, the
// regime-event marker, the provenance notes (which value is still TBF, and why), and the
// technical/role bookkeeping.
type PublicConfig struct {
	Version       string        `json:"version"`
	Event         PublicEvent   `json:"event"`
	Market        Market        `json:"market"`
	Teams         Teams         `json:"teams"`
	Accounts      Accounts      `json:"accounts"`
	Qualification Qualification `json:"qualification"`
	Fund          Fund          `json:"fund"`
	Fees          Fees          `json:"fees"`
	News          News          `json:"news"`
	RateLimits    RateLimits    `json:"rateLimits"`
	Leaderboard   Leaderboard   `json:"leaderboard"`
	Disputes      Disputes      `json:"disputes"`
	Prizes        Prizes        `json:"prizes"`
}

type PublicEvent struct {
	TotalMinutes int           `json:"totalMinutes"`
	Timeline     []PublicBlock `json:"timeline"`
}

func (r *Rulebook) Public() PublicConfig {
	return PublicConfig{
		Version:       r.Version,
		Event:         PublicEvent{TotalMinutes: int(r.TotalDuration() / time.Minute), Timeline: r.PublicTimeline()},
		Market:        r.Market,
		Teams:         r.Teams,
		Accounts:      r.Accounts,
		Qualification: r.Qualification,
		Fund:          r.Fund,
		Fees:          r.Fees,
		News:          r.News,
		RateLimits:    r.RateLimits,
		Leaderboard:   r.Leaderboard,
		Disputes:      r.Disputes,
		Prizes:        r.Prizes,
	}
}
