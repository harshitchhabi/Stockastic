import type { TieBreakStep } from "@stockastic/config";

export interface Phase1Standing {
  accountId: string;
  /** cash + holdings at freeze price. */
  finalValue: number;
  /** Highest portfolio value seen during Phase 1 (Sec 5 tie-break 1). */
  peakValue: number;
  /** Executed transactions during Phase 1 (Sec 5 tie-break 2: fewer wins). */
  executedTransactions: number;
}

export type RankDecidedBy = "value" | TieBreakStep | "unresolved";

export interface RankedStanding extends Phase1Standing {
  rank: number;
  /** What separated this team from the one ranked immediately above it. */
  decidedBy: RankDecidedBy;
}

const paise = (v: number) => Math.round(v * 100);

/**
 * Sec 4/5: rank descending by final value; ties broken by the configured
 * step order. `coinToss` must return a stable per-team key (an
 * organiser-supervised toss result) — the rulebook makes this a manual step,
 * so it is injected rather than randomised here.
 */
export function rankPhase1(
  standings: Phase1Standing[],
  tieBreak: TieBreakStep[],
  coinToss: (accountId: string) => number = () => 0
): RankedStanding[] {
  const compareStep = (step: TieBreakStep, a: Phase1Standing, b: Phase1Standing): number => {
    switch (step) {
      case "peak_portfolio_value":
        return paise(b.peakValue) - paise(a.peakValue);
      case "fewer_transactions":
        return a.executedTransactions - b.executedTransactions;
      case "coin_toss":
        return coinToss(a.accountId) - coinToss(b.accountId);
    }
  };

  const decide = (a: Phase1Standing, b: Phase1Standing): { order: number; by: RankDecidedBy } => {
    const byValue = paise(b.finalValue) - paise(a.finalValue);
    if (byValue !== 0) return { order: byValue, by: "value" };
    for (const step of tieBreak) {
      const c = compareStep(step, a, b);
      if (c !== 0) return { order: c, by: step };
    }
    return { order: a.accountId < b.accountId ? -1 : 1, by: "unresolved" };
  };

  const sorted = [...standings].sort((a, b) => decide(a, b).order);
  return sorted.map((s, i) => ({
    ...s,
    rank: i + 1,
    decidedBy: i === 0 ? "value" : decide(sorted[i - 1], s).by,
  }));
}
