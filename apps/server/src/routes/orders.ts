import type { FastifyInstance } from "fastify";
import { z } from "zod";
import { TradingFrozenError } from "@stockastic/matching-engine";
import { CONFIG } from "@stockastic/config";
import { exchange, SYMBOLS } from "../state/symbols";
import { orderIndex } from "../state/orderIndex";
import { orderRateLimiter } from "../state/rateLimiter";
import { requireAuth } from "../auth/middleware";
import { persistCancellation, persistOrderSubmission } from "../persistence";

const symbolCodes = SYMBOLS.map((s) => s.symbol) as [string, ...string[]];

const newOrderSchema = z.object({
  clientOrderId: z.string().min(1),
  symbol: z.enum(symbolCodes),
  side: z.enum(["buy", "sell"]),
  price: z.number().positive(),
  qty: z.number().int().min(CONFIG.matchingEngine.minOrderQty),
});

export async function orderRoutes(app: FastifyInstance) {
  // Idempotent by design: same (accountId, clientOrderId) resubmitted returns
  // the original result (`deduped: true`) instead of matching twice. accountId
  // comes from the verified session, never the request body — a client can
  // only ever submit orders as itself.
  app.post("/api/orders", { preHandler: requireAuth }, async (request, reply) => {
    const accountId = request.account!.sub;

    if (!orderRateLimiter.tryConsume(accountId)) {
      return reply.status(429).send({ error: "rate_limited" });
    }

    const parsed = newOrderSchema.safeParse(request.body);
    if (!parsed.success) {
      return reply.status(400).send({ error: parsed.error.flatten() });
    }

    try {
      const result = await exchange.submitOrder({ ...parsed.data, accountId });
      const { persisted, error } = await persistOrderSubmission(result);
      return reply.send({ ...result, persisted, persistenceError: error });
    } catch (err) {
      if (err instanceof TradingFrozenError) {
        return reply.status(423).send({ error: "trading_frozen" });
      }
      throw err;
    }
  });

  app.delete<{ Params: { symbol: string; orderId: string } }>(
    "/api/orders/:symbol/:orderId",
    { preHandler: requireAuth },
    async (request, reply) => {
      const existing = exchange.getOrder(request.params.symbol, request.params.orderId);
      if (existing && existing.accountId !== request.account!.sub) {
        return reply.status(403).send({ error: "not_your_order" });
      }

      const result = await exchange.cancelOrder(request.params.symbol, request.params.orderId);
      if (!result.cancelled && result.reason === "not_found") {
        return reply.status(404).send(result);
      }
      const { persisted } = await persistCancellation(result);
      return reply.send({ ...result, persisted });
    }
  );

  app.get("/api/orders/pending", { preHandler: requireAuth }, async (request) => {
    return orderIndex.pendingFor(request.account!.sub);
  });
}
