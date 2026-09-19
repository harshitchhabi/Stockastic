import type { CancelOrderResult, Order, SubmitOrderResult } from "@stockastic/matching-engine";
import { exchange } from "./state/symbols";
import { accountStore } from "./state/accounts";
import * as repo from "./db/repository";
import type { PersistedAccount } from "./db/repository";

/**
 * Serializes persistence writes that touch the SAME order id, mirroring the
 * matching engine's own per-symbol queue. This exists because two concurrent
 * HTTP requests carrying the same (accountId, clientOrderId) both resolve
 * through the in-memory Exchange (one `deduped: false`, one `deduped: true`)
 * with the identical order object, and both request handlers then call
 * persistOrderSubmission with it. The `orders` table has TWO unique
 * constraints that both describe that one row's identity (engine_order_id,
 * and (account_id, client_order_id)) — Postgres's ON CONFLICT only
 * suppresses the error for whichever ONE is named as the target, so a true
 * concurrent INSERT of the identical row can still raise a hard duplicate-key
 * error on the OTHER constraint. A k6 run at ~450 concurrent VUs hit this
 * exact race twice (once per constraint, once per attempted fix) before this
 * per-order-id queue closed it properly instead of picking a conflict target
 * and hoping.
 */
const orderWriteQueues = new Map<string, Promise<unknown>>();

function serializedByOrderId<T>(orderId: string, task: () => Promise<T>): Promise<T> {
  const prior = orderWriteQueues.get(orderId) ?? Promise.resolve();
  const result = prior.then(task, task);
  orderWriteQueues.set(
    orderId,
    result.then(
      () => undefined,
      () => undefined
    )
  );
  return result;
}

function persistOrderSerialized(order: Order): Promise<void> {
  return serializedByOrderId(order.id, () => repo.insertOrder(order));
}

/**
 * Write-through persistence for one order submission. Idempotent by
 * construction (every repository write is an upsert), so it's safe to call
 * even on a `deduped: true` result — the rows already exist and this is a
 * no-op in that case.
 *
 * Returns `{ persisted: false }` rather than throwing on a DB failure: the
 * match already happened and is authoritative in the in-memory engine (it
 * cannot be undone — no client-side reversal exists), so the caller still
 * reports the trade to the client but should surface the persistence
 * failure loudly (logged here, and the route adds a response flag) so an
 * operator knows a reconciliation pass may be needed.
 */
export async function persistOrderSubmission(
  result: SubmitOrderResult
): Promise<{ persisted: boolean; error?: string }> {
  try {
    // Writes for DIFFERENT orders/accounts are independent and run
    // concurrently against the pool; only writes that touch the SAME order
    // id are serialized (via persistOrderSerialized), which is the minimum
    // needed to close the race above without giving up the concurrency that
    // fixed the earlier latency problem.
    const writes: Promise<unknown>[] = [
      persistOrderSerialized(result.order),
      persistAccountAndHoldings(result.order.accountId, result.order.symbol),
    ];

    for (const fill of result.fills) {
      writes.push(repo.insertFill(fill));
      const makerOrder = exchange.getOrder(fill.symbol, fill.makerOrderId);
      if (makerOrder) writes.push(persistOrderSerialized(makerOrder));
      writes.push(persistAccountAndHoldings(fill.makerAccountId, fill.symbol));
    }

    await Promise.all(writes);
    return { persisted: true };
  } catch (err) {
    // eslint-disable-next-line no-console
    console.error("[persistence] failed to persist order submission — reconciliation may be needed", err);
    return { persisted: false, error: err instanceof Error ? err.message : "unknown_error" };
  }
}

export async function persistCancellation(
  result: CancelOrderResult
): Promise<{ persisted: boolean; error?: string }> {
  if (!result.order) return { persisted: true };
  try {
    await persistOrderSerialized(result.order);
    return { persisted: true };
  } catch (err) {
    // eslint-disable-next-line no-console
    console.error("[persistence] failed to persist cancellation", err);
    return { persisted: false, error: err instanceof Error ? err.message : "unknown_error" };
  }
}

async function persistAccountAndHoldings(accountId: string, touchedSymbol: string): Promise<void> {
  const account = accountStore.get(accountId);
  if (!account) return;

  const holding = accountStore.holdingsFor(account.id).find((h) => h.symbol === touchedSymbol);

  await Promise.all([
    repo.upsertAccount({
      id: account.id,
      displayName: account.displayName,
      email: account.email,
      passwordHash: account.passwordHash,
      role: account.role,
      isAdmin: account.isAdmin,
      cashBalance: account.cashBalance,
    }),
    holding
      ? repo.upsertHolding({ accountId: account.id, symbol: touchedSymbol, qty: holding.qty, avgPrice: holding.avgPrice })
      : Promise.resolve(),
  ]);
}

export async function persistAccount(account: PersistedAccount): Promise<void> {
  await repo.upsertAccount(account);
}
