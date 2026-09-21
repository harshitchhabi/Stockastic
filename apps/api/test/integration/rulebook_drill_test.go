// Package integration holds cross-package tests. This file is the "final rulebook" drill: it edits the
// real rulebook.json the way the organisers plausibly will, and checks two things —
//
//  1. VALUE changes (numbers, orderings, sizes) are accepted and actually reach the code that uses them,
//     with no code edit.
//  2. STRUCTURAL changes (a field the code does not know, a new stage, a new tie-break rule, a broken
//     schedule) are REJECTED at startup, loudly, rather than being silently ignored. The number of
//     allocation windows is data, not structure: it is derived from the timeline.
//
// It is the executable answer to "what happens when the final rulebook arrives".
package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"stockastic/api/internal/disputes"
	"stockastic/api/internal/eventclock"
	"stockastic/api/internal/money"
	"stockastic/api/internal/news"
	"stockastic/api/internal/ratelimit"
	"stockastic/api/internal/rulebook"
	"stockastic/api/internal/scoring"
)

const rulebookPath = "../../internal/rulebook/rulebook.json"

// edit applies fn to the real rulebook as generic JSON and parses the result.
func edit(t *testing.T, fn func(m map[string]any)) (*rulebook.Rulebook, error) {
	t.Helper()
	raw, err := os.ReadFile(rulebookPath)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	fn(m)
	out, _ := json.Marshal(m)
	return rulebook.Parse(out)
}

func sub(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		m = m[k].(map[string]any)
	}
	return m
}

func timeline(m map[string]any) []any { return sub(m, "event")["timeline"].([]any) }

func block(m map[string]any, id string) map[string]any {
	for _, b := range timeline(m) {
		if bm := b.(map[string]any); bm["id"] == id {
			return bm
		}
	}
	panic("no block " + id)
}

func mustEdit(t *testing.T, fn func(m map[string]any)) *rulebook.Rulebook {
	t.Helper()
	rb, err := edit(t, fn)
	if err != nil {
		t.Fatalf("this is a value-only change and must be accepted: %v", err)
	}
	return rb
}

// ---- 1. value changes: accepted, and they reach the running code ------------------------------

