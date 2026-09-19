import type { Role } from "@stockastic/config";
import type { BookDepth, Fill, Order, OrderSide } from "@stockastic/matching-engine";

export type { BookDepth, Fill, Order, OrderSide, Role };

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
}

export interface NewsItem {
  id: string;
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
