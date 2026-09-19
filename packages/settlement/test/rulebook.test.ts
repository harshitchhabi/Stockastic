import { describe, expect, it } from "vitest";
import { CONFIG, getPublicTimeline } from "@stockastic/config";
import {
  allocationCapState,
  checkAllocation,
  checkTimeline,
  computeFinalPerformanceFee,
  computeHighWaterMarkFees,
  herfindahl,
  INITIAL_MANDATORY_STATE,
  managementFeeForPeriod,
  mandatoryPool,
  maxDrawdown,
  minInvestment,
  minMaxNormalize,
  mirrorPairing,
  navPerUnit,
  nextMandatoryState,
  prize1Scores,
  prize2Ranking,
  prize4Scores,
  rankPhase1,
  unitsForAmount,
} from "../src";

describe("Sec 17 timeline", () => {
  it("sums to exactly 300 minutes across 17 blocks with the rulebook's stage split", () => {
    const r = checkTimeline(CONFIG.event.timeline, CONFIG.event.totalMinutes);
    expect(r.totalMinutes).toBe(300);
    expect(r.blockCount).toBe(17);
    expect(r.minutesByStage).toEqual({ phase1: 78, transition: 22, phase2: 155, closing: 45 });
  });

  it("rejects a timeline that no longer sums to the cap", () => {
    const bad = CONFIG.event.timeline.map((b, i) => (i === 0 ? { ...b, durationMin: 16 } : b));
    expect(() => checkTimeline(bad, 300)).toThrow(/301/);
  });

  it("never leaks the confidential regime-event block to the participant-facing schedule", () => {
    const publicJson = JSON.stringify(getPublicTimeline());
    expect(publicJson).not.toMatch(/regime/i);
    expect(publicJson).not.toContain("organiser");
    expect(CONFIG.event.timeline.filter((b) => b.regimeEvent)).toHaveLength(1);
  });

  it("opens Window 0..3 exactly once each, and only Phase 1 + Phase 2 trading blocks trade", () => {
    const windows = CONFIG.event.timeline.map((b) => b.allocationWindow).filter((w) => w !== null);
    expect(windows).toEqual([0, 1, 2, 3]);
    expect(CONFIG.event.timeline.filter((b) => b.marketOpen).map((b) => b.id)).toEqual([
      "p1_trading", "p2_t1", "p2_t2", "p2_t3", "p2_t4",
    ]);
  });
});

describe("Sec 6 mirror pairing", () => {
  it("pairs Rank k with Rank 21-k for 20 qualifiers", () => {
    const ranks = Array.from({ length: 20 }, (_, i) => i + 1);
    const pairs = mirrorPairing(ranks);
    expect(pairs).toHaveLength(10);
    expect(pairs[0]).toEqual({ fundNumber: 1, stronger: 1, weaker: 20 });
    expect(pairs[2]).toEqual({ fundNumber: 3, stronger: 3, weaker: 18 });
    expect(pairs[9]).toEqual({ fundNumber: 10, stronger: 10, weaker: 11 });
    for (const p of pairs) expect(p.stronger + p.weaker).toBe(21);
  });

  it("rejects an odd or empty qualifier list", () => {
    expect(() => mirrorPairing([1, 2, 3])).toThrow();
    expect(() => mirrorPairing([])).toThrow();
  });
});

