package scoring

import (
	"math"

	"stockastic/api/internal/money"
	"stockastic/api/internal/rulebook"
)

// MinInvestment is the lower of the absolute floor or a percentage of the investor's wallet (Sec 10).
func MinInvestment(wallet money.Paise, r rulebook.Fund) money.Paise {
	abs := money.FromRupees(r.MinInvestmentAbsolute)
	pct := money.Paise(math.Round(float64(wallet) * r.MinInvestmentWalletPercent / 100))
	if pct < abs {
		return pct
	}
	return abs
}

// CapFund is one fund's state within the current allocation window.
type CapFund struct {
	FundID string
	// Headcount below the standard team size prorates the fund's tranche (Sec 6).
	Headcount int
	// InflowThisWindow counts only valid allocations (self-investment is void and excluded).
	InflowThisWindow money.Paise
	// Active is false for disqualified funds: they take no inflow and never hold up the level.
	Active bool
}

type CapState struct {
	FundID    string
	Tranche   money.Paise
	Allowance money.Paise
	// Room is how much more this fund may take right now.
	Room money.Paise
}

// AllocationCaps implements Sec 9/10. The mandatory pool is split equally across funds (prorated
// by headcount/standard for short-handed ones) into a per-fund tranche. A fund that fills its
// allowance is blocked until EVERY active fund has filled the same level, and then the level rises
// by one tranche. The window's hard close ends it regardless — callers stop asking after close and
// nothing carries over (cap-stall closure), so this function has no notion of carry-over at all.
func AllocationCaps(funds []CapFund, pool money.Paise, fundCount, standardHeadcount int) (int, []CapState) {
	tranche := func(f CapFund) money.Paise {
		return money.Paise(int64(pool) * int64(f.Headcount) / (int64(fundCount) * int64(standardHeadcount)))
	}
	var active []CapFund
	for _, f := range funds {
		if f.Active {
			active = append(active, f)
		}
	}
	// The level is one above the fewest whole tranches any active fund has filled.
	level := 1
	if len(active) > 0 {
		minFilled := int64(math.MaxInt64)
		for _, f := range active {
			t := tranche(f)
			if t <= 0 {
				minFilled = 0
				break
			}
			if filled := int64(f.InflowThisWindow) / int64(t); filled < minFilled {
				minFilled = filled
			}
		}
		level = int(minFilled) + 1
	}
	states := make([]CapState, len(funds))
	for i, f := range funds {
		t := tranche(f)
		st := CapState{FundID: f.FundID, Tranche: t}
		switch {
		case !f.Active:
		case t <= 0:
			st.Allowance, st.Room = math.MaxInt64, math.MaxInt64
		default:
			st.Allowance = money.Paise(int64(level)) * t
			if st.Allowance > f.InflowThisWindow {
				st.Room = st.Allowance - f.InflowThisWindow
			}
		}
		states[i] = st
	}
	return level, states
}

// MandatoryPool is the Sec 9 pool: the mandatory percentage of every investor team's portfolio.
func MandatoryPool(portfolioValues []money.Paise, mandatoryPercent float64) money.Paise {
	var total float64
	for _, v := range portfolioValues {
		total += float64(v) * mandatoryPercent / 100
	}
	return money.Paise(math.Round(total))
}
