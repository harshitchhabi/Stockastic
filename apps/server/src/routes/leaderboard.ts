import type { FastifyInstance } from "fastify";
import { CONFIG } from "@stockastic/config";
import { accountStore } from "../state/accounts";
import { marketData } from "../state/market";

export async function leaderboardRoutes(app: FastifyInstance) {
  // Fields here are a stub: rank + portfolio value + % return only.
  // Scoring rubric (Prize 3/4) and tie-break logic are explicitly unfinalized
  // per the rulebook — do not build anything downstream of those yet.
  app.get("/api/leaderboard", async () => {
    const rows = accountStore.list().map((account) => {
      const holdingsValue = accountStore
        .holdingsFor(account.id)
        .reduce((sum, h) => sum + (marketData.getLastPrice(h.symbol) ?? h.avgPrice) * h.qty, 0);
      const totalValue = account.cashBalance + holdingsValue;
      const percentReturn =
        ((totalValue - CONFIG.accounts.startingCapital) / CONFIG.accounts.startingCapital) * 100;

      return {
        accountId: account.id,
        displayName: account.displayName,
        portfolioValue: totalValue,
        percentReturn,
      };
    });

    // TODO: tie-break strategy is CONFIG.scoring.tieBreakStrategy = "TBD".
    // Naive sort by value only, until the rulebook locks tie-break rules.
    rows.sort((a, b) => b.portfolioValue - a.portfolioValue);

    return rows.map((row, index) => ({ rank: index + 1, ...row }));
  });
}