describe("Sec 5 Phase-1 ranking and tie-breaks", () => {
  const s = (accountId: string, finalValue: number, peakValue: number, executedTransactions: number) => ({
    accountId, finalValue, peakValue, executedTransactions,
  });
  const tb = CONFIG.qualification.tieBreak;

  it("ranks by final value descending", () => {
    const r = rankPhase1([s("a", 100, 100, 1), s("b", 300, 300, 1), s("c", 200, 200, 1)], tb);
    expect(r.map((x) => x.accountId)).toEqual(["b", "c", "a"]);
    expect(r.map((x) => x.rank)).toEqual([1, 2, 3]);
  });

  it("breaks a value tie by higher intraday peak first", () => {
    const r = rankPhase1([s("low", 500, 510, 5), s("high", 500, 600, 50)], tb);
    expect(r[0]).toMatchObject({ accountId: "high", decidedBy: "value" });
    expect(r[1]).toMatchObject({ accountId: "low", decidedBy: "peak_portfolio_value" });
  });

  it("then by fewer executed transactions", () => {
    const r = rankPhase1([s("busy", 500, 600, 40), s("lean", 500, 600, 10)], tb);
    expect(r.map((x) => x.accountId)).toEqual(["lean", "busy"]);
    expect(r[1].decidedBy).toBe("fewer_transactions");
  });

  it("then by the supervised coin toss", () => {
    const toss: Record<string, number> = { x: 2, y: 1 };
    const r = rankPhase1([s("x", 500, 600, 10), s("y", 500, 600, 10)], tb, (id) => toss[id]);
    expect(r.map((x) => x.accountId)).toEqual(["y", "x"]);
    expect(r[1].decidedBy).toBe("coin_toss");
  });

  it("treats values equal to the paisa as tied", () => {
    const r = rankPhase1([s("a", 1000.001, 900, 1), s("b", 1000.004, 950, 1)], tb);
    expect(r[0].accountId).toBe("b"); // decided by peak, not by the sub-paisa difference
  });

  it("only uses the tie-break steps it is configured with", () => {
    const r = rankPhase1([s("a", 500, 900, 1), s("b", 500, 100, 1)], ["fewer_transactions"]);
    expect(r[1].decidedBy).toBe("unresolved");
  });
});

describe("Sec 10 unit, entry and cap rules", () => {
  const rules = CONFIG.fund;

  it("launches at NAV 100 and issues units = amount / NAV", () => {
    expect(navPerUnit(0, 0, rules.launchNav)).toBe(100);
    expect(unitsForAmount(50_000, 100)).toBe(500);
    expect(unitsForAmount(50_000, 125)).toBe(400);
  });

  it("NAV tracks fund value / units", () => {
    expect(navPerUnit(1_158_000, 10_000, 100)).toBeCloseTo(115.8);
  });

  it("minimum investment is the lower of ₹5,000 or 5% of wallet", () => {
    expect(minInvestment(1_000_000, rules)).toBe(5_000);
    expect(minInvestment(60_000, rules)).toBe(3_000);
  });

  it("rejects below-minimum, over-60%, and unaffordable allocations", () => {
    const base = { walletValue: 1_000_000, cash: 1_000_000, existingFundValue: 0 };
    expect(checkAllocation({ ...base, amount: 4_999 }, rules)).toEqual({ ok: false, reason: "below_minimum" });
    expect(checkAllocation({ ...base, amount: 600_001 }, rules)).toEqual({ ok: false, reason: "exceeds_single_fund_cap" });
    expect(checkAllocation({ ...base, amount: 100_000, cash: 50_000 }, rules)).toEqual({ ok: false, reason: "insufficient_cash" });
    expect(checkAllocation({ ...base, amount: 600_000 }, rules)).toEqual({ ok: true });
  });

  it("applies the 60% cap to the fund position INCLUDING what is already held", () => {
    const r = checkAllocation({ amount: 200_000, walletValue: 1_000_000, cash: 500_000, existingFundValue: 450_000 }, rules);
    expect(r).toEqual({ ok: false, reason: "exceeds_single_fund_cap" });
  });
});

