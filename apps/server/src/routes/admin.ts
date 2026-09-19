import type { FastifyInstance } from "fastify";
import { z } from "zod";
import { requireAdmin } from "../auth/middleware";
import { exchange, SYMBOLS } from "../state/symbols";
import { accountStore } from "../state/accounts";
import { controlState, type WindowName } from "../state/controlState";
import { newsDispatcher } from "../news/dispatcher";
import { orderIndex } from "../state/orderIndex";
import { insertTradeAdjustment } from "../db/repository";
import { persistAccount } from "../persistence";

const windowNames = ["tradingRound1", "fundAllocationWindow", "tradingRound2"] as const;

const newsSchema = z.object({
  headline: z.string().min(1),
  body: z.string().optional(),
});

const adjustmentSchema = z.object({
  fillId: z.string().min(1),
  reason: z.string().min(1),
  adjustment: z.record(z.unknown()),
});

/**
 * Admin/organizer control surface — everything here requires requireAdmin.
 * These are live human controls, not just data: freezing rejects in-flight
 * order submissions inside the matching engine itself the instant it's
 * called (see Exchange.freeze), not merely a UI flag.
 */
export async function adminRoutes(app: FastifyInstance) {
  app.addHook("preHandler", requireAdmin);

  app.get("/api/admin/state", async () => {
    const { tradingFrozen: _persisted, ...rest } = controlState.snapshot();
    return {
      tradingFrozen: exchange.isFrozen(),
      ...rest,
      windowsEffective: Object.fromEntries(
        windowNames.map((name) => [name, controlState.isWindowOpenNow(name)])
      ),
    };
  });

  // Trigger news release: fires into both the fund-manager queue (immediate)
  // and the public queue (after CONFIG.news.fundManagerLeadTimeMs), exactly
  // like a normal publish — this endpoint just restricts who can pull the trigger.
  app.post("/api/admin/news", async (request, reply) => {
    const parsed = newsSchema.safeParse(request.body);
    if (!parsed.success) return reply.status(400).send({ error: parsed.error.flatten() });
    return newsDispatcher.publish(parsed.data.headline, parsed.data.body);
  });

  app.post<{ Params: { name: string } }>("/api/admin/windows/:name/open", async (request, reply) => {
    if (!windowNames.includes(request.params.name as WindowName)) {
      return reply.status(400).send({ error: "unknown_window" });
    }
    await controlState.setWindowOverride(request.params.name as WindowName, "open");
    return { name: request.params.name, override: "open" };
  });

  app.post<{ Params: { name: string } }>("/api/admin/windows/:name/close", async (request, reply) => {
    if (!windowNames.includes(request.params.name as WindowName)) {
      return reply.status(400).send({ error: "unknown_window" });
    }
    await controlState.setWindowOverride(request.params.name as WindowName, "closed");
    return { name: request.params.name, override: "closed" };
  });

  app.post<{ Params: { name: string } }>("/api/admin/windows/:name/reset", async (request, reply) => {
    if (!windowNames.includes(request.params.name as WindowName)) {
      return reply.status(400).send({ error: "unknown_window" });
    }
    await controlState.setWindowOverride(request.params.name as WindowName, undefined);
    return { name: request.params.name, override: null };
  });

  // Force-freeze: rejects every order submission the instant it's called,
  // including ones already queued but not yet processed (see engine.ts).
  app.post("/api/admin/freeze", async () => {
    exchange.freeze();
    await controlState.setTradingFrozen(true);
    return { tradingFrozen: true };
  });

  app.post("/api/admin/unfreeze", async () => {
    exchange.unfreeze();
    await controlState.setTradingFrozen(false);
    return { tradingFrozen: false };
  });

  // Promotion = flip role on the existing account, triggered by an
  // organizer/admin. Never a new account, never a redeploy, never a migration.
  app.post<{ Params: { id: string } }>("/api/admin/accounts/:id/promote", async (request, reply) => {
    const account = accountStore.promote(request.params.id);
    if (!account) return reply.status(404).send({ error: "not_found" });
    await persistAccount(account);
    const { passwordHash: _passwordHash, ...pub } = account;
    return pub;
  });

  app.get("/api/admin/accounts", async () => {
    return accountStore.list().map(({ passwordHash: _passwordHash, ...pub }) => pub);
  });

  // Lookup for dispute resolution: an account's full order history across symbols.
  app.get<{ Params: { id: string } }>("/api/admin/accounts/:id", async (request, reply) => {
    const account = accountStore.get(request.params.id);
    if (!account) return reply.status(404).send({ error: "not_found" });
    const { passwordHash: _passwordHash, ...pub } = account;
    return {
      ...pub,
      holdings: accountStore.holdingsFor(account.id),
      pendingOrders: orderIndex.pendingFor(account.id),
    };
  });

  app.get<{ Params: { symbol: string; orderId: string } }>(
    "/api/admin/orders/:symbol/:orderId",
    async (request, reply) => {
      const order = exchange.getOrder(request.params.symbol, request.params.orderId);
      if (!order) return reply.status(404).send({ error: "not_found" });
      return order;
    }
  );

  // Explicit admin/dispute-resolution correction path against trade_adjustments.
  // Never user-facing; trades themselves stay immutable — this records a
  // correction alongside the original fill rather than mutating it.
  app.post("/api/admin/trade-adjustments", async (request, reply) => {
    const parsed = adjustmentSchema.safeParse(request.body);
    if (!parsed.success) return reply.status(400).send({ error: parsed.error.flatten() });

    await insertTradeAdjustment({
      fillId: parsed.data.fillId,
      adminAccountId: request.account!.sub,
      reason: parsed.data.reason,
      adjustment: parsed.data.adjustment,
    });
    return reply.status(201).send({ ok: true });
  });

  app.get("/api/admin/symbols", async () => SYMBOLS);
}
