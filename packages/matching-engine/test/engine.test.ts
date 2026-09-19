import { describe, expect, it } from "vitest";
import { Exchange, TradingFrozenError } from "../src/engine";
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

describe("Exchange", () => {
  it("dedupes a resubmission of the same (accountId, clientOrderId)", async () => {
    const exchange = new Exchange();
    const request = req({ clientOrderId: "dup-1", accountId: "acct-1" });

    const first = await exchange.submitOrder(request);
    const second = await exchange.submitOrder(request);

    expect(first.deduped).toBe(false);
    expect(second.deduped).toBe(true);
    expect(second.order.id).toBe(first.order.id);

    // Only one order should actually be resting in the book.
    const depth = exchange.getDepth("ACME");
    expect(depth.bids[0].qty).toBe(10);
  });

  it("does not dedupe the same clientOrderId across different accounts", async () => {
    const exchange = new Exchange();
    const a = await exchange.submitOrder(req({ clientOrderId: "same", accountId: "acct-A" }));
    const b = await exchange.submitOrder(req({ clientOrderId: "same", accountId: "acct-B" }));

    expect(a.order.id).not.toBe(b.order.id);
  });

  it("processes concurrent submissions for a symbol strictly in arrival order", async () => {
    const exchange = new Exchange();
    // Fire 50 buy orders concurrently; sequence numbers assigned inside the
    // per-symbol queue must come out strictly increasing with no duplicates.
    const results = await Promise.all(
      Array.from({ length: 50 }, (_, i) =>
        exchange.submitOrder(req({ clientOrderId: `seq-${i}`, price: 100, qty: 1 }))
      )
    );

    const seqs = results.map((r) => r.order.seq);
    const sorted = [...seqs].sort((a, b) => a - b);
    expect(seqs).toEqual(sorted);
    expect(new Set(seqs).size).toBe(50);
  });

  it("emits fill and bookUpdate events on a match", async () => {
    const exchange = new Exchange();
    const fills: unknown[] = [];
    const bookUpdates: unknown[] = [];
    exchange.on("fill", (f) => fills.push(f));
    exchange.on("bookUpdate", (d) => bookUpdates.push(d));

    await exchange.submitOrder(req({ side: "sell", price: 100, qty: 5, accountId: "maker" }));
    await exchange.submitOrder(req({ side: "buy", price: 100, qty: 5, accountId: "taker" }));

    expect(fills).toHaveLength(1);
    expect(bookUpdates.length).toBeGreaterThanOrEqual(2);
  });

  it("cancels an order through the exchange", async () => {
    const exchange = new Exchange();
    const { order } = await exchange.submitOrder(req({}));

    const result = await exchange.cancelOrder("ACME", order.id);
    expect(result.cancelled).toBe(true);
    expect(result.order?.status).toBe("cancelled");
  });

  it("returns not_found when cancelling an unknown order", async () => {
    const exchange = new Exchange();
    const result = await exchange.cancelOrder("ACME", "does-not-exist");
    expect(result.cancelled).toBe(false);
    expect(result.reason).toBe("not_found");
  });

  it("keeps independent, non-blocking queues per symbol", async () => {
    const exchange = new Exchange();
    const [a, b] = await Promise.all([
      exchange.submitOrder(req({ symbol: "AAA", clientOrderId: "x1" })),
      exchange.submitOrder(req({ symbol: "BBB", clientOrderId: "x2" })),
    ]);
    expect(a.order.symbol).toBe("AAA");
    expect(b.order.symbol).toBe("BBB");
  });

  it("dedupes two concurrent retries racing on the same idempotency key (reconnect-and-retry)", async () => {
    // Simulates a client that submits, the connection drops before the ack
    // arrives, and the client retries with the SAME clientOrderId while the
    // first attempt may still be in flight through the per-symbol queue.
    const exchange = new Exchange();
    const request = req({ clientOrderId: "retry-1", accountId: "acct-retry" });

    const [first, second] = await Promise.all([
      exchange.submitOrder(request),
      exchange.submitOrder(request),
    ]);

    const dedupedCount = [first.deduped, second.deduped].filter(Boolean).length;
    expect(dedupedCount).toBe(1); // exactly one of the two was the "original"
    expect(first.order.id).toBe(second.order.id);

    // Still only one order resting — no duplicate trade was created.
    const depth = exchange.getDepth("ACME");
    expect(depth.bids[0].qty).toBe(10);
  });

  it("lets an order that fully processed before freeze stand, but rejects the next one", async () => {
    const exchange = new Exchange();
    await expect(exchange.submitOrder(req({ clientOrderId: "before-freeze" }))).resolves.toMatchObject({
      deduped: false,
    });

    exchange.freeze();
    await expect(exchange.submitOrder(req({ clientOrderId: "after-freeze" }))).rejects.toBeInstanceOf(
      TradingFrozenError
    );
  });

  it("rejects a submission queued in the same tick as freeze(), before it has run", async () => {
    // The freeze check runs when the queued task actually executes, not when
    // submitOrder() is called — so calling freeze() synchronously right after
    // enqueuing (before any microtask has had a chance to run the task) still
    // catches it. This is what makes the freeze "instant" rather than best-effort.
    const exchange = new Exchange();
    const queuedRightBeforeFreeze = exchange.submitOrder(req({ clientOrderId: "queued-before-freeze" }));
    exchange.freeze();

    await expect(queuedRightBeforeFreeze).rejects.toBeInstanceOf(TradingFrozenError);
  });

  it("accepts submissions again after unfreeze", async () => {
    const exchange = new Exchange();
    exchange.freeze();
    await expect(exchange.submitOrder(req({}))).rejects.toBeInstanceOf(TradingFrozenError);
    exchange.unfreeze();
    await expect(exchange.submitOrder(req({}))).resolves.toMatchObject({ deduped: false });
  });

  it("restores resting orders from persisted state in price-time order", async () => {
    const exchange = new Exchange();
    // Simulate what hydration does at startup: replay persisted orders
    // directly into the book without re-matching them.
    exchange.restoreOrder({
      id: "ord-1",
      clientOrderId: "c1",
      accountId: "acct-1",
      symbol: "ACME",
      side: "sell",
      price: 100,
      qty: 5,
      remainingQty: 5,
      status: "open",
      createdAt: Date.now(),
      seq: 1,
    });
    exchange.restoreOrder({
      id: "ord-2",
      clientOrderId: "c2",
      accountId: "acct-2",
      symbol: "ACME",
      side: "sell",
      price: 100,
      qty: 3,
      remainingQty: 3,
      status: "open",
      createdAt: Date.now(),
      seq: 2,
    });

    const depth = exchange.getDepth("ACME");
    expect(depth.asks[0]).toMatchObject({ price: 100, qty: 8, orderCount: 2 });

    // A fresh taker order should match the earlier-restored maker (ord-1) first.
    const { fills } = await exchange.submitOrder(
      req({ side: "buy", price: 100, qty: 5, clientOrderId: "taker-1" })
    );
    expect(fills[0].makerOrderId).toBe("ord-1");
  });

  it("seeds the idempotency cache so a post-restart retry of a pre-crash order dedupes", async () => {
    const exchange = new Exchange();
    const priorResult = {
      order: {
        id: "ord-9",
        clientOrderId: "pre-crash-1",
        accountId: "acct-1",
        symbol: "ACME",
        side: "buy" as const,
        price: 100,
        qty: 5,
        remainingQty: 5,
        status: "open" as const,
        createdAt: Date.now(),
        seq: 1,
      },
      fills: [],
      deduped: false,
    };
    exchange.restoreOrder(priorResult.order);
    exchange.seedIdempotency("acct-1", "pre-crash-1", priorResult);

    const retry = await exchange.submitOrder(
      req({ accountId: "acct-1", clientOrderId: "pre-crash-1" })
    );
    expect(retry.deduped).toBe(true);
    expect(retry.order.id).toBe("ord-9");
  });
});