describe("Sec 9/10 allocation caps", () => {
  const f = (fundId: string, inflowThisWindow: number, headcount = 6, active = true) => ({
    fundId, headcount, inflowThisWindow, active,
  });

  it("splits the mandatory pool equally across funds", () => {
    const pool = mandatoryPool([1_000_000, 1_000_000], 5); // 100,000
    expect(pool).toBe(100_000);
    const s = allocationCapState([f("A", 0), f("B", 0)], pool, 2, 6);
    expect(s.funds.map((x) => x.tranche)).toEqual([50_000, 50_000]);
    expect(s.level).toBe(1);
  });

  it("blocks a fund that hit its cap until every other fund catches up, then raises the level", () => {
    const pool = 200_000; // 100k tranche each with 2 funds
    const ahead = allocationCapState([f("A", 100_000), f("B", 40_000)], pool, 2, 6);
    expect(ahead.level).toBe(1);
    expect(ahead.funds[0].room).toBe(0);
    expect(ahead.funds[1].room).toBe(60_000);

    const caughtUp = allocationCapState([f("A", 100_000), f("B", 100_000)], pool, 2, 6);
    expect(caughtUp.level).toBe(2);
    expect(caughtUp.funds[0].room).toBe(100_000);
  });

  it("prorates a short-handed fund's tranche by headcount / 6 (Sec 6)", () => {
    const s = allocationCapState([f("full", 0, 6), f("short", 0, 4)], 120_000, 2, 6);
    expect(s.funds[0].tranche).toBe(60_000);
    expect(s.funds[1].tranche).toBeCloseTo(40_000);
  });

  it("does not let a disqualified fund hold up the level or receive inflow", () => {
    const s = allocationCapState([f("A", 100_000), f("dq", 0, 6, false)], 200_000, 2, 6);
    expect(s.level).toBe(2);
    expect(s.funds[1].room).toBe(0);
  });
});

describe("Sec 9 mandatory 5% enforcement", () => {
  it("warns once, then removes Prize 2/4 eligibility on a consecutive breach", () => {
    let st = nextMandatoryState(INITIAL_MANDATORY_STATE, false); // W0 below
    expect(st).toMatchObject({ warnings: 1, prizeIneligible: false });
    st = nextMandatoryState(st, false); // W1 still below
    expect(st.prizeIneligible).toBe(true);
  });

  it("recovering at the next checkpoint clears the consecutive run", () => {
    let st = nextMandatoryState(INITIAL_MANDATORY_STATE, false);
    st = nextMandatoryState(st, true);
    expect(st).toMatchObject({ warnings: 1, belowAtLastCheckpoint: false, prizeIneligible: false });
  });

  it("closes the sit-at-0%-then-dump-5%-at-the-end gap", () => {
    let st = INITIAL_MANDATORY_STATE;
    for (const compliant of [false, false, false, false]) st = nextMandatoryState(st, compliant);
    expect(st.prizeIneligible).toBe(true);
    st = nextMandatoryState(st, true); // token 5% in the final window
    expect(st.prizeIneligible).toBe(true); // stays ineligible
  });
});

describe("Sec 12 fees", () => {
  it("charges the management fee on time-weighted average AUM", () => {
    const t0 = 0, t1 = 10 * 60_000;
    const r = managementFeeForPeriod({
      samples: [{ t: 0, aum: 1_000_000 }, { t: 5 * 60_000, aum: 3_000_000 }],
      periodStart: t0,
      periodEnd: t1,
      managementFeePercent: 1.5,
    });
    expect(r.averageAum).toBe(2_000_000);
    expect(r.fee).toBeCloseTo(30_000);
  });

  it("carries the AUM in force at period start from an earlier sample", () => {
    const r = managementFeeForPeriod({
      samples: [{ t: 0, aum: 800_000 }],
      periodStart: 60_000,
      periodEnd: 120_000,
      managementFeePercent: 1,
    });
    expect(r.averageAum).toBe(800_000);
  });

  it("prorates a short-handed fund's fee by headcount / 6", () => {
    const base = { samples: [{ t: 0, aum: 1_000_000 }], periodStart: 0, periodEnd: 1000, managementFeePercent: 1.5 };
    expect(managementFeeForPeriod({ ...base, proration: 4 / 6 }).fee).toBeCloseTo(10_000);
  });

  it("charges performance fees from the launch NAV, not from the first checkpoint", () => {
    const [w0] = computeHighWaterMarkFees(
      [{ timestamp: 0, navPerUnit: 104, totalUnitsOutstanding: 100 }],
      5,
      { initialHighWaterMark: 100 }
    );
    expect(w0.feePerUnit).toBeCloseTo(0.2);
  });

  it("Appendix A.4: a NAV peak that isn't sustained is clawed back at Final Settlement", () => {
    const units = 10_000;
    const provisional = computeHighWaterMarkFees(
      [
        { timestamp: 0, navPerUnit: 100, totalUnitsOutstanding: units },
        { timestamp: 1, navPerUnit: 121.89, totalUnitsOutstanding: units }, // pump at a checkpoint
        { timestamp: 2, navPerUnit: 115.8, totalUnitsOutstanding: units },
      ],
      5,
      { initialHighWaterMark: 100 }
    );
    const provisionalTotal = provisional.reduce((s, p) => s + p.totalFeeCharged, 0);
    const final = computeFinalPerformanceFee({ launchNav: 100, finalNav: 115.8, unitsAtFinal: units, performanceFeePercent: 5 });

    expect(provisionalTotal).toBeCloseTo(0.05 * 21.89 * units);
    expect(final.totalFee).toBeCloseTo(0.05 * 15.8 * units);
    expect(final.totalFee).toBeLessThan(provisionalTotal);
  });

  it("charges nothing at Final Settlement if the fund ends at or below launch NAV, however high it peaked", () => {
    expect(computeFinalPerformanceFee({ launchNav: 100, finalNav: 97, unitsAtFinal: 1000, performanceFeePercent: 5 }).totalFee).toBe(0);
  });
});

