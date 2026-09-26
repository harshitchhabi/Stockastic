// Wire types for the organiser API (docs/admin-api.md). Every state-changing call carries a `reason`
// and is written to the audit log by the server. Timestamps are epoch milliseconds.

export type ClockStatus = "not_started" | "running" | "paused" | "ended";
/** null means "follow the schedule". */
export type Override = "open" | "closed" | null;

export interface TimelineBlock {
  id: string;
  label: string;
  startMin: number;
  durationMin: number;
  stage: "phase1" | "transition" | "phase2" | "closing";
  marketOpen: boolean;
  allocationWindow: number | null;
}

export interface Overview {
  clock: {
    status: ClockStatus;
    elapsedMs: number;
    totalMs: number;
    blockIndex: number;
    intoMs: number;
    remainingMs: number;
  };
  timeline: TimelineBlock[];
  control: {
    tradingFrozen: boolean;
    pausedSymbols: string[];
    marketOverride: Override;
    marketOpen: boolean;
    windowOverrides: Override[];
    windowsOpen: boolean[];
  };
}

export interface Systems {
  uptimeSec: number;
  dbOk: boolean;
  connected: number;
  tradesPerMin: number;
  commitP50Ms: number;
  commitP99Ms: number;
  journalErrors: number;
  symbolsTotal: number;
  priceTicks: number;
  tickSeconds: number;
  lastPriceAt: number;
  diskFreeMb: number;
  diskLow: boolean;
  recentErrors: { at: number; message: string }[];
}

export type AccountStatus = "active" | "warned" | "disqualified";

export interface AdminAccount {
  id: string;
  displayName: string;
  email: string;
  role: "investor" | "fund_manager";
  isAdmin: boolean;
  status: AccountStatus;
  warnings: number;
  cashBalance: number;
  portfolioValue: number;
  positions: number;
  locked: boolean;
  online: boolean;
  /** Browser tabs connected right now. */
  sockets: number;
  /** When the team last connected or disconnected, in milliseconds; 0 if not since the server started. */
  lastSeen: number;
  /** Share of an investor's portfolio held in funds, in percent (0 until the funds exist). */
  fundShare: number;
  /** True once the funds exist and the investor holds less than the required minimum share in them. */
  belowMandatory: boolean;
}

export interface NewsRelease {
  id: string;
  kind: "news" | "regime";
  headline: string;
  body?: string;
  createdAt: number;
  /** 0 = not yet delivered. */
  fundManagerAt: number;
  publicAt: number;
}

export interface NewsDesk {
  publicDelaySeconds: number;
  items: NewsRelease[];
}

export interface Ticket {
  id: string;
  accountName: string;
  category: string;
  summary: string;
  raisedAt: number;
  incidentAt: number;
  late: boolean;
  /** When the committee should have decided by. */
  dueBy: number;
  status: "open" | "resolved";
  resolution?: string;
}

export interface AuditEntry {
  id: string;
  at: number;
  actor: string;
  action: string;
  target: string;
  reason: string;
  ok: boolean;
}

export interface RulebookStatus {
  version: string;
  source: string;
  provenance: { path: string; status: "recommended" | "tbf" | "assumption" | "confirmed"; section: string; note?: string }[];
  values: unknown;
}

export interface TeamDetail {
  account: AdminAccount;
  wallet: { cash: number; netWorth: number };
  holdings: { symbol: string; qty: number; avgPrice: number; marketValue: number; unrealizedPnl: number }[];
  trades: { id: string; symbol: string; side: "buy" | "sell"; price: number; qty: number; value: number; timestamp: number }[];
  history: AuditEntry[];
}

// ---- market events (the price simulation) ----

export interface SimItem {
  id: string;
  kind: "news" | "bull" | "bear";
  atMinute: number;
  headline: string;
  fired: boolean;
  /** REAL, FAKE or DENIAL. Organiser only: teams are never told which news is fake. */
  type?: string;
  category?: string;
  impacts: number;
  skipped: boolean;
  edited: boolean;
  phase?: string;
  /** When it should go out on the event clock, in minutes, under the current schedule (null if it never will). */
  clockMinute: number | null;
}

export interface SimStatus {
  tickSeconds: number;
  ticks: number;
  activeShocks: number;
  pendingShocks: number;
  scriptedCompanies: number;
  items: SimItem[];
  newsManual: boolean;
  fromTable: boolean;
  tableSteps: number;
  marketSeconds: number;
  newsClock: "event" | "market";
}

export interface AdminTrade {
  id: string;
  at: number;
  accountId: string;
  team: string;
  symbol: string;
  side: "buy" | "sell";
  qty: number;
  price: number;
  value: number;
  stage: string;
}

// ---- Phase 2: qualification, funds, prizes ----

export interface QualRow {
  rank: number;
  accountId: string;
  team: string;
  value: number;
  peak: number;
  trades: number;
  decidedBy: string;
  qualifies: boolean;
  fund?: string;
}

export interface Qualification {
  ready: boolean;
  done: boolean;
  cutoff: number;
  rows: QualRow[];
  boundaryTie: boolean;
  coinToss: boolean;
}

export interface AdminFund {
  id: string;
  number: number;
  name: string;
  philosophy: string;
  risk: string;
  strategy: string;
  managers: string[];
  nav: number;
  returnPct: number;
  aum: number;
  investors: number;
  disqualified: boolean;
  cash: number;
  maxDrawdown: number;
  retention: number;
  profitability: number;
  ranks: [number, number];
  memberIds: string[];
  /** Account id of the team that places the fund's trades. */
  trader: string;
  checkpoints: { name: string; nav: number; aum: number; avgAum: number; mgmtFee: number; perfFee: number }[];
}

export interface PrizeRow {
  id: string;
  name: string;
  score: number;
  rank: number;
  note?: string;
}

export interface Prizes {
  final: boolean;
  prize1: PrizeRow[];
  prize2: PrizeRow[];
  prize3: PrizeRow[];
  prize4: PrizeRow[];
}

export interface LogEntrant {
  accountId: string;
  team: string;
  logs: { checkpoint: number; text: string; at: number }[];
  checkpoints: number;
  eligible: boolean;
  scores: Record<string, number>;
  total: number | null;
}

export interface StrategyLogs {
  rubric: { criterion: string; weight: number; maxScore: number }[];
  entrants: LogEntrant[];
}

export interface ScheduleBlock {
  id: string;
  label: string;
  minutes: number;
  stage: "phase1" | "transition" | "phase2" | "closing";
  marketOpen: boolean;
  allocationWindow: number | null;
  freezeSnapshot: string;
}

export interface Schedule {
  blocks: ScheduleBlock[];
  template: ScheduleBlock[];
  started: boolean;
}

export interface ScheduleCheck {
  openMinutes: number;
  dataMinutes: number;
  lastNewsMinute: number;
  blocks: { id: string; clockStartMin: number; marketStartMin: number; marketOpen: boolean; minutes: number }[];
  breaks: string[];
  warnings: string[];
}
