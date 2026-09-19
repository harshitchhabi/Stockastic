/**
 * SINGLE SOURCE OF TRUTH for every numeric/timing/threshold value in Stockastic.
 *
 * Values come from "Live Financial Ecosystem — Official Event Rulebook v1.1".
 * The rulebook is NOT final: each field notes its rulebook section and, where
 * the rulebook itself says "Recommended" or "To Be Finalised", carries a
 * [RECOMMENDED] / [TBF] tag. [ASSUMPTION] marks a value the rulebook does not
 * specify at all and we had to pick something so the system can run.
 * Nothing outside this file should hardcode a number that belongs here.
 * The frontend reads the participant-safe subset via GET /api/config.
 */

export type Role = "investor" | "fund_manager";

export type Stage = "phase1" | "transition" | "phase2" | "closing";

export interface EventBlock {
  id: string;
  /** Organiser-internal label (may name the confidential regime-event block). */
  label: string;
  /** Participant-facing label — never reveals the regime-event block (Sec 13/17). */
  publicLabel: string;
  durationMin: number;
  stage: Stage;
  /** Whether direct stock trading is open during this block. */
  marketOpen: boolean;
  /** Which fund allocation window (0-3) is open during this block, if any. */
  allocationWindow: 0 | 1 | 2 | 3 | null;
  /** Capture freeze prices at the START of this block (Sec 4, Sec 24). */
  freezeSnapshot?: "phase1" | "final";
  /** Organiser-confidential: a scheduled Bull/Bear Run happens in this block (Sec 13). */
  regimeEvent?: boolean;
}

export type TieBreakStep = "peak_portfolio_value" | "fewer_transactions" | "coin_toss";

export interface StockasticConfig {
  event: {
    /** Sec 1/17: strict cap; the timeline below must sum to exactly this. */
    totalMinutes: number;
    expectedParticipants: number;
    expectedConcurrentClients: number;
    /** Sec 17 — the 17-block schedule. */
    timeline: EventBlock[];
  };

  teams: {
    /** Sec 3: individual-investor teams are exactly 3. */
    investorTeamSize: number;
    /** Sec 6: merged Fund Management Team standard size. */
    fundTeamSize: number;
  };

  accounts: {
    /** Sec 4: identical for every team. [RECOMMENDED] ₹10,00,000 — set by organisers. */
    startingCapital: number;
    /** Direct-stock exposure cap. Rulebook defines none (only fund/wallet caps). */
    exposureCapPerAccount: number | null;
  };

  qualification: {
    /** Sec 5: top N Phase-1 teams qualify. */
    qualifyingTeams: number;
    /** Sec 6: qualifying teams pair up (mirror) into this many funds. */
    fundCount: number;
    /** Sec 5 tie-break order at the qualification boundary. [TBF] — must be published before Phase 1. */
    tieBreak: TieBreakStep[];
  };

  fund: {
    /** Sec 10: NAV per unit at Phase-2 launch. [RECOMMENDED] */
    launchNav: number;
    /** Sec 10: min single investment = min(this, minInvestmentWalletPercent% of wallet). [RECOMMENDED] */
    minInvestmentAbsolute: number;
    minInvestmentWalletPercent: number;
    /** Sec 10: max % of an investor's total wallet value in one fund at allocation time. [RECOMMENDED] */
    maxSingleFundWalletPercent: number;
    /** Sec 9: each investor team must hold at least this % of its portfolio in funds. */
    mandatoryAllocationPercent: number;
    /**
     * [ASSUMPTION] Fund managers' Phase-1 wallets do not carry into the fund;
     * a fund starts with this much of its own capital (0 = investor money only).
     */
    seedCapital: number;
  };

  fees: {
    /** Sec 12: [RECOMMENDED] 1.5%, range 1–2%, exact figure TBF. */
    managementFeePercent: number;
    /** Sec 12: 5% of new profit above the high-water mark. */
    performanceFeePercent: number;
    /**
     * [ASSUMPTION] Sec 12 doesn't say whether 1.5% is per settlement period or
     * per event/annum. "per_period" applies it once to each period's time-
     * weighted average AUM. Flip this when the rulebook clarifies.
     */
    managementFeeBasis: "per_period";
  };

  news: {
    /** Sec 11: fixed, published, 60s. */
    fundManagerLeadTimeMs: number;
  };

  matchingEngine: {
    tickSize: number;
    minOrderQty: number;
    maxOrderQty: number | null;
  };

  market: {
    /** Sec 14: ~250 simulated companies. */
    symbolCount: number;
  };

  rateLimits: {
    /** Sec 11/14: 2 trades/minute, per platform ACCOUNT (a 6-person fund shares one cap). */
    tradesPerWindow: number;
    windowMs: number;
  };

