import { EventEmitter } from "node:events";
import { randomUUID } from "node:crypto";
import { CONFIG } from "@stockastic/config";

export interface NewsItem {
  id: string;
  headline: string;
  body?: string;
  createdAt: number;
}

interface DispatcherEvents {
  /** Fired immediately for fund managers. */
  fundManagerNews: (item: NewsItem) => void;
  /** Fired after CONFIG.news.fundManagerLeadTimeMs for everyone else. */
  publicNews: (item: NewsItem) => void;
}

/**
 * Two-tier news dispatch: fund managers and the public are two SEPARATE
 * delivery queues, never a single broadcast. The delay between them is read
 * from config on every dispatch (not captured at startup) so it can be
 * tuned live without a restart while the rulebook is still settling.
 */
export class NewsDispatcher extends EventEmitter {
  private readonly items: NewsItem[] = [];
  private readonly pendingTimers = new Set<NodeJS.Timeout>();

  publish(headline: string, body?: string): NewsItem {
    const item: NewsItem = { id: randomUUID(), headline, body, createdAt: Date.now() };
    this.items.push(item);

    // Fund-manager queue: immediate.
    this.emit("fundManagerNews", item);

    // Public queue: delayed by the current configured lead time.
    const delayMs = CONFIG.news.fundManagerLeadTimeMs;
    const timer = setTimeout(() => {
      this.pendingTimers.delete(timer);
      this.emit("publicNews", item);
    }, delayMs);
    this.pendingTimers.add(timer);

    return item;
  }

  list(): NewsItem[] {
    return [...this.items];
  }

  /** For tests/shutdown: cancel any not-yet-fired public dispatches. */
  clearPending() {
    for (const timer of this.pendingTimers) clearTimeout(timer);
    this.pendingTimers.clear();
  }
}

export interface NewsDispatcher {
  on<K extends keyof DispatcherEvents>(event: K, listener: DispatcherEvents[K]): this;
  emit<K extends keyof DispatcherEvents>(event: K, ...args: Parameters<DispatcherEvents[K]>): boolean;
}

export const newsDispatcher = new NewsDispatcher();
