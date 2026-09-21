export type Role = "investor" | "fund_manager";

/** The participant-safe slice of GET /api/config that the UI reads (the server owns the full shape). */
export interface PublicConfig {
  leaderboard: { refreshSeconds: number; visibleToParticipants?: boolean };
}

export type TradeSide = "buy" | "sell";

/** One executed trade: shares bought or sold at the price at that moment. Trades are final. */
export interface Trade {
  id: string;
  clientTradeId: string;
  symbol: string;
  side: TradeSide;
  qty: number;
  price: number;
  value: number;
  timestamp: number;
}

/** The `prices` event: every company whose price changed in one step. */
export interface PricesUpdate {
  at: number;
  prices: { symbol: string; price: number }[];
}

export interface Account {
  id: string;
  displayName: string;
  email: string;
  role: Role;
  isAdmin: boolean;
  cashBalance: number;
}

export interface Holding {
  symbol: string;
  qty: number;
  avgPrice: number;
  marketValue: number;
  unrealizedPnl: number;
}

export interface FundPosition {
  fundId: string;
  name: string;
  units: number;
  nav: number;
  value: number;
  contributed: number;
  pnl: number;
}

export interface Portfolio {
  accountId: string;
  /** Set when this is a fund's portfolio (a fund manager's view). */
  fundId?: string;
  cashBalance: number;
  holdings: Holding[];
  fundPositions: FundPosition[];
  totalValue: number;
}

export interface LeaderboardRow {
  rank: number;
  accountId: string;
  displayName: string;
  portfolioValue: number;
  percentReturn: number;
}

export interface SymbolInfo {
  symbol: string;
  displayName: string;
  lastPrice: number | null;
  /** The price the session opened at; the day change is measured from it. Falls back to the first price seen. */
  openPrice?: number;
  /** Optional: supplied with the company universe. Filter chips are built from whatever sectors exist. */
  sector?: string;
}

export interface NewsItem {
  id: string;
  /** "notice" is an organiser announcement; anything else is market news. */
  kind?: string;
  headline: string;
  body?: string;
  createdAt: number;
}

export interface FundInfo {
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
  /** How much more this fund can take right now under the equal-share rule. */
  room: number;
  myUnits: number;
  myValue: number;
  myContributed: number;
}

export interface FundsView {
  formed: boolean;
  windowOpen: boolean;
  window: number;
  mandatoryPercent: number;
  minAbsolute: number;
  minWalletPercent: number;
  maxWalletPercent: number;
  myValueInFunds: number;
  myWallet: number;
  compliant: boolean;
  funds: FundInfo[];
}

export interface FundCheckpoint {
  name: string;
  nav: number;
  aum: number;
  avgAum: number;
  mgmtFee: number;
  perfFee: number;
}

export interface MyFund {
  fund: FundInfo;
  cash: number;
  holdings: Holding[];
  checkpoints: FundCheckpoint[];
  maxDrawdown: number;
  retention: number;
  /** False for the fund's other team: it can watch the fund but only the trader places trades. */
  canTrade: boolean;
  traderName: string;
}

export interface StrategyLogEntry {
  account: string;
  checkpoint: number;
  text: string;
  at: number;
}
