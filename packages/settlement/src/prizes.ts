/** Min–max normalise to [0,1]; if every value is equal the field is indistinguishable, so all get 0.5. */
export function minMaxNormalize(values: number[]): number[] {
  if (values.length === 0) return [];
  const min = Math.min(...values);
  const max = Math.max(...values);
  if (max === min) return values.map(() => 0.5);
  return values.map((v) => (v - min) / (max - min));
}

/** Largest peak-to-trough fall as a fraction of the peak (0 = never fell). */
export function maxDrawdown(series: number[]): number {
  let peak = -Infinity;
  let worst = 0;
  for (const v of series) {
    if (v > peak) peak = v;
    if (peak > 0) worst = Math.max(worst, (peak - v) / peak);
  }
  return worst;
}

/** Herfindahl–Hirschman index of holding weights (excludes cash). 1 = one holding, →0 = spread thin. */
export function herfindahl(holdingValues: number[]): number {
  const positive = holdingValues.filter((v) => v > 0);
  const total = positive.reduce((s, v) => s + v, 0);
  if (total <= 0) return 1;
  return positive.reduce((s, v) => s + (v / total) ** 2, 0);
}

export interface Prize1Input {
  fundId: string;
  /** Gross % NAV return over Phase 2, before fees. */
  navReturnPct: number;
  maxDrawdown: number;
  /** 0–1: share of the fund's own investors who ended Phase 2 in net profit. */
  investorProfitability: number;
  /** 0–1: capital retention through the reallocation windows (already floor-filtered, Sec 16). */
  retention: number;
}

export interface Prize1Weights {
  performance: number;
  riskManagement: number;
  investorProfitability: number;
  retention: number;
}

/**
 * Sec 16 Prize 1: Score = 100 × (0.35·Return + 0.25·RiskMgmt + 0.25·InvestorProfit
 * + 0.15·Retention), each component min–max normalised across the funds.
 * Risk management is the INVERSE of max drawdown. AUM is deliberately not an input.
 */
export function prize1Scores(funds: Prize1Input[], w: Prize1Weights) {
  const ret = minMaxNormalize(funds.map((f) => f.navReturnPct));
  const risk = minMaxNormalize(funds.map((f) => -f.maxDrawdown));
  const profit = minMaxNormalize(funds.map((f) => f.investorProfitability));
  const keep = minMaxNormalize(funds.map((f) => f.retention));

  return funds
    .map((f, i) => ({
      fundId: f.fundId,
      score:
        100 *
        (w.performance * ret[i] +
          w.riskManagement * risk[i] +
          w.investorProfitability * profit[i] +
          w.retention * keep[i]),
    }))
    .sort((a, b) => b.score - a.score)
    .map((f, i) => ({ ...f, rank: i + 1 }));
}

export interface Prize2Input {
  accountId: string;
  finalValue: number;
  /** Disqualified, or lost eligibility via the Sec 9 5%-minimum escalation. */
  eligible: boolean;
}

/** Sec 16 Prize 2: highest final portfolio value among eligible individual investors. */
export function prize2Ranking(entrants: Prize2Input[]) {
  return entrants
    .filter((e) => e.eligible)
    .sort((a, b) => b.finalValue - a.finalValue)
    .map((e, i) => ({ accountId: e.accountId, finalValue: e.finalValue, rank: i + 1 }));
}

export interface Prize4Input {
  accountId: string;
  returnPct: number;
  maxDrawdown: number;
  /** Market value of every non-cash holding, incl. fund units at NAV. */
  holdingValues: number[];
  eligible: boolean;
}

export interface Prize4Weights {
  riskAdjustedReturn: number;
  drawdownControl: number;
  diversification: number;
  diversificationCapHoldings: number;
}

/** Avoid dividing by ~0 when a portfolio never fell. */
const DRAWDOWN_FLOOR = 0.001;

/**
 * Sec 16 Prize 4 ("Capital Guardian"): risk-adjusted return (return ÷ max
 * drawdown) 40%, drawdown control 30%, diversification 30% — each min–max
 * normalised across eligible entrants. Diversification is 1/HHI (effective
 * number of holdings) capped at `diversificationCapHoldings` ("up to a
 * sensible point"), so past the cap more holdings stop helping.
 */
export function prize4Scores(entrants: Prize4Input[], w: Prize4Weights) {
  const pool = entrants.filter((e) => e.eligible);
  const riskAdj = minMaxNormalize(pool.map((e) => e.returnPct / Math.max(e.maxDrawdown, DRAWDOWN_FLOOR)));
  const control = minMaxNormalize(pool.map((e) => -e.maxDrawdown));
  const diversification = minMaxNormalize(
    pool.map((e) => {
      const hasHoldings = e.holdingValues.some((v) => v > 0);
      if (!hasHoldings) return 0;
      return Math.min(1 / herfindahl(e.holdingValues), w.diversificationCapHoldings);
    })
  );

  return pool
    .map((e, i) => ({
      accountId: e.accountId,
      score:
        100 *
        (w.riskAdjustedReturn * riskAdj[i] +
          w.drawdownControl * control[i] +
          w.diversification * diversification[i]),
    }))
    .sort((a, b) => b.score - a.score)
    .map((e, i) => ({ ...e, rank: i + 1 }));
}
