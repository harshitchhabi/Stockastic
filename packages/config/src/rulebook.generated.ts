/* GENERATED from rulebook.schema.json by scripts/gen-types.mjs — do not edit by hand. */

/**
 * Event rules from the Live Financial Ecosystem Rulebook. Single source of truth: the Go backend loads rulebook.json directly; the web app gets types generated from this schema.
 */
export interface Rulebook {
  version: string;
  source: string;
  roles: ("investor" | "fund_manager")[];
  event: {
    totalMinutes: number;
    participants: {
      min: number;
      max: number;
    };
    /**
     * @minItems 1
     */
    timeline: [EventBlock, ...EventBlock[]];
  };
  market: {
    symbolCount: number;
  };
  teams: {
    investorTeamSize: number;
    fundTeamSize: number;
    fundManagerSeats: number;
  };
  accounts: {
    startingCapital: number;
    currency: string;
  };
  qualification: {
    qualifyingTeams: number;
    fundCount: number;
    pairing: "mirror";
    tieBreak: ("peak_portfolio_value" | "fewer_transactions" | "coin_toss")[];
  };
  fund: {
    launchNav: number;
    minInvestmentAbsolute: number;
    minInvestmentWalletPercent: number;
    maxSingleFundWalletPercent: number;
    mandatoryAllocationPercent: number;
    seedCapital: number;
  };
  fees: {
    managementFeePercent: number;
    performanceFeePercent: number;
    managementFeeBasis: "per_period";
  };
  news: {
    fundManagerLeadSeconds: number;
  };
  rateLimits: {
    tradesPerWindow: number;
    windowSeconds: number;
    scope: "account";
  };
  leaderboard: {
    refreshSeconds: number;
    navSampleSeconds: number;
    investorFields: string[];
    fundFields: string[];
    exposeHoldings: boolean;
  };
  disputes: {
    expeditedPerPhase: number;
    overflowQueue: "standard";
    raiseWithinMinutes: number;
    expeditedTurnaroundMinutes: number;
  };
  prizes: {
    prize1: {
      performance: number;
      riskManagement: number;
      investorProfitability: number;
      retention: number;
    };
    prize4: {
      riskAdjustedReturn: number;
      drawdownControl: number;
      diversification: number;
      diversificationCapHoldings: number;
    };
    prize3: {
      strategyLogCheckpoints: number;
      rubric: {
        criterion: string;
        weight: number | null;
        maxScore: number | null;
      }[];
    };
  };
  technical: {
    minimumDeviceRequirements: string | null;
  };
  /**
   * Which values are not yet final. Keyed by dotted path into this document; any value without an entry is fixed by the rulebook.
   */
  provenance: {
    [k: string]: Provenance;
  };
}
export interface EventBlock {
  id: string;
  /**
   * Organiser-internal label; may name the confidential regime-event block.
   */
  label: string;
  /**
   * Participant-facing label; never reveals the regime-event block.
   */
  publicLabel: string;
  durationMin: number;
  stage: "phase1" | "transition" | "phase2" | "closing";
  marketOpen: boolean;
  allocationWindow: 0 | 1 | 2 | 3 | null;
  /**
   * Capture freeze prices at the start of this block.
   */
  freezeSnapshot?: "phase1" | "final";
  /**
   * Organiser-confidential: a scheduled Bull/Bear Run happens in this block.
   */
  regimeEvent?: boolean;
}
export interface Provenance {
  status: "recommended" | "tbf" | "assumption";
  section: string;
  note?: string;
}
