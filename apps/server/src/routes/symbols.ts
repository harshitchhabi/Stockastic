import type { FastifyInstance } from "fastify";
import { SYMBOLS, exchange } from "../state/symbols";
import { marketData } from "../state/market";

export async function symbolRoutes(app: FastifyInstance) {
  app.get("/api/symbols", async () => {
    return SYMBOLS.map((s) => ({
      ...s,
      lastPrice: marketData.getLastPrice(s.symbol) ?? null,
    }));
  });

  app.get<{ Params: { symbol: string } }>("/api/symbols/:symbol/depth", async (request) => {
    return exchange.getDepth(request.params.symbol);
  });

  app.get<{ Params: { symbol: string }; Querystring: { limit?: string } }>(
    "/api/symbols/:symbol/history",
    async (request) => {
      const limit = request.query.limit ? Number(request.query.limit) : undefined;
      return marketData.getHistory(request.params.symbol, limit);
    }
  );
}