func TestValueChangesFlowThroughWithNoCodeEdit(t *testing.T) {
	t.Run("fee percentages reach the fee maths", func(t *testing.T) {
		rb := mustEdit(t, func(m map[string]any) {
			sub(m, "fees")["managementFeePercent"] = 2.0
			sub(m, "fees")["performanceFeePercent"] = 10.0
		})
		t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
		_, fee := scoring.ManagementFee([]scoring.AumSample{{At: t0, AUM: money.FromRupees(1_000_000)}}, t0, t0.Add(time.Minute), rb.Fees.ManagementFeePercent, 1)
		if fee != money.FromRupees(20_000) {
			t.Errorf("management fee = %v, want 20,000 at 2%%", fee)
		}
		_, perf := scoring.FinalPerformanceFee(100, 110, 1000, rb.Fees.PerformanceFeePercent, 1)
		if perf != money.FromRupees(1000) {
			t.Errorf("performance fee = %v, want 1,000 at 10%% of a 10 gain on 1000 units", perf)
		}
	})

	t.Run("a reordered tie-break list changes who ranks higher", func(t *testing.T) {
		st := []scoring.Standing{
			{AccountID: "high-peak-many-trades", FinalValue: 500, PeakValue: 900, Transactions: 50},
			{AccountID: "low-peak-few-trades", FinalValue: 500, PeakValue: 100, Transactions: 5},
		}
		def := mustEdit(t, func(m map[string]any) {})
		if got := scoring.RankPhase1(st, def.Qualification.TieBreak, nil); got[0].AccountID != "high-peak-many-trades" {
			t.Fatalf("v1.1 order (peak first): %v", got[0].AccountID)
		}
		flipped := mustEdit(t, func(m map[string]any) {
			sub(m, "qualification")["tieBreak"] = []any{"fewer_transactions", "peak_portfolio_value", "coin_toss"}
		})
		if got := scoring.RankPhase1(st, flipped.Qualification.TieBreak, nil); got[0].AccountID != "low-peak-few-trades" {
			t.Errorf("with transactions first the leaner team must win: %v", got[0].AccountID)
		}
		short := mustEdit(t, func(m map[string]any) { sub(m, "qualification")["tieBreak"] = []any{"coin_toss"} })
		if len(short.Qualification.TieBreak) != 1 {
			t.Error("a shorter tie-break list is a legitimate value change")
		}
	})

	t.Run("the trade-rate limit is entirely data", func(t *testing.T) {
		rb := mustEdit(t, func(m map[string]any) {
			sub(m, "rateLimits")["tradesPerWindow"] = 3
			sub(m, "rateLimits")["windowSeconds"] = 30
		})
		l := ratelimit.FromRulebook(rb.RateLimits)
		now := time.Now()
		for i := 0; i < 3; i++ {
			if ok, _ := l.Allow("a", now); !ok {
				t.Fatalf("trade %d should be allowed at 3 per window", i+1)
			}
		}
		if ok, _ := l.Allow("a", now); ok {
			t.Error("the 4th must be refused")
		}
		if ok, _ := l.Allow("a", now.Add(30*time.Second)); !ok {
			t.Error("the window is 30s now")
		}
	})

	t.Run("news lead and dispute limits", func(t *testing.T) {
		rb := mustEdit(t, func(m map[string]any) {
			sub(m, "news")["fundManagerLeadSeconds"] = 90
			sub(m, "disputes")["expeditedPerPhase"] = 5
		})
		if rb.News.Lead() != 90*time.Second {
			t.Errorf("lead = %v", rb.News.Lead())
		}
		_ = news.Config{Lead: rb.News.Lead()}
		tr := disputes.New(disputes.FromRulebook(rb.Disputes))
		var q disputes.Queue
		for i := 0; i < 5; i++ {
			q = tr.Raise("a", disputes.Phase2, disputes.CategoryOther, "", time.Time{}, time.Now()).Queue
		}
		if q != disputes.QueueExpedited || tr.Raise("a", disputes.Phase2, disputes.CategoryOther, "", time.Time{}, time.Now()).Queue != disputes.QueueStandard {
			t.Error("5 expedited then standard")
		}
	})

	t.Run("rescheduling blocks (same 300 minutes) moves when the market opens", func(t *testing.T) {
		rb := mustEdit(t, func(m map[string]any) {
			block(m, "p1_trading")["durationMin"] = 50 // Phase 1 trading 40 -> 50
			block(m, "results")["durationMin"] = 10    // ...paid for by a shorter results block
		})
		ft := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
		now := ft
		c := eventclock.New(rb, func() time.Time { return now }, nil)
		_ = c.Start()
		now = ft.Add(65 * time.Minute) // 40-min trading would be frozen by now; 50-min is still open
		if !c.MarketOpen() {
			t.Error("with a 50-minute Phase 1 the market must still be open at T+65")
		}
		now = ft.Add(71 * time.Minute)
		if c.MarketOpen() {
			t.Error("...and frozen after T+70")
		}
	})

	t.Run("more qualifiers and funds: pairing and caps scale", func(t *testing.T) {
		rb := mustEdit(t, func(m map[string]any) {
			sub(m, "qualification")["qualifyingTeams"] = 24
			sub(m, "qualification")["fundCount"] = 12
			sub(m, "teams")["fundManagerSeats"] = 72
		})
		ranks := make([]int, rb.Qualification.QualifyingTeams)
		for i := range ranks {
			ranks[i] = i + 1
		}
		pairs, err := scoring.MirrorPairing(ranks)
		if err != nil || len(pairs) != rb.Qualification.FundCount {
			t.Fatalf("pairs=%d err=%v", len(pairs), err)
		}
		if pairs[0].Weaker != 24 || pairs[11].Stronger != 12 || pairs[11].Weaker != 13 {
			t.Errorf("mirror pairing must use N+1-k for any N: %+v", pairs)
		}
		funds := make([]scoring.CapFund, rb.Qualification.FundCount)
		for i := range funds {
			funds[i] = scoring.CapFund{FundID: string(rune('a' + i)), Headcount: rb.Teams.FundTeamSize, Active: true}
		}
		_, caps := scoring.AllocationCaps(funds, money.FromRupees(1_200_000), rb.Qualification.FundCount, rb.Teams.FundTeamSize)
		if caps[0].Tranche != money.FromRupees(100_000) {
			t.Errorf("pool split 12 ways = %v, want 100,000 each", caps[0].Tranche)
		}
	})

	t.Run("a different NUMBER of allocation windows needs no code edit", func(t *testing.T) {
		five := mustEdit(t, func(m map[string]any) { block(m, "p2_t4")["allocationWindow"] = 4 })
		if five.WindowCount() != 5 {
			t.Fatalf("window count = %d", five.WindowCount())
		}
		ft := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
		now := ft
		c := eventclock.New(five, func() time.Time { return now }, nil)
		_ = c.Start()
		now = ft.Add(240 * time.Minute) // inside p2_t4 (T+230..T+255), which is now window 4
		if !c.WindowOpen(4) {
			t.Error("window 4 must open when its block starts — it used to be silently unreachable")
		}
		if err := c.SetWindowOverride(4, nil); err != nil {
			t.Errorf("window 4 overrides must exist: %v", err)
		}
		three := mustEdit(t, func(m map[string]any) { block(m, "w3")["allocationWindow"] = nil })
		c3 := eventclock.New(three, func() time.Time { return now }, nil)
		if three.WindowCount() != 3 || c3.SetWindowOverride(3, nil) == nil || c3.WindowOpen(3) {
			t.Error("with 3 windows, window 3 must not exist (and must not panic)")
		}
	})

	t.Run("starting capital, prize weights, fund rules", func(t *testing.T) {
		rb := mustEdit(t, func(m map[string]any) {
			sub(m, "accounts")["startingCapital"] = 500000
			sub(m, "prizes", "prize1")["performance"] = 0.5
			sub(m, "prizes", "prize1")["retention"] = 0.0
			sub(m, "fund")["maxSingleFundWalletPercent"] = 50
		})
		if rb.StartingCapital() != money.FromRupees(500_000) {
			t.Error("starting capital")
		}
		got := scoring.Prize1Scores([]scoring.Prize1Input{
			{FundID: "a", NavReturnPct: 10, MaxDrawdown: 0.1, InvestorProfitability: 0.5, Retention: 0},
			{FundID: "b", NavReturnPct: 0, MaxDrawdown: 0.1, InvestorProfitability: 0.5, Retention: 1},
		}, rb.Prizes.Prize1)
		if got[0].ID != "a" {
			t.Errorf("with retention weighted 0 the higher-return fund must win: %v", got)
		}
	})

	t.Run("resolving TBF items: rubric weights set, provenance tags removed", func(t *testing.T) {
		rb := mustEdit(t, func(m map[string]any) {
			for _, c := range sub(m, "prizes", "prize3")["rubric"].([]any) {
				c.(map[string]any)["weight"] = 0.25
				c.(map[string]any)["maxScore"] = 10.0
			}
			sub(m, "technical")["minimumDeviceRequirements"] = "Chrome 120+, 8GB RAM"
			delete(sub(m, "provenance"), "prizes.prize3.rubric")
			delete(sub(m, "provenance"), "fees.managementFeePercent") // exact figure now final
			sub(m, "fees")["managementFeePercent"] = 1.75
		})
		if _, tagged := rb.Status("fees.managementFeePercent"); tagged {
			t.Error("a finalised value must lose its Recommended tag")
		}
		if w := rb.Prizes.Prize3.Rubric[0].Weight; w == nil || *w != 0.25 {
			t.Error("rubric weight")
		}
	})
}

