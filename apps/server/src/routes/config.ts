import type { FastifyInstance } from "fastify";
import { getPublicConfig } from "@stockastic/config";
import { controlState } from "../state/controlState";
import { exchange } from "../state/symbols";

export async function configRoutes(app: FastifyInstance) {
  app.get("/api/config", async () => {
    return {
      ...getPublicConfig(),
      tradingFrozen: exchange.isFrozen(),
      windowsOpen: {
        tradingRound1: controlState.isWindowOpenNow("tradingRound1"),
        fundAllocationWindow: controlState.isWindowOpenNow("fundAllocationWindow"),
        tradingRound2: controlState.isWindowOpenNow("tradingRound2"),
      },
    };
  });
}
