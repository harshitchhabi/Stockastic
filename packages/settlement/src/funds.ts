export interface FundRules {
  launchNav: number;
  minInvestmentAbsolute: number;
  minInvestmentWalletPercent: number;
  maxSingleFundWalletPercent: number;
  mandatoryAllocationPercent: number;
}

/** Sec 10: NAV/unit = fund portfolio value ÷ units outstanding; launches at launchNav. */
export function navPerUnit(fundValue: number, unitsOutstanding: number, launchNav: number): number {
  if (unitsOutstanding <= 0) return launchNav;
  return fundValue / unitsOutstanding;
}

/** Sec 10: units = amount ÷ NAV at that moment. */
export function unitsForAmount(amount: number, nav: number): number {
  if (nav <= 0) throw new Error("NAV must be positive");
  return amount / nav;
}

/** Sec 10: minimum single investment = the lower of ₹5,000 or 5% of total wallet. */
export function minInvestment(walletValue: number, rules: FundRules): number {
  return Math.min(rules.minInvestmentAbsolute, (walletValue * rules.minInvestmentWalletPercent) / 100);
}

export type AllocationRejection = "below_minimum" | "exceeds_single_fund_cap" | "insufficient_cash";

export interface AllocationCheckInput {
  amount: number;
  /** Investor's total wallet: cash + direct holdings + fund units at current NAV. */
  walletValue: number;
  cash: number;
  /** Current value of the investor's existing position in THIS fund. */
  existingFundValue: number;
}

/** Sec 10 entry rules: minimum size, 60% single-fund cap, cannot spend cash you don't have. */
export function checkAllocation(
  input: AllocationCheckInput,
  rules: FundRules
): { ok: true } | { ok: false; reason: AllocationRejection } {
  if (input.amount > input.cash) return { ok: false, reason: "insufficient_cash" };
  if (input.amount < minInvestment(input.walletValue, rules)) return { ok: false, reason: "below_minimum" };
  const cap = (input.walletValue * rules.maxSingleFundWalletPercent) / 100;
  if (input.existingFundValue + input.amount > cap + 1e-9) {
    return { ok: false, reason: "exceeds_single_fund_cap" };
  }
  return { ok: true };
}

export interface CapFundInput {
  fundId: string;
  /** Sec 6: actual headcount (short-handed funds are prorated). */
  headcount: number;
  /** Gross inflow into this fund during the CURRENT window, counting only valid (non-void) allocations. */
  inflowThisWindow: number;
  /** Disqualified funds neither receive inflow nor hold up the level. */
  active: boolean;
}

export interface FundCapState {
  fundId: string;
  tranche: number;
  allowance: number;
  room: number;
}

/**
 * Sec 9/10 allocation caps. The mandatory pool is split equally across funds
 * (prorated by headcount/standard for short-handed funds) into a per-fund
 * tranche. A fund that fills its allowance is blocked until EVERY active fund
 * has filled the same level, at which point the level rises by one tranche.
 * The window's hard close ends everything regardless (cap-stall closure) —
 * callers simply stop asking after close; nothing carries over.
 */
export function allocationCapState(
  funds: CapFundInput[],
  mandatoryPool: number,
  fundCount: number,
  standardHeadcount: number
): { level: number; funds: FundCapState[] } {
  const active = funds.filter((f) => f.active);
  const tranche = (f: CapFundInput) =>
    ((mandatoryPool / fundCount) * f.headcount) / standardHeadcount;

  let level = 1;
  if (active.length > 0 && active.every((f) => tranche(f) > 0)) {
    while (active.every((f) => f.inflowThisWindow >= level * tranche(f) - 1e-9)) level++;
  }

  return {
    level,
    funds: funds.map((f) => {
      const t = tranche(f);
      const allowance = t > 0 ? level * t : Number.POSITIVE_INFINITY;
      return {
        fundId: f.fundId,
        tranche: t,
        allowance,
        room: f.active ? Math.max(0, allowance - f.inflowThisWindow) : 0,
      };
    }),
  };
}

/** Sec 9: total mandatory pool = Σ mandatory% × each investor team's portfolio value. */
export function mandatoryPool(investorPortfolioValues: number[], mandatoryPercent: number): number {
  return investorPortfolioValues.reduce((s, v) => s + (v * mandatoryPercent) / 100, 0);
}

export interface MandatoryState {
  warnings: number;
  belowAtLastCheckpoint: boolean;
  /** Lost Prize 2 and Prize 4 eligibility (not disqualified from the event). */
  prizeIneligible: boolean;
}

export const INITIAL_MANDATORY_STATE: MandatoryState = {
  warnings: 0,
  belowAtLastCheckpoint: false,
  prizeIneligible: false,
};

/**
 * Sec 9 enforcement, run at the close of Windows 0–3: below the minimum at
 * one checkpoint = one formal warning; still below at the immediately
 * following checkpoint = ineligible for Prizes 2 and 4 only. (Sec 21 words the
 * escalation more loosely as "a second violation"; Sec 9 is the rule specific
 * to the 5% minimum, so its consecutive-checkpoint wording is used.)
 */
export function nextMandatoryState(prev: MandatoryState, compliantNow: boolean): MandatoryState {
  if (compliantNow) return { ...prev, belowAtLastCheckpoint: false };
  if (prev.belowAtLastCheckpoint) {
    return { warnings: prev.warnings, belowAtLastCheckpoint: true, prizeIneligible: true };
  }
  return { warnings: prev.warnings + 1, belowAtLastCheckpoint: true, prizeIneligible: prev.prizeIneligible };
}

export function isMandatoryCompliant(
  totalWalletValue: number,
  valueInFunds: number,
  mandatoryPercent: number
): boolean {
  if (totalWalletValue <= 0) return true;
  return (valueInFunds / totalWalletValue) * 100 + 1e-9 >= mandatoryPercent;
}
