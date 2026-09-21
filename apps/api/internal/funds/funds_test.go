package funds

import (
	"math"
	"testing"

	"stockastic/api/internal/money"
)

func formed(t *testing.T) *Book {
	t.Helper()
	b := NewBook(100)
	if err := b.Apply(Event{Op: OpFormed, Funds: []Formed{{ID: "F1", Number: 1, Account: "fund:F1", Members: [2]string{"a", "b"}, Ranks: [2]int{1, 20}}}}); err != nil {
		t.Fatal(err)
	}
	if err := b.Apply(Event{Op: OpFormed}); err == nil {
		t.Fatal("funds were formed twice")
	}
	return b
}

func TestUnitsFollowAllocationsAndRedemptions(t *testing.T) {
	b := formed(t)
	b.Apply(Event{Op: OpAlloc, FundID: "F1", Investor: "x", Window: 0, Amount: 1_000_000, Units: 100})
	b.Apply(Event{Op: OpAlloc, FundID: "F1", Investor: "y", Window: 0, Amount: 500_000, Units: 50})
	b.Apply(Event{Op: OpRedeem, FundID: "F1", Investor: "x", Window: 1, Amount: 250_000, Units: 25})
	f, _ := b.Fund("F1")
	if f.Units != 125 || b.Holding("x", "F1").Units != 75 {
		t.Fatalf("units = %v, x holds %v", f.Units, b.Holding("x", "F1").Units)
	}
	if b.Inflow(0, "F1") != 1_500_000 || b.Inflow(1, "F1") != 0 {
		t.Fatalf("inflows = %v, %v", b.Inflow(0, "F1"), b.Inflow(1, "F1"))
	}
	if got := b.Retention("F1"); math.Abs(got-(1-25.0/150)) > 1e-9 {
		t.Fatalf("retention = %v", got)
	}
	// Two investors put in Rs 15,000 and hold 140 units in all, so at a NAV of 100 nobody has gained.
	if p := b.Profitability("F1", 100); p != 0 {
		t.Fatalf("profitability at the launch NAV = %v", p)
	}
	// At NAV 200, y (50 units, paid 5,000) is up and x (75 units and 2,500 out, paid 10,000) is up too.
	if p := b.Profitability("F1", 200); p != 1 {
		t.Fatalf("profitability at NAV 200 = %v", p)
	}
}

func TestPerformanceFeeOnlyOnNewProfitAboveTheHighWaterMark(t *testing.T) {
	b := formed(t)
	b.Apply(Event{Op: OpAlloc, FundID: "F1", Investor: "x", Amount: 1_000_000, Units: 1000}) // 10,000 rupees, 1000 units
	step := func(name string, nav float64) FundCheckpoint {
		b.Apply(Event{Op: OpSeries, AUMs: map[string]int64{"F1": 2_000_000}})
		cp := b.PlanCheckpoint(name, 0, map[string]float64{"F1": nav}, map[string]int64{"F1": 2_000_000}, 1.5, 5)
		b.Apply(Event{Op: OpCheckpoint, Checkpoint: &cp})
		return cp.Funds[0]
	}
	c1 := step("w0", 110) // 10 above 100: 5% of 10 = 0.50 a unit x 1000 units = Rs 500
	if c1.PerfFee != int64(money.FromRupees(500)) || c1.HWMAfter != 110 {
		t.Fatalf("first checkpoint: %+v", c1)
	}
	c2 := step("w1", 105) // fell: no fee, mark stays
	if c2.PerfFee != 0 || c2.HWMAfter != 110 {
		t.Fatalf("a fall paid a fee or lowered the mark: %+v", c2)
	}
	c3 := step("w2", 108) // recovered but below the mark: still no fee
	if c3.PerfFee != 0 {
		t.Fatalf("merely recovering paid a fee: %+v", c3)
	}
	c4 := step("w3", 120) // 10 above the mark
	if c4.PerfFee != int64(money.FromRupees(500)) {
		t.Fatalf("new profit above the mark: %+v", c4)
	}
	if c1.MgmtFee != 30_000 {
		t.Fatalf("management fee = %d paise, want 30000 (1.5%% of average AUM Rs 20,000)", c1.MgmtFee)
	}
}

func TestRiskTrackingFindsTheLargestFallFromAPeak(t *testing.T) {
	b := formed(t)
	for _, v := range []int64{100, 120, 90, 110, 130, 104} {
		b.Apply(Event{Op: OpSeries, Values: map[string]int64{"x": v}, NAVs: map[string]float64{"F1": float64(v)}})
	}
	_, peak, dd, ok := b.Risk("x")
	if !ok || peak != 130 || math.Abs(dd-0.25) > 1e-9 {
		t.Fatalf("peak %d, drawdown %v; want 130 and 0.25 (120 down to 90)", peak, dd)
	}
	if got := b.MaxDrawdown("F1"); math.Abs(got-0.25) > 1e-9 {
		t.Fatalf("fund drawdown = %v", got)
	}
}