describe("Sec 16 Prize 1 — quality beats size", () => {
  it("does not let AUM decide, and ranks by the weighted normalised score", () => {
    const weights = CONFIG.prizes.prize1;
    const funds = [
      { fundId: "big-weak", navReturnPct: -2, maxDrawdown: 0.3, investorProfitability: 0.2, retention: 0.4 },
      { fundId: "small-strong", navReturnPct: 18, maxDrawdown: 0.05, investorProfitability: 0.9, retention: 0.8 },
      { fundId: "middling", navReturnPct: 6, maxDrawdown: 0.15, investorProfitability: 0.5, retention: 0.6 },
    ];
    const r = prize1Scores(funds, weights);
    expect(r.map((x) => x.fundId)).toEqual(["small-strong", "middling", "big-weak"]);
    expect(r[0].score).toBeCloseTo(100);
    expect(r[2].score).toBeCloseTo(0);
  });

  it("weights sum to 1", () => {
    const w = CONFIG.prizes.prize1;
    expect(w.performance + w.riskManagement + w.investorProfitability + w.retention).toBeCloseTo(1);
    const w4 = CONFIG.prizes.prize4;
    expect(w4.riskAdjustedReturn + w4.drawdownControl + w4.diversification).toBeCloseTo(1);
  });
});

describe("Sec 16 Prize 2 / Prize 4", () => {
  it("Prize 2 skips ineligible teams", () => {
    const r = prize2Ranking([
      { accountId: "a", finalValue: 900, eligible: true },
      { accountId: "cheater", finalValue: 2000, eligible: false },
      { accountId: "b", finalValue: 1200, eligible: true },
    ]);
    expect(r.map((x) => x.accountId)).toEqual(["b", "a"]);
  });

  it("Prize 4 lets a smaller, steadier, diversified portfolio beat the raw-returns leader", () => {
    const w = CONFIG.prizes.prize4;
    const r = prize4Scores(
      [
        { accountId: "gambler", returnPct: 40, maxDrawdown: 0.5, holdingValues: [1000], eligible: true },
        { accountId: "guardian", returnPct: 12, maxDrawdown: 0.04, holdingValues: [100, 100, 100, 100, 100], eligible: true },
        { accountId: "dq", returnPct: 12, maxDrawdown: 0.01, holdingValues: [100, 100], eligible: false },
      ],
      w
    );
    expect(r.map((x) => x.accountId)).toEqual(["guardian", "gambler"]);
  });

  it("drawdown and concentration helpers", () => {
    expect(maxDrawdown([100, 120, 90, 110])).toBeCloseTo(0.25);
    expect(maxDrawdown([1, 2, 3])).toBe(0);
    expect(herfindahl([50, 50])).toBeCloseTo(0.5);
    expect(herfindahl([100])).toBe(1);
    expect(minMaxNormalize([5, 5, 5])).toEqual([0.5, 0.5, 0.5]);
    expect(minMaxNormalize([0, 5, 10])).toEqual([0, 0.5, 1]);
  });
});
