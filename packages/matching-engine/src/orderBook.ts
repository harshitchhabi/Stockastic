import type { BookDepth, Fill, NewOrderRequest, Order, OrderSide, PriceLevel } from "./types";

let fillCounter = 0;
function nextFillId(symbol: string): string {
  fillCounter += 1;
  return `${symbol}-fill-${fillCounter}`;
}

/**
 * A single-symbol limit order book. Strictly price-time priority, partial fills
 * supported. Not thread-safe by design — callers (Exchange) must serialize all
 * mutating calls for a given symbol, which is trivial in Node's single-threaded
 * event loop as long as no `await` is interposed between calls into this class.
 */
export class OrderBook {
  readonly symbol: string;

  /** price -> FIFO queue of resting orders at that price. */
  private readonly bidLevels = new Map<number, Order[]>();
  private readonly askLevels = new Map<number, Order[]>();

  /** Sorted price arrays kept for fast best-price lookup. Bids descending, asks ascending. */
  private bidPrices: number[] = [];
  private askPrices: number[] = [];

  private readonly ordersById = new Map<string, Order>();

  private seqCounter = 0;

  constructor(symbol: string) {
    this.symbol = symbol;
  }

  private nextSeq(): number {
    this.seqCounter += 1;
    return this.seqCounter;
  }

  private levelsFor(side: OrderSide) {
    return side === "buy" ? this.bidLevels : this.askLevels;
  }

  private oppositeLevelsFor(side: OrderSide) {
    return side === "buy" ? this.askLevels : this.bidLevels;
  }

  private pricesFor(side: OrderSide) {
    return side === "buy" ? this.bidPrices : this.askPrices;
  }

  private setPricesFor(side: OrderSide, prices: number[]) {
    if (side === "buy") this.bidPrices = prices;
    else this.askPrices = prices;
  }

  private insertPrice(side: OrderSide, price: number) {
    const prices = this.pricesFor(side);
    if (prices.includes(price)) return;
    prices.push(price);
    if (side === "buy") {
      prices.sort((a, b) => b - a); // highest bid first
    } else {
      prices.sort((a, b) => a - b); // lowest ask first
    }
  }

  private removePriceIfEmpty(side: OrderSide, price: number) {
    const levels = this.levelsFor(side);
    const queue = levels.get(price);
    if (queue && queue.length === 0) {
      levels.delete(price);
      this.setPricesFor(
        side,
        this.pricesFor(side).filter((p) => p !== price)
      );
    }
  }

  private crosses(takerSide: OrderSide, takerPrice: number, restingPrice: number): boolean {
    return takerSide === "buy" ? takerPrice >= restingPrice : takerPrice <= restingPrice;
  }

  /**
   * Accept a new order, matching it immediately against resting opposite-side
   * liquidity at price-time priority, then resting any remainder in the book.
   */
  submit(request: NewOrderRequest, orderId: string, createdAt: number): { order: Order; fills: Fill[] } {
    const order: Order = {
      id: orderId,
      clientOrderId: request.clientOrderId,
      accountId: request.accountId,
      symbol: request.symbol,
      side: request.side,
      price: request.price,
      qty: request.qty,
      remainingQty: request.qty,
      status: "open",
      createdAt,
      seq: this.nextSeq(),
    };

    const fills = this.match(order, createdAt);

    if (order.remainingQty > 0) {
      order.status = fills.length > 0 ? "partially_filled" : "open";
      this.rest(order);
    } else {
      order.status = "filled";
    }

    this.ordersById.set(order.id, order);
    return { order, fills };
  }

  private match(taker: Order, timestamp: number): Fill[] {
    const fills: Fill[] = [];
    const oppositeLevels = this.oppositeLevelsFor(taker.side);
    const oppositePrices = this.pricesFor(taker.side === "buy" ? "sell" : "buy");

    while (taker.remainingQty > 0 && oppositePrices.length > 0) {
      const bestPrice = oppositePrices[0];
      if (!this.crosses(taker.side, taker.price, bestPrice)) break;

      const queue = oppositeLevels.get(bestPrice);
      if (!queue || queue.length === 0) {
        oppositePrices.shift();
        continue;
      }

      const maker = queue[0];
      const fillQty = Math.min(taker.remainingQty, maker.remainingQty);

      taker.remainingQty -= fillQty;
      maker.remainingQty -= fillQty;

      fills.push({
        id: nextFillId(this.symbol),
        symbol: this.symbol,
        price: maker.price,
        qty: fillQty,
        takerOrderId: taker.id,
        makerOrderId: maker.id,
        takerAccountId: taker.accountId,
        makerAccountId: maker.accountId,
        takerSide: taker.side,
        timestamp,
      });

      if (maker.remainingQty === 0) {
        maker.status = "filled";
        queue.shift();
        this.ordersById.set(maker.id, maker);
        if (queue.length === 0) {
          oppositePrices.shift();
          oppositeLevels.delete(bestPrice);
        }
      } else {
        maker.status = "partially_filled";
        this.ordersById.set(maker.id, maker);
      }
    }

    return fills;
  }

  private rest(order: Order) {
    const levels = this.levelsFor(order.side);
    this.insertPrice(order.side, order.price);
    const queue = levels.get(order.price) ?? [];
    queue.push(order);
    levels.set(order.price, queue);
  }

  /**
   * Re-insert a previously-accepted order straight into the book, bypassing
   * matching entirely. Used only at startup to rebuild resting liquidity from
   * persisted state after a restart — the order was already matched (or not)
   * against its original counterparties before the process died, so running
   * it through `match()` again would be wrong. Callers must restore orders
   * for a symbol in ascending `seq` order to preserve time priority.
   */
  restore(order: Order): void {
    if (order.status !== "open" && order.status !== "partially_filled") {
      // Filled/cancelled orders are history, not resting liquidity — still
      // recorded so getOrder()/cancel() idempotency lookups work post-restart.
      this.ordersById.set(order.id, order);
      this.seqCounter = Math.max(this.seqCounter, order.seq);
      return;
    }
    this.rest(order);
    this.ordersById.set(order.id, order);
    this.seqCounter = Math.max(this.seqCounter, order.seq);
  }

  cancel(orderId: string): Order | null {
    const order = this.ordersById.get(orderId);
    if (!order) return null;
    if (order.status === "filled" || order.status === "cancelled") return order;

    const levels = this.levelsFor(order.side);
    const queue = levels.get(order.price);
    if (queue) {
      const idx = queue.findIndex((o) => o.id === orderId);
      if (idx >= 0) queue.splice(idx, 1);
      this.removePriceIfEmpty(order.side, order.price);
    }

    order.status = "cancelled";
    order.remainingQty = 0;
    this.ordersById.set(order.id, order);
    return order;
  }

  getOrder(orderId: string): Order | undefined {
    return this.ordersById.get(orderId);
  }

  bestBid(): number | undefined {
    return this.bidPrices[0];
  }

  bestAsk(): number | undefined {
    return this.askPrices[0];
  }

  getDepth(maxLevels = 10): BookDepth {
    const buildSide = (prices: number[], levels: Map<number, Order[]>): PriceLevel[] =>
      prices.slice(0, maxLevels).map((price) => {
        const queue = levels.get(price) ?? [];
        return {
          price,
          qty: queue.reduce((sum, o) => sum + o.remainingQty, 0),
          orderCount: queue.length,
        };
      });

    return {
      symbol: this.symbol,
      bids: buildSide(this.bidPrices, this.bidLevels),
      asks: buildSide(this.askPrices, this.askLevels),
    };
  }
}
