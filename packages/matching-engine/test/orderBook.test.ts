import { describe, expect, it } from "vitest";
import { OrderBook } from "../src/orderBook";
import type { NewOrderRequest } from "../src/types";

function req(overrides: Partial<NewOrderRequest>): NewOrderRequest {
  return {
    clientOrderId: `c-${Math.random()}`,
    accountId: "acct-1",
    symbol: "ACME",
    side: "buy",
    price: 100,
    qty: 10,
    ...overrides,
  };
}

describe("OrderBook", () => {
  it("rests an order with no crossing liquidity", () => {
    const book = new OrderBook("ACME");
    const { order, fills } = book.submit(req({ side: "buy", price: 100, qty: 10 }), "o1", 1);

    expect(fills).toHaveLength(0);
    expect(order.status).toBe("open");
    expect(book.bestBid()).toBe(100);
    expect(book.bestAsk()).toBeUndefined();
  });

  it("matches a fully crossing order at the resting price (price improvement for taker)", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "sell", price: 100, qty: 10, accountId: "maker" }), "o1", 1);

    const { order, fills } = book.submit(
      req({ side: "buy", price: 105, qty: 10, accountId: "taker" }),
      "o2",
      2
    );

    expect(fills).toHaveLength(1);
    expect(fills[0].price).toBe(100); // trades at the maker's resting price, not taker's limit
    expect(fills[0].qty).toBe(10);
    expect(order.status).toBe("filled");
    expect(order.remainingQty).toBe(0);
    expect(book.bestAsk()).toBeUndefined();
  });

  it("supports partial fills, resting the remainder", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "sell", price: 100, qty: 5, accountId: "maker" }), "o1", 1);

    const { order, fills } = book.submit(
      req({ side: "buy", price: 100, qty: 10, accountId: "taker" }),
      "o2",
      2
    );

    expect(fills).toHaveLength(1);
    expect(fills[0].qty).toBe(5);
    expect(order.status).toBe("partially_filled");
    expect(order.remainingQty).toBe(5);
    expect(book.bestBid()).toBe(100); // remainder now resting as a bid
  });

  it("enforces price-time priority: earlier order at the best price fills first", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "sell", price: 100, qty: 5, accountId: "maker-early" }), "o1", 1);
    book.submit(req({ side: "sell", price: 100, qty: 5, accountId: "maker-late" }), "o2", 2);

    const { fills } = book.submit(
      req({ side: "buy", price: 100, qty: 5, accountId: "taker" }),
      "o3",
      3
    );

    expect(fills).toHaveLength(1);
    expect(fills[0].makerOrderId).toBe("o1");
  });

  it("enforces price priority over time: better price fills before an earlier worse price", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "sell", price: 101, qty: 5, accountId: "maker-worse-early" }), "o1", 1);
    book.submit(req({ side: "sell", price: 100, qty: 5, accountId: "maker-better-late" }), "o2", 2);

    const { fills } = book.submit(
      req({ side: "buy", price: 101, qty: 5, accountId: "taker" }),
      "o3",
      3
    );

    expect(fills).toHaveLength(1);
    expect(fills[0].makerOrderId).toBe("o2");
    expect(fills[0].price).toBe(100);
  });

  it("walks multiple price levels to fill a large taker order", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "sell", price: 100, qty: 5, accountId: "m1" }), "o1", 1);
    book.submit(req({ side: "sell", price: 101, qty: 5, accountId: "m2" }), "o2", 2);

    const { order, fills } = book.submit(
      req({ side: "buy", price: 101, qty: 10, accountId: "taker" }),
      "o3",
      3
    );

    expect(fills).toHaveLength(2);
    expect(fills[0].price).toBe(100);
    expect(fills[1].price).toBe(101);
    expect(order.status).toBe("filled");
  });

  it("does not match orders that do not cross", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "sell", price: 105, qty: 5, accountId: "maker" }), "o1", 1);

    const { order, fills } = book.submit(
      req({ side: "buy", price: 100, qty: 5, accountId: "taker" }),
      "o2",
      2
    );

    expect(fills).toHaveLength(0);
    expect(order.status).toBe("open");
    expect(book.bestBid()).toBe(100);
    expect(book.bestAsk()).toBe(105);
  });

  it("cancels a resting order and removes it from the book", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "buy", price: 100, qty: 10 }), "o1", 1);

    const cancelled = book.cancel("o1");
    expect(cancelled?.status).toBe("cancelled");
    expect(book.bestBid()).toBeUndefined();
  });

  it("cannot cancel an order that has already fully filled", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "sell", price: 100, qty: 5, accountId: "maker" }), "o1", 1);
    book.submit(req({ side: "buy", price: 100, qty: 5, accountId: "taker" }), "o2", 2);

    const result = book.cancel("o1");
    expect(result?.status).toBe("filled");
    expect(result?.remainingQty).toBe(0);
  });

  it("aggregates depth by price level", () => {
    const book = new OrderBook("ACME");
    book.submit(req({ side: "buy", price: 100, qty: 5, accountId: "a" }), "o1", 1);
    book.submit(req({ side: "buy", price: 100, qty: 3, accountId: "b" }), "o2", 2);
    book.submit(req({ side: "buy", price: 99, qty: 7, accountId: "c" }), "o3", 3);

    const depth = book.getDepth();
    expect(depth.bids[0]).toEqual({ price: 100, qty: 8, orderCount: 2 });
    expect(depth.bids[1]).toEqual({ price: 99, qty: 7, orderCount: 1 });
  });
});
