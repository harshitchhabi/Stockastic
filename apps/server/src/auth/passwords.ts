import bcrypt from "bcrypt";

const SALT_ROUNDS = 10;

/**
 * Native bcrypt, not bcryptjs. bcryptjs is pure JS and hashes/compares
 * synchronously on Node's single thread — a k6 load test at ~150 concurrent
 * signups/logins showed request latency climbing past 15s p95 purely from
 * password hashing saturating the event loop, well before the matching
 * engine or Postgres were anywhere near their limits. Native bcrypt offloads
 * the actual hashing to libuv's threadpool, so it no longer blocks request
 * handling (including live order matching) under concurrent auth traffic.
 */
export async function hashPassword(password: string): Promise<string> {
  return bcrypt.hash(password, SALT_ROUNDS);
}

export async function verifyPassword(password: string, hash: string): Promise<boolean> {
  return bcrypt.compare(password, hash);
}
