import { EventEmitter } from "node:events";
import { randomUUID } from "node:crypto";
import { OrderBook } from "./orderBook";
import type {
  BookDepth,
  CancelOrderResult,
  Fill,
  NewOrderRequest,
  Order,
  SubmitOrderResult,
} from "./types";

interface ExchangeEvents {
  fill: (fill: Fill) => void;
  orderAccepted: (order: Order) => void;
  orderCancelled: (order: Order) => void;
  bookUpdate: (depth: BookDepth) => void;
}

/** Thrown by submitOrder while the exchange is force-frozen. */
export class TradingFrozenError extends Error {
  constructor() {
    super("trading_frozen");
    this.name = "TradingFrozenError";
  }
}

/**
 * Owns one OrderBook per symbol and guarantees strictly sequential processing
 * of orders within a symbol, even though callers may submit concurrently and
 * validation upstream may be async. Each symbol gets its own promise chain
 * ("actor queue"); cross-symbol operations run fully in parallel.
 *
 * Idempotency: (accountId, clientOrderId) is deduped — a resubmission returns
 * the original result instead of matching twice. This matters more here than
 * in a normal CRUD app because a duplicate would be a duplicate trade.
 */
export class Exchange extends EventEmitter {
  private readonly books = new Map<string, OrderBook>();
  private readonly queues = new Map<string, Promise<unknown>>();
  private readonly idempotencyCache = new Map<string, SubmitOrderResult>();

  /**
   * Force-freeze flag for an admin/organizer "halt trading" control. Checked
   * inside each symbol's queued task (not at call time), so a submission that
   * was already enqueued but hasn't run yet is still rejected the instant
   * freeze() is called — JS's single-threaded execution means freeze() can
   * never interleave with a task already mid-run, so nothing already matched
   * gets silently undone either.
   */
  private frozen = false;

  private orderCounter = 0;
  private nextOrderId(): string {
    this.orderCounter += 1;
    return `ord-${this.orderCounter}-${randomUUID().slice(0, 8)}`;
  }

  private bookFor(symbol: string): OrderBook {
    let book = this.books.get(symbol);
    if (!book) {
      book = new OrderBook(symbol);
      this.books.set(symbol, book);
    }
    return book;
  }

  private idempotencyKey(accountId: string, clientOrderId: string): string {
    return `${accountId}::${clientOrderId}`;
  }

  /** Chains `task` onto the given symbol's queue so it runs after any prior op on that symbol. */
  private enqueue<T>(symbol: string, task: () => T | Promise<T>): Promise<T> {
    const prior = this.queues.get(symbol) ?? Promise.resolve();
    const result = prior.then(task, task);
    // Swallow rejection at the chain level so one failure doesn't wedge the queue;
    // the caller's own promise still rejects normally.
    this.queues.set(
      symbol,
      result.then(
        () => undefined,
        () => undefined
      )
    );
    return result;
  }

  async submitOrder(request: NewOrderRequest): Promise<SubmitOrderResult> {
    const key = this.idempotencyKey(request.accountId, request.clientOrderId);
    const cached = this.idempotencyCache.get(key);
    if (cached) {
      return { ...cached, deduped: true };
    }

    return this.enqueue(request.symbol, () => {
      if (this.frozen) throw new TradingFrozenError();

      // Re-check inside the queue: two concurrent submits with the same key
      // could both pass the outer check before either finishes.
      const raceCached = this.idempotencyCache.get(key);
      if (raceCached) return { ...raceCached, deduped: true };

      const book = this.bookFor(request.symbol);
      const orderId = this.nextOrderId();
      const { order, fills } = book.submit(request, orderId, Date.now());

      const result: SubmitOrderResult = { order, fills, deduped: false };
      this.idempotencyCache.set(key, result);

      this.emit("orderAccepted", order);
      for (const fill of fills) this.emit("fill", fill);
      this.emit("bookUpdate", book.getDepth());

      return result;
    });
  }

  async cancelOrder(symbol: string, orderId: string): Promise<CancelOrderResult> {
    return this.enqueue(symbol, () => {
      const book = this.bookFor(symbol);
      const existing = book.getOrder(orderId);
      if (!existing) {
        return { order: null, cancelled: false, reason: "not_found" };
      }
      if (existing.status === "filled" || existing.status === "cancelled") {
        return { order: existing, cancelled: false, reason: `already_${existing.status}` };
      }

      const cancelled = book.cancel(orderId);
      if (cancelled) {
        this.emit("orderCancelled", cancelled);
        this.emit("bookUpdate", book.getDepth());
      }
      return { order: cancelled, cancelled: true };
    });
  }

  getDepth(symbol: string, maxLevels = 10): BookDepth {
    return this.bookFor(symbol).getDepth(maxLevels);
  }

  getOrder(symbol: string, orderId: string): Order | undefined {
    return this.bookFor(symbol).getOrder(orderId);
  }

  freeze(): void {
    this.frozen = true;
  }

  unfreeze(): void {
    this.frozen = false;
  }

  isFrozen(): boolean {
    return this.frozen;
  }

  /**
   * Rebuild a symbol's resting liquidity from persisted state at startup.
   * Callers must invoke this for every open/partially_filled order of a
   * symbol in ascending `order.seq` before the exchange accepts any new
   * submissions, so price-time priority survives the restart intact.
   */
  restoreOrder(order: Order): void {
    this.bookFor(order.symbol).restore(order);
  }

  /**
   * Re-populate the idempotency cache from persisted orders/fills at startup,
   * so a client that retries a pre-crash (accountId, clientOrderId) after
   * reconnecting still gets deduped instead of matching a second time.
   */
  seedIdempotency(accountId: string, clientOrderId: string, result: SubmitOrderResult): void {
    this.idempotencyCache.set(this.idempotencyKey(accountId, clientOrderId), {
      ...result,
      deduped: false,
    });
  }
}

export interface Exchange {
  on<K extends keyof ExchangeEvents>(event: K, listener: ExchangeEvents[K]): this;
  emit<K extends keyof ExchangeEvents>(event: K, ...args: Parameters<ExchangeEvents[K]>): boolean;
}
