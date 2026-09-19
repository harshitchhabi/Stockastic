import Fastify from "fastify";
import cors from "@fastify/cors";
import { attachWebsocketGateway } from "./ws/gateway";
import { initLedgerListener } from "./state/ledgerListener";
import { runMigrations } from "./db/migrate";
import { hydrateFromDatabase } from "./db/hydrate";
import { bootstrapAdmin } from "./auth/bootstrap";
import { ensureSymbol } from "./db/repository";
import { SYMBOLS } from "./state/symbols";
import { configRoutes } from "./routes/config";
import { symbolRoutes } from "./routes/symbols";
import { authRoutes } from "./routes/auth";
import { orderRoutes } from "./routes/orders";
import { portfolioRoutes } from "./routes/portfolio";
import { leaderboardRoutes } from "./routes/leaderboard";
import { newsRoutes } from "./routes/news";
import { fundRoutes } from "./routes/funds";
import { adminRoutes } from "./routes/admin";

const PORT = Number(process.env.PORT ?? 4000);

async function main() {
  await runMigrations();
  for (const s of SYMBOLS) await ensureSymbol(s.symbol, s.displayName);

  // Order matters: rebuild in-memory state from Postgres BEFORE the ledger
  // listener attaches (nothing should be trading yet) and before the app
  // starts accepting requests.
  await hydrateFromDatabase();
  initLedgerListener();
  await bootstrapAdmin();

  const app = Fastify({ logger: true });
  await app.register(cors, { origin: "*" }); // TODO: lock down once deployment origin is known

  await app.register(configRoutes);
  await app.register(symbolRoutes);
  await app.register(authRoutes);
  await app.register(orderRoutes);
  await app.register(portfolioRoutes);
  await app.register(leaderboardRoutes);
  await app.register(newsRoutes);
  await app.register(fundRoutes);
  await app.register(adminRoutes);

  app.get("/health", async () => ({ ok: true }));

  await app.ready();
  attachWebsocketGateway(app.server);

  await app.listen({ port: PORT, host: "0.0.0.0" });
}

main().catch((err) => {
  console.error(err);
  process.exit(1);
});
