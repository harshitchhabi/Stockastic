package scoring

import (
	"errors"
	"math"
	"testing"
	"time"

	"stockastic/api/internal/money"
	"stockastic/api/internal/rulebook"
)

func rb(t *testing.T) *rulebook.Rulebook {
	t.Helper()
	r, err := rulebook.Default()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rs(r float64) money.Paise { return money.FromRupees(r) }

func near(t *testing.T, name string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("%s = %v, want %v", name, got, want)
	}
}

func TestMirrorPairingSec6(t *testing.T) {
	ranks := make([]int, 20)
	for i := range ranks {
		ranks[i] = i + 1
	}
	pairs, err := MirrorPairing(ranks)
	if err != nil {
		t.Fatal(err)
	}
	if len(pairs) != 10 {
		t.Fatalf("got %d funds, want 10", len(pairs))
	}
	want := map[int][2]int{1: {1, 20}, 3: {3, 18}, 10: {10, 11}}
	for _, p := range pairs {
		if p.Stronger+p.Weaker != 21 {
			t.Errorf("fund %d pairs rank %d + %d, want sum 21", p.FundNumber, p.Stronger, p.Weaker)
		}
		if w, ok := want[p.FundNumber]; ok && (p.Stronger != w[0] || p.Weaker != w[1]) {
			t.Errorf("fund %d = %d+%d, want %v", p.FundNumber, p.Stronger, p.Weaker, w)
		}
	}
	for _, bad := range [][]int{nil, {1, 2, 3}} {
		if _, err := MirrorPairing(bad); err == nil {
			t.Errorf("MirrorPairing(%v) should fail", bad)
		}
	}
}

