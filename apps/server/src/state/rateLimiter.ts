import { CONFIG } from "@stockastic/config";

/**
 * Fixed-window rate limiter, per account, for order submission. In-memory
 * only — losing counters on a restart just means everyone gets a fresh
 * window, which is an acceptable reset (unlike ledger state, this isn't a
 * durability requirement). Cheap to add now; expensive to retrofit once
 * real teams are pounding the submit button.
 */
class RateLimiter {
  private readonly hits = new Map<string, number[]>();

  /** Returns true if the account is within its allowed rate, recording this attempt if so. */
  tryConsume(accountId: string): boolean {
    const { ordersPerWindow, windowMs } = CONFIG.rateLimits;
    const now = Date.now();
    const windowStart = now - windowMs;

    const timestamps = (this.hits.get(accountId) ?? []).filter((t) => t > windowStart);

    if (timestamps.length >= ordersPerWindow) {
      this.hits.set(accountId, timestamps);
      return false;
    }

    timestamps.push(now);
    this.hits.set(accountId, timestamps);
    return true;
  }
}

export const orderRateLimiter = new RateLimiter();
