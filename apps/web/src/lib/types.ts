export type Role = "investor" | "fund_manager";

/** The participant-safe slice of GET /api/config that the UI reads (the server owns the full shape). */
export interface PublicConfig {
  leaderboard: { refreshSeconds: number; visibleToParticipants?: boolean };
}

// Wire types for the order book and trades. Placeholder until the Go API contract defines them.
export type OrderSide = "buy" | "sell";
export type OrderStatus = "open" | "partially_filled" | "filled" | "cancelled" | "rejected";

export interface Order {
  id: string;
  clientOrderId: string;
  accountId: string;
  symbol: string;
  side: OrderSide;
  price: number;
  qty: number;
  remainingQty: number;
  status: OrderStatus;
  createdAt: number;
  seq: number;
}

export interface Fill {
  id: string;
  symbol: string;
  price: number;
  qty: number;
  takerOrderId: string;
  makerOrderId: string;
  takerAccountId: string;
  makerAccountId: string;
  takerSide: OrderSide;
  timestamp: number;
}

export interface PriceLevel {
  price: number;
  qty: number;
  orderCount: number;
}

export interface BookDepth {
  symbol: string;
  bids: PriceLevel[];
  asks: PriceLevel[];
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

export interface Portfolio {
  accountId: string;
  cashBalance: number;
  holdings: Holding[];
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

export interface Fund {
  id: string;
  managerAccountId: string;
  name: string;
  pitch: string;
  riskProfile: string;
  navPerUnit: number;
  totalUnits: number;
}
