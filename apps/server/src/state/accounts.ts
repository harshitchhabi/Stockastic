import { randomUUID } from "node:crypto";
import { CONFIG, type Role } from "@stockastic/config";

export interface Account {
  id: string;
  displayName: string;
  email: string;
  passwordHash: string;
  role: Role;
  isAdmin: boolean;
  cashBalance: number;
}

export interface Holding {
  symbol: string;
  qty: number;
  avgPrice: number;
}

/**
 * In-memory account/ledger store — the hot path the matching engine's fill
 * events update synchronously. Postgres (db/repository.ts) is the durable
 * write-through copy: every mutation here is followed, in the route/listener
 * that triggered it, by an awaited persistence call before the client is
 * told the mutation succeeded. See apps/server/src/persistence.ts.
 */
class AccountStore {
  private readonly accounts = new Map<string, Account>();
  private readonly emailIndex = new Map<string, string>(); // email -> accountId
  private readonly holdingsByAccount = new Map<string, Map<string, Holding>>();

  create(displayName: string, email: string, passwordHash: string, role: Role = "investor"): Account {
    const account: Account = {
      id: randomUUID(),
      displayName,
      email,
      passwordHash,
      role,
      isAdmin: false,
      cashBalance: CONFIG.accounts.startingCapital,
    };
    this.accounts.set(account.id, account);
    this.emailIndex.set(email.toLowerCase(), account.id);
    this.holdingsByAccount.set(account.id, new Map());
    return account;
  }

  /** Rehydrate a previously-persisted account at startup — cash balance and role are restored as-is, never reset. */
  hydrate(account: Account, holdings: Holding[]): void {
    this.accounts.set(account.id, account);
    this.emailIndex.set(account.email.toLowerCase(), account.id);
    this.holdingsByAccount.set(account.id, new Map(holdings.map((h) => [h.symbol, h])));
  }

  get(id: string): Account | undefined {
    return this.accounts.get(id);
  }

  getByEmail(email: string): Account | undefined {
    const id = this.emailIndex.get(email.toLowerCase());
    return id ? this.accounts.get(id) : undefined;
  }

  list(): Account[] {
    return [...this.accounts.values()];
  }

  /** Promotion = flip role on the existing account. Never create/replace the row. */
  promote(id: string): Account | undefined {
    const account = this.accounts.get(id);
    if (!account) return undefined;
    account.role = "fund_manager";
    return account;
  }

  holdingsFor(accountId: string): Holding[] {
    return [...(this.holdingsByAccount.get(accountId)?.values() ?? [])];
  }

  /** Applies a fill's cash/position impact to one side of the trade. */
  applyFill(accountId: string, symbol: string, side: "buy" | "sell", price: number, qty: number) {
    const account = this.accounts.get(accountId);
    if (!account) return;

    const notional = price * qty;
    account.cashBalance += side === "buy" ? -notional : notional;

    const holdings = this.holdingsByAccount.get(accountId) ?? new Map<string, Holding>();
    this.holdingsByAccount.set(accountId, holdings);
    const existing = holdings.get(symbol) ?? { symbol, qty: 0, avgPrice: 0 };

    const signedQty = side === "buy" ? qty : -qty;
    const newQty = existing.qty + signedQty;

    if (side === "buy" && existing.qty >= 0) {
      // Adding to (or opening) a long position: roll the average price.
      const totalCost = existing.avgPrice * existing.qty + notional;
      existing.avgPrice = newQty !== 0 ? totalCost / newQty : 0;
    } else if (side === "sell" && existing.qty <= 0) {
      const totalCost = existing.avgPrice * Math.abs(existing.qty) + notional;
      existing.avgPrice = newQty !== 0 ? totalCost / Math.abs(newQty) : 0;
    }
    // Reducing/closing a position keeps the existing avgPrice (realized P&L is
    // not separately tracked yet — TODO once scoring rubric is finalized).

    existing.qty = newQty;
    holdings.set(symbol, existing);
  }
}

export const accountStore = new AccountStore();
