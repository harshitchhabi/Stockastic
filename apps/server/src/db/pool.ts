import { Pool } from "pg";

/**
 * Lazily-initialized Postgres pool. If DATABASE_URL isn't set (e.g. running
 * the dev server without a local Postgres yet), we log once and every query
 * helper below becomes a no-op — this lets Phase 1 UI/engine work proceed
 * without a hard dependency on the database being provisioned first.
 */
const connectionString = process.env.DATABASE_URL;

// node-postgres defaults `max` to 10 connections, which becomes the real
// bottleneck under concurrent order submission — the matching engine itself
// is in-memory and sub-millisecond, but every submission does several
// sequential write-through queries (order, fills, account, holdings), and
// with only 10 pooled connections those queue up hard past a couple hundred
// concurrent requests. A k6 run against ~150 concurrent order submitters
// showed p95 request latency over 6s with the default; raising the pool
// size is the fix, not the query logic. Tune via POSTGRES_POOL_SIZE for the
// actual event's concurrency (~700-750 clients, a fraction of which submit
// orders at any instant) — Postgres' own default max_connections is 100, so
// this must stay comfortably under that with room for other connections
// (migrations, admin tooling).
const POOL_SIZE = Number(process.env.POSTGRES_POOL_SIZE ?? 50);

export const pool = connectionString
  ? new Pool({ connectionString, max: POOL_SIZE })
  : null;

let warned = false;
function warnNoDb() {
  if (!warned) {
    warned = true;
    // eslint-disable-next-line no-console
    console.warn(
      "[db] DATABASE_URL not set — persistence is disabled, running with in-memory state only."
    );
  }
}

export async function query<T = unknown>(text: string, params?: unknown[]): Promise<T[]> {
  if (!pool) {
    warnNoDb();
    return [];
  }
  const result = await pool.query(text, params);
  return result.rows as T[];
}

export function isDbEnabled(): boolean {
  return pool !== null;
}
