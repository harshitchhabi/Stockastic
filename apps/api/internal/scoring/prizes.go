package scoring

import (
	"math"
	"sort"

	"stockastic/api/internal/money"
	"stockastic/api/internal/rulebook"
)

// MinMaxNormalize maps values to [0,1]. If every value is equal the field cannot separate anyone,
// so all get 0.5.
func MinMaxNormalize(values []float64) []float64 {
	out := make([]float64, len(values))
	if len(values) == 0 {
		return out
	}
	lo, hi := values[0], values[0]
	for _, v := range values {
		lo, hi = math.Min(lo, v), math.Max(hi, v)
	}
	for i, v := range values {
		if hi == lo {
			out[i] = 0.5
		} else {
			out[i] = (v - lo) / (hi - lo)
		}
	}
	return out
}

// MaxDrawdown is the largest peak-to-trough fall as a fraction of the peak (0 = never fell).
func MaxDrawdown(series []float64) float64 {
	peak, worst := math.Inf(-1), 0.0
	for _, v := range series {
		if v > peak {
			peak = v
		}
		if peak > 0 {
			worst = math.Max(worst, (peak-v)/peak)
		}
	}
	return worst
}

// Herfindahl is the HHI of holding weights (cash excluded): 1 = a single holding, toward 0 = spread thin.
func Herfindahl(holdingValues []float64) float64 {
	var total float64
	for _, v := range holdingValues {
		if v > 0 {
			total += v
		}
	}
	if total <= 0 {
		return 1
	}
	var h float64
	for _, v := range holdingValues {
		if v > 0 {
			h += (v / total) * (v / total)
		}
	}
	return h
}

type Prize1Input struct {
	FundID string
	// NavReturnPct is gross % NAV return over Phase 2, before fees.
	NavReturnPct float64
	MaxDrawdown  float64
	// InvestorProfitability is 0-1: share of the fund's investors who ended in net profit.
	InvestorProfitability float64
	// Retention is 0-1, already filtered by the Sec 16 minimum-investment floor.
	Retention float64
}

type Scored struct {
	ID    string
	Score float64
	Rank  int
}

func rankScores(ids []string, scores []float64) []Scored {
	out := make([]Scored, len(ids))
	for i := range ids {
		out[i] = Scored{ID: ids[i], Score: scores[i]}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].ID < out[j].ID
	})
	for i := range out {
		out[i].Rank = i + 1
	}
	return out
}

// Prize1Scores implements Sec 16 Prize 1: 100 x (w.perf*Return + w.risk*RiskMgmt + w.profit*InvestorProfit
// + w.retention*Retention), each component min-max normalised across the funds. Risk management is
// the INVERSE of max drawdown. AUM is deliberately not an input: size must not win.
func Prize1Scores(funds []Prize1Input, w rulebook.Prize1) []Scored {
	n := len(funds)
	ret, risk, profit, keep := make([]float64, n), make([]float64, n), make([]float64, n), make([]float64, n)
	ids := make([]string, n)
	for i, f := range funds {
		ids[i], ret[i], risk[i], profit[i], keep[i] = f.FundID, f.NavReturnPct, -f.MaxDrawdown, f.InvestorProfitability, f.Retention
	}
	nr, nk, np, nt := MinMaxNormalize(ret), MinMaxNormalize(risk), MinMaxNormalize(profit), MinMaxNormalize(keep)
	scores := make([]float64, n)
	for i := range funds {
		scores[i] = 100 * (w.Performance*nr[i] + w.RiskManagement*nk[i] + w.InvestorProfitability*np[i] + w.Retention*nt[i])
	}
	return rankScores(ids, scores)
}

type Prize2Input struct {
	AccountID  string
	FinalValue money.Paise
	// Eligible is false for disqualified teams and those that lost eligibility via Sec 9.
	Eligible bool
}

// Prize2Ranking is Sec 16 Prize 2: highest final portfolio value among eligible individual investors.
func Prize2Ranking(entrants []Prize2Input) []Scored {
	var in []Prize2Input
	for _, e := range entrants {
		if e.Eligible {
			in = append(in, e)
		}
	}
	ids, vals := make([]string, len(in)), make([]float64, len(in))
	for i, e := range in {
		ids[i], vals[i] = e.AccountID, float64(e.FinalValue)
	}
	return rankScores(ids, vals)
}

type Prize4Input struct {
	AccountID   string
	ReturnPct   float64
	MaxDrawdown float64
	// HoldingValues are the market values of every non-cash holding, including fund units at NAV.
	HoldingValues []float64
	// AvgEffectiveHoldings, if above zero, is the average spread of the investor's money over the whole event and is
	// used instead of HoldingValues, so diversifying only in the last minute cannot win the prize.
	AvgEffectiveHoldings float64
	Eligible             bool
}

// drawdownFloor avoids dividing by ~0 for a portfolio that never fell.
const drawdownFloor = 0.001

// Prize4Scores implements Sec 16 Prize 4 ("Capital Guardian"): risk-adjusted return (return / max
// drawdown), drawdown control, and diversification, each min-max normalised across eligible entrants.
// Diversification is the effective number of holdings 1/HHI, capped at DiversificationCapHoldings
// ("up to a sensible point").
func Prize4Scores(entrants []Prize4Input, w rulebook.Prize4) []Scored {
	var in []Prize4Input
	for _, e := range entrants {
		if e.Eligible {
			in = append(in, e)
		}
	}
	n := len(in)
	ids, riskAdj, control, div := make([]string, n), make([]float64, n), make([]float64, n), make([]float64, n)
	for i, e := range in {
		ids[i] = e.AccountID
		riskAdj[i] = e.ReturnPct / math.Max(e.MaxDrawdown, drawdownFloor)
		control[i] = -e.MaxDrawdown
		if e.AvgEffectiveHoldings > 0 {
			div[i] = math.Min(e.AvgEffectiveHoldings, w.DiversificationCapHoldings)
			continue
		}
		has := false
		for _, v := range e.HoldingValues {
			if v > 0 {
				has = true
			}
		}
		if has {
			div[i] = math.Min(1/Herfindahl(e.HoldingValues), w.DiversificationCapHoldings)
		}
	}
	nr, nc, nd := MinMaxNormalize(riskAdj), MinMaxNormalize(control), MinMaxNormalize(div)
	scores := make([]float64, n)
	for i := range in {
		scores[i] = 100 * (w.RiskAdjustedReturn*nr[i] + w.DrawdownControl*nc[i] + w.Diversification*nd[i])
	}
	return rankScores(ids, scores)
}
