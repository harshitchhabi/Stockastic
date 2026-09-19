import type { FastifyInstance } from "fastify";
import { z } from "zod";
import { fundStore } from "../state/funds";
import { accountStore } from "../state/accounts";
import { requireAuth } from "../auth/middleware";
import { controlState } from "../state/controlState";

const createFundSchema = z.object({
  name: z.string().min(1),
  pitch: z.string().default(""),
  riskProfile: z.string().default("TBD"),
});

const navSchema = z.object({ navPerUnit: z.number().positive() });

const investmentSchema = z.object({
  units: z.number().positive(),
});

export async function fundRoutes(app: FastifyInstance) {
  app.get("/api/funds", async () => fundStore.list());

  app.post("/api/funds", { preHandler: requireAuth }, async (request, reply) => {
    const parsed = createFundSchema.safeParse(request.body);
    if (!parsed.success) return reply.status(400).send({ error: parsed.error.flatten() });

    const manager = accountStore.get(request.account!.sub);
    if (!manager || manager.role !== "fund_manager") {
      return reply.status(403).send({ error: "only fund_manager accounts may create funds" });
    }
    return fundStore.create(manager.id, parsed.data.name, parsed.data.pitch, parsed.data.riskProfile);
  });

  app.post<{ Params: { id: string } }>(
    "/api/funds/:id/nav",
    { preHandler: requireAuth },
    async (request, reply) => {
      const fund = fundStore.get(request.params.id);
      if (!fund) return reply.status(404).send({ error: "not_found" });
      if (fund.managerAccountId !== request.account!.sub) {
        return reply.status(403).send({ error: "not_your_fund" });
      }
      const parsed = navSchema.safeParse(request.body);
      if (!parsed.success) return reply.status(400).send({ error: parsed.error.flatten() });
      return fundStore.setNav(request.params.id, parsed.data.navPerUnit);
    }
  );

  app.get<{ Params: { id: string } }>("/api/funds/:id/breakdown", async (request) => {
    return fundStore.breakdownForFund(request.params.id);
  });

  // Allocate/redeem is DISABLED (not hidden) outside an explicitly "open"
  // window. controlState reconciles the config-driven schedule with a live
  // admin override, so an organizer can open/close this window by hand
  // without waiting on (or instead of) the configured timestamps.
  const gateOnWindow = (reply: { status: (n: number) => { send: (b: unknown) => unknown } }) => {
    if (!controlState.isWindowOpenNow("fundAllocationWindow")) {
      reply.status(423).send({ error: "fund_allocation_window_closed" });
      return false;
    }
    return true;
  };

  app.post<{ Params: { id: string } }>(
    "/api/funds/:id/allocate",
    { preHandler: requireAuth },
    async (request, reply) => {
      if (!gateOnWindow(reply)) return;
      const parsed = investmentSchema.safeParse(request.body);
      if (!parsed.success) return reply.status(400).send({ error: parsed.error.flatten() });
      return fundStore.record(request.params.id, request.account!.sub, parsed.data.units, "allocate");
    }
  );

  app.post<{ Params: { id: string } }>(
    "/api/funds/:id/redeem",
    { preHandler: requireAuth },
    async (request, reply) => {
      if (!gateOnWindow(reply)) return;
      const parsed = investmentSchema.safeParse(request.body);
      if (!parsed.success) return reply.status(400).send({ error: parsed.error.flatten() });
      return fundStore.record(request.params.id, request.account!.sub, parsed.data.units, "redeem");
    }
  );
}
