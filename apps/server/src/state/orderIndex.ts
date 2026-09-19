import type { Order } from "@stockastic/matching-engine";
import { exchange } from "./symbols";

/**
 * The matching engine only tracks orders within a single symbol's book. The
 * order ticket's "pending orders" panel needs a cross-symbol view per
 * account, so we maintain a light secondary index here, kept in sync purely
 * by listening to the engine's own events (never mutated directly).
 */
class OrderIndex {
  private readonly byAccount = new Map<string, Map<string, Order>>();

  private upsert(order: Order) {
    const forAccount = this.byAccount.get(order.accountId) ?? new Map<string, Order>();
    forAccount.set(order.id, order);
    this.byAccount.set(order.accountId, forAccount);
  }

  pendingFor(accountId: string): Order[] {
    const forAccount = this.byAccount.get(accountId);
    if (!forAccount) return [];
    return [...forAccount.values()].filter(
      (o) => o.status === "open" || o.status === "partially_filled"
    );
  }

  init() {
    exchange.on("orderAccepted", (order) => this.upsert(order));
    exchange.on("orderCancelled", (order) => this.upsert(order));
    exchange.on("fill", (fill) => {
      // A fill mutates the maker order's remainingQty/status without emitting
      // its own orderAccepted event, so refresh both sides from the book.
      const taker = exchange.getOrder(fill.symbol, fill.takerOrderId);
      const maker = exchange.getOrder(fill.symbol, fill.makerOrderId);
      if (taker) this.upsert(taker);
      if (maker) this.upsert(maker);
    });
  }
}

export const orderIndex = new OrderIndex();
