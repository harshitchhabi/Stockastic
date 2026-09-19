import type { Fill, Order } from "@stockastic/matching-engine";
import type { Role } from "@stockastic/config";
import { isDbEnabled, query } from "./pool";

export interface PersistedAccount {
  id: string;
  displayName: string;
  email: string;
  passwordHash: string;
  role: Role;
  isAdmin: boolean;
  cashBalance: number;
}

export interface PersistedHolding {
  accountId: string;
  symbol: string;
  qty: number;
  avgPrice: number;
}

/**
 * Write-through persistence for the ledger. Every function here is a no-op
 * (resolves immediately) when DATABASE_URL isn't set, via query()'s own
 * fallback — see pool.ts. Callers await these BEFORE acknowledging a mutation
 * to the client, so "the request succeeded" and "it survived a restart" are
 * the same guarantee, not two separate ones that can silently drift apart.
 */

export async function ensureSymbol(symbol: string, displayName: string): Promise<void> {
  await query(
    `INSERT INTO symbols (symbol, display_name) VALUES ($1, $2)
     ON CONFLICT (symbol) DO NOTHING`,
    [symbol, displayName]
  );
}

export async function upsertAccount(account: PersistedAccount): Promise<void> {
  await query(
    `INSERT INTO accounts (id, display_name, email, password_hash, role, is_admin, cash_balance)
     VALUES ($1, $2, $3, $4, $5, $6, $7)
     ON CONFLICT (id) DO UPDATE SET
       role = EXCLUDED.role,
       is_admin = EXCLUDED.is_admin,
       cash_balance = EXCLUDED.cash_balance`,
    [
      account.id,
      account.displayName,
      account.email,
      account.passwordHash,
      account.role,
      account.isAdmin,
      account.cashBalance,
    ]
  );
}

export async function upsertHolding(holding: PersistedHolding): Promise<void> {
  await query(
    `INSERT INTO holdings (account_id, symbol, qty, avg_price)
     VALUES ($1, $2, $3, $4)
     ON CONFLICT (account_id, symbol) DO UPDATE SET qty = EXCLUDED.qty, avg_price = EXCLUDED.avg_price`,
    [holding.accountId, holding.symbol, holding.qty, holding.avgPrice]
  );
}

export async function insertOrder(order: Order): Promise<void> {
  // Conflict target is (account_id, client_order_id) — the SAME idempotency
  // key the in-memory Exchange dedupes on — not engine_order_id. The table
  // has a unique constraint on both, but Postgres's ON CONFLICT only
  // suppresses the error for the exact constraint named here; a concurrent
  // insert of the identical row (e.g. two requests racing on the same
  // order, or a maker order persisted from two overlapping fills) would
  // still hit the OTHER unique constraint as a hard error. A k6 run at
  // ~550 concurrent VUs surfaced exactly that race.
  await query(
    `INSERT INTO orders
       (engine_order_id, client_order_id, account_id, symbol, side, price, qty, remaining_qty, status, engine_seq)
     VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
     ON CONFLICT (account_id, client_order_id) DO UPDATE SET
       engine_order_id = EXCLUDED.engine_order_id,
       remaining_qty = EXCLUDED.remaining_qty,
       status = EXCLUDED.status,
       updated_at = now()`,
    [
      order.id,
      order.clientOrderId,
      order.accountId,
      order.symbol,
      order.side,
      order.price,
      order.qty,
      order.remainingQty,
      order.status,
      order.seq,
    ]
  );
}

export async function insertFill(fill: Fill): Promise<void> {
  await query(
    `INSERT INTO fills
       (engine_fill_id, symbol, price, qty, taker_order_id, maker_order_id, taker_account_id, maker_account_id)
     VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
     ON CONFLICT (engine_fill_id) DO NOTHING`,
    [
      fill.id,
      fill.symbol,
      fill.price,
      fill.qty,
      fill.takerOrderId,
      fill.makerOrderId,
      fill.takerAccountId,
      fill.makerAccountId,
    ]
  );
}

export async function loadAccounts(): Promise<PersistedAccount[]> {
  if (!isDbEnabled()) return [];
  const rows = await query<{
    id: string;
    display_name: string;
    email: string;
    password_hash: string;
    role: Role;
    is_admin: boolean;
    cash_balance: string;
  }>("SELECT id, display_name, email, password_hash, role, is_admin, cash_balance FROM accounts");
  return rows.map((r) => ({
    id: r.id,
    displayName: r.display_name,
    email: r.email,
    passwordHash: r.password_hash,
    role: r.role,
    isAdmin: r.is_admin,
    cashBalance: Number(r.cash_balance),
  }));
}