  leaderboard: {
    /** Sec 15: [RECOMMENDED] refresh every 5 min rather than live. */
    refreshMs: number;
    /** Sec 12: NAV / AUM sampled at the same 5-minute interval. */
    navSampleMs: number;
    /** How often we sample portfolio value for Phase-1 tie-break "intraday peak" (Sec 5). */
    peakSampleMs: number;
  };

  disputes: {
    /** Sec 22: expedited disputes per team per phase. */
    expeditedPerPhase: number;
    /** [RECOMMENDED] raise within this many minutes of the incident. */
    raiseWithinMinutes: number;
    /** [RECOMMENDED] expedited decision turnaround. */
    expeditedTurnaroundMinutes: number;
  };

  prizes: {
    /** Sec 16 Prize 1 weights (sum to 1). */
    prize1: {
      performance: number;
      riskManagement: number;
      investorProfitability: number;
      retention: number;
    };
    /** Sec 16 Prize 4 weights (sum to 1). [RECOMMENDED] — organisers may tune. */
    prize4: {
      riskAdjustedReturn: number;
      drawdownControl: number;
      diversification: number;
      /** [ASSUMPTION] Sec 16 says diversification scores 'up to a sensible point'; effective holdings are capped here. */
      diversificationCapHoldings: number;
    };
    prize3: {
      /** Sec 16: [RECOMMENDED] 2–3 Strategy Log checkpoints. */
      strategyLogCheckpoints: number;
      /** Rubric scale + per-criterion weights are [TBF] — must be fixed before Phase 2. */
      rubric: { criterion: string; weight: number | null; maxScore: number | null }[];
    };
  };
}

export const CONFIG: StockasticConfig = {
  event: {
    totalMinutes: 300,
    expectedParticipants: 750,
    expectedConcurrentClients: 750,
    timeline: [
      { id: "briefing", label: "Registration verification, seating & Opening Briefing", publicLabel: "Registration & Opening Briefing", durationMin: 15, stage: "phase1", marketOpen: false, allocationWindow: null },
      { id: "login", label: "Move to Phase 1 stations / login verification", publicLabel: "Platform & team login verification", durationMin: 5, stage: "phase1", marketOpen: false, allocationWindow: null },
      { id: "p1_trading", label: "PHASE 1 — Live Trading", publicLabel: "Phase 1 — Live Trading", durationMin: 40, stage: "phase1", marketOpen: true, allocationWindow: null },
      { id: "p1_freeze", label: "Trading freeze & Phase 1 portfolio calculation", publicLabel: "Trading freeze & portfolio calculation", durationMin: 10, stage: "phase1", marketOpen: false, allocationWindow: null, freezeSnapshot: "phase1" },
      { id: "p1_announce", label: "Announcement of Top 20 teams & Phase 1 rankings", publicLabel: "Top 20 announcement & Phase 1 rankings", durationMin: 8, stage: "phase1", marketOpen: false, allocationWindow: null },
      { id: "formation", label: "Fund Manager Team Formation + Investor transition briefing", publicLabel: "Fund Team Formation & Investor briefing", durationMin: 12, stage: "transition", marketOpen: false, allocationWindow: null },
      { id: "transition", label: "Transition to Phase 2 — switchover, wallets activated, Window 0 opens", publicLabel: "Transition to Phase 2 — Allocation Window 0", durationMin: 10, stage: "transition", marketOpen: false, allocationWindow: 0 },
      { id: "p2_t1", label: "PHASE 2 — Trading Block 1", publicLabel: "Phase 2 — Trading Block 1", durationMin: 35, stage: "phase2", marketOpen: true, allocationWindow: null },
      { id: "w1", label: "Capital Allocation / Reallocation Window 1", publicLabel: "Allocation Window 1", durationMin: 8, stage: "phase2", marketOpen: false, allocationWindow: 1 },
      { id: "p2_t2", label: "PHASE 2 — Trading Block 2 (organiser-scheduled market-regime event)", publicLabel: "Phase 2 — Trading Block 2", durationMin: 35, stage: "phase2", marketOpen: true, allocationWindow: null, regimeEvent: true },
      { id: "w2", label: "Capital Allocation / Reallocation Window 2", publicLabel: "Allocation Window 2", durationMin: 8, stage: "phase2", marketOpen: false, allocationWindow: 2 },
      { id: "p2_t3", label: "PHASE 2 — Trading Block 3", publicLabel: "Phase 2 — Trading Block 3", durationMin: 35, stage: "phase2", marketOpen: true, allocationWindow: null },
      { id: "w3", label: "Capital Allocation / Reallocation Window 3 (FINAL)", publicLabel: "Allocation Window 3 (Final)", durationMin: 9, stage: "phase2", marketOpen: false, allocationWindow: 3 },
      { id: "p2_t4", label: "PHASE 2 — Trading Block 4 (capital locked)", publicLabel: "Phase 2 — Trading Block 4 (capital locked)", durationMin: 25, stage: "phase2", marketOpen: true, allocationWindow: null },
      { id: "final_close", label: "Final Market Closure & Trading Freeze", publicLabel: "Final market closure & trading freeze", durationMin: 5, stage: "closing", marketOpen: false, allocationWindow: null, freezeSnapshot: "final" },
      { id: "settlement", label: "Final NAV / portfolio settlement calculation (organiser buffer)", publicLabel: "Final settlement calculation", durationMin: 20, stage: "closing", marketOpen: false, allocationWindow: null },
      { id: "results", label: "Results Declaration & Prize Announcement", publicLabel: "Results & Prize Announcement", durationMin: 20, stage: "closing", marketOpen: false, allocationWindow: null },
    ],
  },

  teams: { investorTeamSize: 3, fundTeamSize: 6 },

  accounts: {
    startingCapital: 1_000_000,
    exposureCapPerAccount: null,
  },

  qualification: {
    qualifyingTeams: 20,
    fundCount: 10,
    tieBreak: ["peak_portfolio_value", "fewer_transactions", "coin_toss"],
  },

  fund: {
    launchNav: 100,
    minInvestmentAbsolute: 5_000,
    minInvestmentWalletPercent: 5,
    maxSingleFundWalletPercent: 60,
    mandatoryAllocationPercent: 5,
    seedCapital: 0,
  },

  fees: {
    managementFeePercent: 1.5,
    performanceFeePercent: 5,
    managementFeeBasis: "per_period",
  },

  news: { fundManagerLeadTimeMs: 60_000 },

  matchingEngine: { tickSize: 0.05, minOrderQty: 1, maxOrderQty: null },

  market: { symbolCount: 250 },

  rateLimits: { tradesPerWindow: 2, windowMs: 60_000 },

  leaderboard: { refreshMs: 5 * 60_000, navSampleMs: 5 * 60_000, peakSampleMs: 15_000 },

  disputes: { expeditedPerPhase: 3, raiseWithinMinutes: 10, expeditedTurnaroundMinutes: 15 },

  prizes: {
    prize1: { performance: 0.35, riskManagement: 0.25, investorProfitability: 0.25, retention: 0.15 },
    prize4: { riskAdjustedReturn: 0.4, drawdownControl: 0.3, diversification: 0.3, diversificationCapHoldings: 10 },
    prize3: {
      strategyLogCheckpoints: 3,
      rubric: [
        { criterion: "Successful contrarian positions", weight: null, maxScore: null },
        { criterion: "Effective sector rotation", weight: null, maxScore: null },
        { criterion: "Exceptional timing", weight: null, maxScore: null },
        { criterion: "Innovative portfolio construction", weight: null, maxScore: null },
      ],
    },
  },
};

