package scoring

import (
	"math"
	"sort"
	"time"

	"stockastic/api/internal/money"
)

// AumSample is the fund's AUM at an instant; the leaderboard's 5-minute refresh is the sampling
// interval (Sec 12).
type AumSample struct {
	At  time.Time
	AUM money.Paise
}

// TimeWeightedAverageAUM averages AUM over [start, end]. Each sample holds until the next one, and
// the last holds until end; the AUM in force at start is the latest sample at or before it.
func TimeWeightedAverageAUM(samples []AumSample, start, end time.Time) money.Paise {
	if !end.After(start) {
		return 0
	}
	in := make([]AumSample, 0, len(samples))
	for _, s := range samples {
		if !s.At.After(end) {
			in = append(in, s)
		}
	}
	if len(in) == 0 {
		return 0
	}
	sort.SliceStable(in, func(i, j int) bool { return in[i].At.Before(in[j].At) })

	var current money.Paise
	for _, s := range in {
		if !s.At.After(start) {
			current = s.AUM
		}
	}
	var weighted float64
	cursor := start
	for _, s := range in {
		if !s.At.After(start) {
			continue
		}
		weighted += float64(current) * float64(s.At.Sub(cursor))
		cursor, current = s.At, s.AUM
	}
	weighted += float64(current) * float64(end.Sub(cursor))
	return money.Paise(math.Round(weighted / float64(end.Sub(start))))
}

// ManagementFee is the Sec 12 fee for one settlement period: percent of time-weighted average AUM,
// charged regardless of performance. proration = headcount/standard for short-handed funds (Sec 6);
// pass 1 for a full team. Whether the percent is per period or per event is
// rulebook.fees.managementFeeBasis.
func ManagementFee(samples []AumSample, start, end time.Time, percent, proration float64) (avg, fee money.Paise) {
	avg = TimeWeightedAverageAUM(samples, start, end)
	return avg, money.Paise(math.Round(float64(avg) * percent / 100 * proration))
}

// NavCheckpoint is a fund's NAV and unit count at a window close (Sec 12 checkpoints).
type NavCheckpoint struct {
	At    time.Time
	NAV   float64
	Units float64
}

type PerfFeeCheckpoint struct {
	At         time.Time
	NAV        float64
	HWMBefore  float64
	HWMAfter   float64
	FeePerUnit float64
	TotalFee   money.Paise
}

// ProvisionalPerformanceFees is the Sec 12 checkpoint fee: percent of NAV growth above the running
// high-water mark, starting from initialHWM (the launch NAV). A checkpoint that sets no new high
// pays nothing and leaves the mark untouched. These figures are PROVISIONAL and are superseded at
// Final Settlement by FinalPerformanceFee (clawback). proration scales the profit basis for a
// short-handed fund (Sec 6).
func ProvisionalPerformanceFees(cps []NavCheckpoint, percent, initialHWM, proration float64) []PerfFeeCheckpoint {
	out := make([]PerfFeeCheckpoint, len(cps))
	hwm := initialHWM
	for i, cp := range cps {
		before := hwm
		var perUnit float64
		if cp.NAV > hwm {
			perUnit = (cp.NAV - hwm) * percent / 100
			hwm = cp.NAV
		}
		out[i] = PerfFeeCheckpoint{At: cp.At, NAV: cp.NAV, HWMBefore: before, HWMAfter: hwm, FeePerUnit: perUnit,
			TotalFee: money.FromRupees(perUnit * cp.Units * proration)}
	}
	return out
}

// FinalPerformanceFee is the Sec 12 fee clawback: at Final Settlement the fee is recalculated ONCE
// against the fund's sustained final NAV. A peak that was not held to the end contributes nothing,
// so this can never exceed the peak-based provisional total (Appendix A.4: peak 121.89, final
// 115.80 -> the fee is on 15.80, not 21.89).
func FinalPerformanceFee(launchNav, finalNav, unitsAtFinal, percent, proration float64) (perUnit float64, total money.Paise) {
	gain := math.Max(0, finalNav-launchNav)
	perUnit = gain * percent / 100
	return perUnit, money.FromRupees(perUnit * unitsAtFinal * proration)
}
