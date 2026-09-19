import { readFileSync } from "node:fs";
import { join } from "node:path";
import { pool, isDbEnabled } from "./pool";

/**
 * Applies schema.sql on startup. Every statement in it is CREATE ... IF NOT
 * EXISTS, so this is safe to run every boot — no separate migration-tracking
 * table needed yet at this stage of the build.
 */
export async function runMigrations(): Promise<void> {
  if (!isDbEnabled() || !pool) return;

  const schemaPath = join(__dirname, "schema.sql");
  const schema = readFileSync(schemaPath, "utf-8")
    // Strip full-line comments first so a comment immediately preceding a
    // statement doesn't make the whole chunk look comment-only after split.
    .replace(/^--.*$/gm, "");

  const statements = schema
    .split(";")
    .map((s) => s.trim())
    .filter((s) => s.length > 0);

  for (const statement of statements) {
    await pool.query(statement);
  }

  // eslint-disable-next-line no-console
  console.log(`[db] applied ${statements.length} schema statements`);
}
