import { isDbEnabled } from "./pool";
import * as repo from "./repository";
import { accountStore, type Holding } from "../state/accounts";
import { exchange } from "../state/symbols";
import { controlState } from "../state/controlState";

/**
 * Rebuilds all in-memory state from Postgres at startup: accounts/holdings,
 * every symbol's resting order book (in original price-time order), the
 * idempotency cache (so a client retrying a pre-crash clientOrderId after
 * reconnecting still dedupes instead of double-trading), and the admin
 * control state (freeze flag, window overrides). No-ops entirely if
 * DATABASE_URL isn't set — see pool.ts.
 */
export async function hydrateFromDatabase(): Promise<void> {
  if (!isDbEnabled()) {
    console.log("[hydrate] DATABASE_URL not set — starting with empty in-memory state");
    return;
  }

  const [accounts, holdings, orders, fillsByTaker, state] = await Promise.all([
    repo.loadAccounts(),
    repo.loadHoldings(),
    repo.loadOrdersForHydration(),
    repo.loadAllFillsByTakerOrder(),
    repo.loadControlState(),
  ]);

  const holdingsByAccount = new Map<string, Holding[]>();
  for (const h of holdings) {
    const list = holdingsByAccount.get(h.accountId) ?? [];
    list.push({ symbol: h.symbol, qty: h.qty, avgPrice: h.avgPrice });
    holdingsByAccount.set(h.accountId, list);
  }

  for (const account of accounts) {
    accountStore.hydrate(account, holdingsByAccount.get(account.id) ?? []);
  }

  // Orders come back ordered by (symbol, engine_seq ASC) — restoring in that
  // order preserves price-time priority within each symbol's book.
  for (const order of orders) {
    exchange.restoreOrder(order);
  }

  // Seed idempotency so a reconnect-and-retry of a pre-crash clientOrderId
  // dedupes instead of matching again. fillsByTaker was loaded in a single
  // query for the whole table (see loadAllFillsByTakerOrder) rather than one
  // query per order — this loop is now pure in-memory map lookups.
  for (const order of orders) {
    exchange.seedIdempotency(order.accountId, order.clientOrderId, {
      order,
      fills: fillsByTaker.get(order.id) ?? [],
      deduped: false,
    });
  }

  controlState.hydrate(state);
  if (state.tradingFrozen) exchange.freeze();

  console.log(
    `[hydrate] restored ${accounts.length} accounts, ${orders.length} orders, ` +
      `frozen=${state.tradingFrozen} from Postgres`
  );
}
