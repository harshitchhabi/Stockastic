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
  openOrders: number;
  ordersPerMin: number;
  tradesPerMin: number;
  commitP50Ms: number;
  commitP99Ms: number;
  journalErrors: number;
  symbolsTotal: number;
  halted: { symbol: string; since: number; reason: string }[];
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
  /** Cash held back for working orders. */
  reserved: number;
  positions: number;
  locked: boolean;
  online: boolean;
  /** Browser tabs connected right now. */
  sockets: number;
  /** When the team last connected or disconnected, in milliseconds; 0 if not since the server started. */
  lastSeen: number;
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
  sequence: number;
  queue: "expedited" | "standard";
  raisedAt: number;
  incidentAt: number;
  late: boolean;
  /** 0 for the standard queue (no SLA). */
  dueBy: number;
  platformWide: boolean;
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
  provenance: { path: string; status: "recommended" | "tbf" | "assumption"; section: string; note?: string }[];
  values: unknown;
}

export interface TeamDetail {
  account: AdminAccount;
  wallet: { cash: number; reserved: number; available: number; netWorth: number };
  holdings: { symbol: string; qty: number; avgPrice: number; marketValue: number; unrealizedPnl: number }[];
  orders: { id: string; symbol: string; side: "buy" | "sell"; price: number; remainingQty: number }[];
  fills: { id: string; symbol: string; price: number; qty: number; takerAccountId: string; takerSide: "buy" | "sell"; timestamp: number }[];
  history: AuditEntry[];
}