/** Cumulative start offset (minutes from T+00:00) of every timeline block. */
export function timelineOffsets(timeline: EventBlock[] = CONFIG.event.timeline): number[] {
  const offsets: number[] = [];
  let acc = 0;
  for (const block of timeline) {
    offsets.push(acc);
    acc += block.durationMin;
  }
  return offsets;
}

/** Participant-facing schedule: no confidential regime marker, no internal labels (Sec 13/17). */
export function getPublicTimeline() {
  const offsets = timelineOffsets();
  return CONFIG.event.timeline.map((b, i) => ({
    id: b.id,
    label: b.publicLabel,
    startMin: offsets[i],
    durationMin: b.durationMin,
    stage: b.stage,
    marketOpen: b.marketOpen,
    allocationWindow: b.allocationWindow,
  }));
}

/** Subset of CONFIG safe to expose to participants via GET /api/config. */
export function getPublicConfig() {
  return {
    event: {
      totalMinutes: CONFIG.event.totalMinutes,
      timeline: getPublicTimeline(),
    },
    teams: CONFIG.teams,
    accounts: { startingCapital: CONFIG.accounts.startingCapital },
    qualification: {
      qualifyingTeams: CONFIG.qualification.qualifyingTeams,
      fundCount: CONFIG.qualification.fundCount,
    },
    fund: CONFIG.fund,
    fees: {
      managementFeePercent: CONFIG.fees.managementFeePercent,
      performanceFeePercent: CONFIG.fees.performanceFeePercent,
    },
    news: CONFIG.news,
    matchingEngine: CONFIG.matchingEngine,
    rateLimits: CONFIG.rateLimits,
    leaderboard: CONFIG.leaderboard,
    disputes: CONFIG.disputes,
    prizes: CONFIG.prizes,
  };
}