export async function loadHoldings(): Promise<PersistedHolding[]> {
  if (!isDbEnabled()) return [];
  const rows = await query<{ account_id: string; symbol: string; qty: string; avg_price: string }>(
    "SELECT account_id, symbol, qty, avg_price FROM holdings"
  );
  return rows.map((r) => ({
    accountId: r.account_id,
    symbol: r.symbol,
    qty: Number(r.qty),
    avgPrice: Number(r.avg_price),
  }));
}

interface PersistedOrderRow {
  engine_order_id: string;
  client_order_id: string;
  account_id: string;
  symbol: string;
  side: "buy" | "sell";
  price: string;
  qty: string;
  remaining_qty: string;
  status: Order["status"];
  engine_seq: string;
  created_at: string;
}

/** All persisted orders, ordered so hydration can restore each symbol's book in time-priority order. */
export async function loadOrdersForHydration(): Promise<Order[]> {
  if (!isDbEnabled()) return [];
  const rows = await query<PersistedOrderRow>(
    "SELECT * FROM orders ORDER BY symbol, engine_seq ASC"
  );
  return rows.map(rowToOrder);
}

function rowToOrder(r: PersistedOrderRow): Order {
  return {
    id: r.engine_order_id,
    clientOrderId: r.client_order_id,
    accountId: r.account_id,
    symbol: r.symbol,
    side: r.side,
    price: Number(r.price),
    qty: Number(r.qty),
    remainingQty: Number(r.remaining_qty),
    status: r.status,
    createdAt: new Date(r.created_at).getTime(),
    seq: Number(r.engine_seq),
  };
}

/**
 * Every fill, grouped by taker_order_id, for rebuilding the idempotency
 * cache at hydration. Deliberately ONE query for the whole table rather
 * than one query per order — a hydration path that does N+1 queries scales
 * with total trade volume, not account count, and at ~25k orders that took
 * over a minute at startup (found via load testing). A crash mid-event with
 * real volume needs the server back up in seconds, not minutes.
 */
export async function loadAllFillsByTakerOrder(): Promise<Map<string, Fill[]>> {
  const byTaker = new Map<string, Fill[]>();
  if (!isDbEnabled()) return byTaker;

  const rows = await query<{
    engine_fill_id: string;
    symbol: string;
    price: string;
    qty: string;
    taker_order_id: string;
    maker_order_id: string;
    taker_account_id: string;
    maker_account_id: string;
    created_at: string;
  }>("SELECT * FROM fills ORDER BY taker_order_id, created_at ASC");

  for (const r of rows) {
    const fill: Fill = {
      id: r.engine_fill_id,
      symbol: r.symbol,
      price: Number(r.price),
      qty: Number(r.qty),
      takerOrderId: r.taker_order_id,
      makerOrderId: r.maker_order_id,
      takerAccountId: r.taker_account_id,
      makerAccountId: r.maker_account_id,
      takerSide: "buy", // not persisted separately; only used for idempotency replay display, not re-derivation of ledger effects
      timestamp: new Date(r.created_at).getTime(),
    };
    const list = byTaker.get(r.taker_order_id) ?? [];
    list.push(fill);
    byTaker.set(r.taker_order_id, list);
  }

  return byTaker;
}

export interface ControlStateRow {
  tradingFrozen: boolean;
  windowOverrides: Record<string, "open" | "closed" | undefined>;
}

export async function loadControlState(): Promise<ControlStateRow> {
  if (!isDbEnabled()) return { tradingFrozen: false, windowOverrides: {} };
  const rows = await query<{ trading_frozen: boolean; window_overrides: Record<string, string> }>(
    "SELECT trading_frozen, window_overrides FROM control_state WHERE id = true"
  );
  const row = rows[0];
  if (!row) return { tradingFrozen: false, windowOverrides: {} };
  return {
    tradingFrozen: row.trading_frozen,
    windowOverrides: row.window_overrides as Record<string, "open" | "closed" | undefined>,
  };
}

export async function saveControlState(state: ControlStateRow): Promise<void> {
  await query(
    `INSERT INTO control_state (id, trading_frozen, window_overrides, updated_at)
     VALUES (true, $1, $2, now())
     ON CONFLICT (id) DO UPDATE SET
       trading_frozen = EXCLUDED.trading_frozen,
       window_overrides = EXCLUDED.window_overrides,
       updated_at = now()`,
    [state.tradingFrozen, JSON.stringify(state.windowOverrides)]
  );
}

export async function insertTradeAdjustment(input: {
  fillId: string;
  adminAccountId: string;
  reason: string;
  adjustment: unknown;
}): Promise<void> {
  // fillId here is the engine_fill_id; resolve to the fills.id surrogate key first.
  await query(
    `INSERT INTO trade_adjustments (fill_id, admin_account_id, reason, adjustment_json)
     SELECT id, $2, $3, $4 FROM fills WHERE engine_fill_id = $1`,
    [input.fillId, input.adminAccountId, input.reason, JSON.stringify(input.adjustment)]
  );
}
