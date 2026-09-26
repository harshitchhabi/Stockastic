package scoring

import (
	"math"
	"testing"

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

func TestMinInvestmentSec10(t *testing.T) {
	f := rb(t).Fund
	if got := MinInvestment(rs(1_000_000), f); got != rs(5000) {
		t.Errorf("min for a 10L wallet = %v, want 5000", got)
	}
	if got := MinInvestment(rs(60_000), f); got != rs(3000) {
		t.Errorf("min for a 60k wallet = %v, want 3000 (5%%)", got)
	}
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
			{"gambler", 40, 0.5, []float64{1000}, 0, true},
			{"guardian", 12, 0.04, []float64{100, 100, 100, 100, 100}, 0, true},
			{"dq", 12, 0.01, []float64{100, 100}, 0, false},
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

func TestLastMinuteDiversifyingCannotWinPrize4(t *testing.T) {
	w := rb(t).Prizes.Prize4
	steady := Prize4Input{AccountID: "steady", ReturnPct: 5, MaxDrawdown: 0.05, AvgEffectiveHoldings: 6, Eligible: true}
	// Holds one company all event and spreads over ten in the final minute: the end-of-event view looks perfect,
	// the average over the event does not.
	sneaky := Prize4Input{AccountID: "sneaky", ReturnPct: 5, MaxDrawdown: 0.05, AvgEffectiveHoldings: 1.1, HoldingValues: []float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1}, Eligible: true}
	got := Prize4Scores([]Prize4Input{steady, sneaky}, w)
	if got[0].ID != "steady" {
		t.Fatalf("ranking = %+v, want the steady diversifier first", got)
	}
}
