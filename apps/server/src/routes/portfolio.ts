import type { FastifyInstance } from "fastify";
import { accountStore } from "../state/accounts";
import { marketData } from "../state/market";
import { requireAuth } from "../auth/middleware";

function buildPortfolio(accountId: string) {
  const account = accountStore.get(accountId);
  if (!account) return null;

  const holdings = accountStore.holdingsFor(account.id).map((h) => {
    const lastPrice = marketData.getLastPrice(h.symbol) ?? h.avgPrice;
    return {
      ...h,
      marketValue: lastPrice * h.qty,
      unrealizedPnl: (lastPrice - h.avgPrice) * h.qty,
    };
  });

  const holdingsValue = holdings.reduce((sum, h) => sum + h.marketValue, 0);

  return {
    accountId: account.id,
    cashBalance: account.cashBalance,
    holdings,
    totalValue: account.cashBalance + holdingsValue,
  };
}

export async function portfolioRoutes(app: FastifyInstance) {
  // Reconnection resync: the client should call this (and /api/orders/pending,
  // and the symbol depth endpoint) right after a socket reconnect rather than
  // trusting whatever it had cached from before the drop.
  app.get("/api/portfolio/me", { preHandler: requireAuth }, async (request, reply) => {
    const portfolio = buildPortfolio(request.account!.sub);
    if (!portfolio) return reply.status(404).send({ error: "not_found" });
    return portfolio;
  });
}