func TestRankPhase1TieBreaksSec5(t *testing.T) {
	tb := rb(t).Qualification.TieBreak
	s := func(id string, final, peak int64, txns int) Standing {
		return Standing{AccountID: id, FinalValue: money.Paise(final), PeakValue: money.Paise(peak), Transactions: txns}
	}
	t.Run("by final value", func(t *testing.T) {
		r := RankPhase1([]Standing{s("a", 100, 100, 1), s("b", 300, 300, 1), s("c", 200, 200, 1)}, tb, nil)
		if r[0].AccountID != "b" || r[1].AccountID != "c" || r[2].AccountID != "a" {
			t.Errorf("order = %v %v %v", r[0].AccountID, r[1].AccountID, r[2].AccountID)
		}
	})
	t.Run("value tie -> higher peak first", func(t *testing.T) {
		r := RankPhase1([]Standing{s("low", 500, 510, 5), s("high", 500, 600, 50)}, tb, nil)
		if r[0].AccountID != "high" || r[1].DecidedBy != DecidedByPeak {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("then fewer transactions", func(t *testing.T) {
		r := RankPhase1([]Standing{s("busy", 500, 600, 40), s("lean", 500, 600, 10)}, tb, nil)
		if r[0].AccountID != "lean" || r[1].DecidedBy != DecidedByFewerTxns {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("then the supervised coin toss", func(t *testing.T) {
		toss := map[string]int64{"x": 2, "y": 1}
		r := RankPhase1([]Standing{s("x", 500, 600, 10), s("y", 500, 600, 10)}, tb, func(id string) int64 { return toss[id] })
		if r[0].AccountID != "y" || r[1].DecidedBy != DecidedByCoinToss {
			t.Errorf("got %+v", r)
		}
	})
	t.Run("only the configured steps are used", func(t *testing.T) {
		r := RankPhase1([]Standing{s("a", 500, 900, 1), s("b", 500, 100, 1)},
			[]rulebook.TieBreakStep{rulebook.TieBreakFewerTransactions}, nil)
		if r[1].DecidedBy != DecidedByUnresolved {
			t.Errorf("decidedBy = %s, want unresolved", r[1].DecidedBy)
		}
	})
	t.Run("input is not mutated", func(t *testing.T) {
		in := []Standing{s("a", 1, 1, 1), s("b", 2, 2, 1)}
		RankPhase1(in, tb, nil)
		if in[0].AccountID != "a" {
			t.Error("RankPhase1 reordered its input slice")
		}
	})
}

func TestUnitsNAVAndEntryRulesSec10(t *testing.T) {
	f := rb(t).Fund
	if got := NavPerUnit(0, 0, f.LaunchNav); got != 100 {
		t.Errorf("empty fund NAV = %v, want launch 100", got)
	}
	u, _ := UnitsForAmount(rs(50000), 100)
	near(t, "units at NAV 100", u, 500)
	u, _ = UnitsForAmount(rs(50000), 125)
	near(t, "units at NAV 125", u, 400)
	near(t, "NAV", NavPerUnit(rs(1158000), 10000, 100), 115.8)
	if _, err := UnitsForAmount(rs(1), 0); err == nil {
		t.Error("zero NAV must be rejected")
	}

	if got := MinInvestment(rs(1_000_000), f); got != rs(5000) {
		t.Errorf("min for a 10L wallet = %v, want 5000", got)
	}
	if got := MinInvestment(rs(60_000), f); got != rs(3000) {
		t.Errorf("min for a 60k wallet = %v, want 3000 (5%%)", got)
	}

	base := AllocationInput{Wallet: rs(1_000_000), Cash: rs(1_000_000)}
	cases := []struct {
		name string
		in   AllocationInput
		want error
	}{
		{"below minimum", with(base, rs(4999), 0), ErrBelowMinimum},
		{"over the 60% single-fund cap", with(base, rs(600_001), 0), ErrExceedsSingleFundCap},
		{"exactly 60% is allowed", with(base, rs(600_000), 0), nil},
		{"cap includes what is already held", AllocationInput{Amount: rs(200_000), Wallet: rs(1_000_000), Cash: rs(500_000), ExistingFundValue: rs(450_000)}, ErrExceedsSingleFundCap},
		{"cannot spend cash you lack", AllocationInput{Amount: rs(100_000), Wallet: rs(1_000_000), Cash: rs(50_000)}, ErrInsufficientCash},
	}
	for _, c := range cases {
		if err := CheckAllocation(c.in, f); !errors.Is(err, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, err, c.want)
		}
	}
}

func with(b AllocationInput, amount, existing money.Paise) AllocationInput {
	b.Amount, b.ExistingFundValue = amount, existing
	return b
}

func TestAllocationCapsSec9And10(t *testing.T) {
	f := func(id string, inflow money.Paise, headcount int, active bool) CapFund {
		return CapFund{FundID: id, Headcount: headcount, InflowThisWindow: inflow, Active: active}
	}
	t.Run("mandatory pool splits equally", func(t *testing.T) {
		pool := MandatoryPool([]money.Paise{rs(1_000_000), rs(1_000_000)}, 5)
		if pool != rs(100_000) {
			t.Fatalf("pool = %v", pool)
		}
		lvl, st := AllocationCaps([]CapFund{f("A", 0, 6, true), f("B", 0, 6, true)}, pool, 2, 6)
		if lvl != 1 || st[0].Tranche != rs(50_000) || st[1].Tranche != rs(50_000) {
			t.Errorf("level=%d states=%v", lvl, st)
		}
	})
	t.Run("a full fund waits for the rest, then the level rises", func(t *testing.T) {
		pool := rs(200_000)
		lvl, st := AllocationCaps([]CapFund{f("A", rs(100_000), 6, true), f("B", rs(40_000), 6, true)}, pool, 2, 6)
		if lvl != 1 || st[0].Room != 0 || st[1].Room != rs(60_000) {
			t.Errorf("level=%d states=%v", lvl, st)
		}
		lvl, st = AllocationCaps([]CapFund{f("A", rs(100_000), 6, true), f("B", rs(100_000), 6, true)}, pool, 2, 6)
		if lvl != 2 || st[0].Room != rs(100_000) {
			t.Errorf("after catching up: level=%d states=%v", lvl, st)
		}
	})
	t.Run("short-handed fund is prorated by headcount/6 (Sec 6)", func(t *testing.T) {
		_, st := AllocationCaps([]CapFund{f("full", 0, 6, true), f("short", 0, 4, true)}, rs(120_000), 2, 6)
		if st[0].Tranche != rs(60_000) || st[1].Tranche != rs(40_000) {
			t.Errorf("tranches = %v / %v, want 60000 / 40000", st[0].Tranche, st[1].Tranche)
		}
	})
	t.Run("a disqualified fund takes no inflow and cannot hold up the level", func(t *testing.T) {
		lvl, st := AllocationCaps([]CapFund{f("A", rs(100_000), 6, true), f("dq", 0, 6, false)}, rs(200_000), 2, 6)
		if lvl != 2 || st[1].Room != 0 {
			t.Errorf("level=%d states=%v", lvl, st)
		}
	})
	t.Run("an unfilled fund forfeits: the level is defined only by funds that filled", func(t *testing.T) {
		// One fund never fills. It stays at level 1 for everyone until the hard close; nothing carries over.
		lvl, _ := AllocationCaps([]CapFund{f("A", rs(500_000), 6, true), f("stalled", 0, 6, true)}, rs(200_000), 2, 6)
		if lvl != 1 {
			t.Errorf("level = %d; a stalled fund holds the level at 1 until the window hard-closes", lvl)
		}
	})
}

func TestMandatoryFivePercentSec9(t *testing.T) {
	var st MandatoryState
	st = NextMandatoryState(st, false)
	if st.Warnings != 1 || st.PrizeIneligible {
		t.Fatalf("first breach: %+v", st)
	}
	st = NextMandatoryState(st, false)
	if !st.PrizeIneligible {
		t.Fatalf("consecutive breach must remove Prize 2/4 eligibility: %+v", st)
	}
	// The dump-a-token-5%-in-the-last-window gap stays closed.
	st = NextMandatoryState(st, true)
	if !st.PrizeIneligible {
		t.Error("ineligibility must be permanent")
	}
	var rec MandatoryState
	rec = NextMandatoryState(rec, false)
	rec = NextMandatoryState(rec, true)
	if rec.BelowAtLastCheckpoint || rec.PrizeIneligible || rec.Warnings != 1 {
		t.Errorf("recovering clears the consecutive run: %+v", rec)
	}
	if !IsMandatoryCompliant(rs(1_000_000), rs(50_000), 5) || IsMandatoryCompliant(rs(1_000_000), rs(49_999), 5) {
		t.Error("5% boundary wrong")
	}
}

func TestManagementFeeSec12(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	t.Run("time-weighted average AUM", func(t *testing.T) {
		samples := []AumSample{{t0, rs(1_000_000)}, {t0.Add(5 * time.Minute), rs(3_000_000)}}
		avg, fee := ManagementFee(samples, t0, t0.Add(10*time.Minute), 1.5, 1)
		if avg != rs(2_000_000) || fee != rs(30_000) {
			t.Errorf("avg=%v fee=%v, want 2,000,000 / 30,000", avg, fee)
		}
	})
	t.Run("AUM in force at period start comes from an earlier sample", func(t *testing.T) {
		avg, _ := ManagementFee([]AumSample{{t0, rs(800_000)}}, t0.Add(time.Minute), t0.Add(2*time.Minute), 1, 1)
		if avg != rs(800_000) {
			t.Errorf("avg = %v", avg)
		}
	})
	t.Run("short-handed fund prorated headcount/6", func(t *testing.T) {
		_, fee := ManagementFee([]AumSample{{t0, rs(1_000_000)}}, t0, t0.Add(time.Second), 1.5, 4.0/6.0)
		if fee != rs(10_000) {
			t.Errorf("fee = %v, want 10,000", fee)
		}
	})
	t.Run("empty and degenerate periods", func(t *testing.T) {
		if a, f := ManagementFee(nil, t0, t0.Add(time.Minute), 1.5, 1); a != 0 || f != 0 {
			t.Error("no samples must give zero")
		}
		if a, _ := ManagementFee([]AumSample{{t0, 1}}, t0, t0, 1.5, 1); a != 0 {
			t.Error("zero-length period must give zero")
		}
	})
}

func TestPerformanceFeeClawbackSec12(t *testing.T) {
	t0 := time.Now()
	const units = 10_000.0
	t.Run("fee starts from the launch NAV", func(t *testing.T) {
		r := ProvisionalPerformanceFees([]NavCheckpoint{{t0, 104, units}}, 5, 100, 1)
		near(t, "fee/unit", r[0].FeePerUnit, 0.2)
	})
	t.Run("Appendix A.4: an unsustained peak is clawed back at Final Settlement", func(t *testing.T) {
		prov := ProvisionalPerformanceFees([]NavCheckpoint{{t0, 100, units}, {t0, 121.89, units}, {t0, 115.8, units}}, 5, 100, 1)
		var provTotal money.Paise
		for _, p := range prov {
			provTotal += p.TotalFee
		}
		perUnit, final := FinalPerformanceFee(100, 115.8, units, 5, 1)
		near(t, "provisional total (rupees)", provTotal.Rupees(), 0.05*21.89*units)
		near(t, "final per unit", perUnit, 0.05*15.8)
		near(t, "final total (rupees)", final.Rupees(), 0.05*15.8*units)
		if final >= provTotal {
			t.Errorf("clawback must reduce the fee: final %v >= provisional %v", final, provTotal)
		}
	})
	t.Run("a mark that is not exceeded pays nothing more", func(t *testing.T) {
		r := ProvisionalPerformanceFees([]NavCheckpoint{{t0, 120, 1}, {t0, 110, 1}, {t0, 115, 1}}, 5, 100, 1)
		if r[1].FeePerUnit != 0 || r[2].FeePerUnit != 0 || r[2].HWMAfter != 120 {
			t.Errorf("%+v", r)
		}
	})
	t.Run("final fee is zero at or below launch NAV however high it peaked", func(t *testing.T) {
		if _, total := FinalPerformanceFee(100, 97, 1000, 5, 1); total != 0 {
			t.Errorf("total = %v", total)
		}
	})
	t.Run("short-handed proration scales the profit basis", func(t *testing.T) {
		_, full := FinalPerformanceFee(100, 110, 1000, 5, 1)
		_, four := FinalPerformanceFee(100, 110, 1000, 5, 4.0/6.0)
		if d := int64(four) - int64(math.Round(float64(full)*4/6)); d < -1 || d > 1 {
			t.Errorf("4/6 proration: got %v, want about %v (within a paisa)", four, float64(full)*4/6)
		}
	})
}

func TestPrizeScoringSec16(t *testing.T) {
	r := rb(t)
	t.Run("Prize 1: quality beats size", func(t *testing.T) {
		got := Prize1Scores([]Prize1Input{
			{"big-weak", -2, 0.3, 0.2, 0.4},
			{"small-strong", 18, 0.05, 0.9, 0.8},
			{"middling", 6, 0.15, 0.5, 0.6},
		}, r.Prizes.Prize1)
		if got[0].ID != "small-strong" || got[1].ID != "middling" || got[2].ID != "big-weak" {
			t.Errorf("order = %v", got)
		}
		near(t, "best", got[0].Score, 100)
		near(t, "worst", got[2].Score, 0)
	})
	t.Run("Prize 2 skips ineligible teams", func(t *testing.T) {
		got := Prize2Ranking([]Prize2Input{{"a", rs(900), true}, {"cheater", rs(2000), false}, {"b", rs(1200), true}})
		if len(got) != 2 || got[0].ID != "b" || got[1].ID != "a" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("Prize 4: a steady diversified portfolio beats the raw-returns leader", func(t *testing.T) {
		got := Prize4Scores([]Prize4Input{
			{"gambler", 40, 0.5, []float64{1000}, true},
			{"guardian", 12, 0.04, []float64{100, 100, 100, 100, 100}, true},
			{"dq", 12, 0.01, []float64{100, 100}, false},
		}, r.Prizes.Prize4)
		if len(got) != 2 || got[0].ID != "guardian" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("weights come from the rulebook and sum to 1", func(t *testing.T) {
		p1 := r.Prizes.Prize1
		near(t, "prize1", p1.Performance+p1.RiskManagement+p1.InvestorProfitability+p1.Retention, 1)
	})
	t.Run("helpers", func(t *testing.T) {
		near(t, "drawdown", MaxDrawdown([]float64{100, 120, 90, 110}), 0.25)
		near(t, "monotone series", MaxDrawdown([]float64{1, 2, 3}), 0)
		near(t, "hhi 50/50", Herfindahl([]float64{50, 50}), 0.5)
		near(t, "hhi single", Herfindahl([]float64{100}), 1)
		for _, v := range MinMaxNormalize([]float64{5, 5, 5}) {
			near(t, "all equal", v, 0.5)
		}
		n := MinMaxNormalize([]float64{0, 5, 10})
		near(t, "0", n[0], 0)
		near(t, "0.5", n[1], 0.5)
		near(t, "1", n[2], 1)
	})
	t.Run("ties are ordered deterministically", func(t *testing.T) {
		got := Prize2Ranking([]Prize2Input{{"b", rs(1), true}, {"a", rs(1), true}})
		if got[0].ID != "a" {
			t.Errorf("equal scores must fall back to id order, got %v", got)
		}
	})
}