// ---- 2. structural changes: rejected loudly, never silently ignored ---------------------------

func TestStructuralChangesAreRejectedAtStartupNotSilentlyIgnored(t *testing.T) {
	cases := map[string]struct {
		fn   func(m map[string]any)
		want string
	}{
		"a brand-new rulebook field the code does not know": {
			func(m map[string]any) { sub(m, "fees")["exitLoadPercent"] = 1.0 }, "unknown field",
		},
		"a brand-new top-level section": {
			func(m map[string]any) { m["shortSelling"] = map[string]any{"allowed": true} }, "unknown field",
		},
		"allocation windows numbered out of order": {
			func(m map[string]any) {
				block(m, "w1")["allocationWindow"] = 2
				block(m, "w2")["allocationWindow"] = 1
			}, "allocation windows must be numbered",
		},
		"a new stage name": {
			func(m map[string]any) { block(m, "briefing")["stage"] = "warmup" }, "unknown stage",
		},
		"a new tie-break rule": {
			func(m map[string]any) {
				sub(m, "qualification")["tieBreak"] = []any{"highest_sharpe_ratio"}
			}, "unknown tie-break",
		},
		"prize weights that do not sum to 1": {
			func(m map[string]any) { sub(m, "prizes", "prize1")["performance"] = 0.5 }, "prize1 weights",
		},
		"qualifiers that no longer pair into the fund count": {
			func(m map[string]any) { sub(m, "qualification")["qualifyingTeams"] = 25 }, "2 x fundCount",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := edit(t, c.fn)
			if err == nil {
				t.Fatal("this needs a code change, so the loader must refuse it")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q should mention %q", err, c.want)
			}
		})
	}
}
