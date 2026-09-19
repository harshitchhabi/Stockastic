import { EventEmitter } from "node:events";
import { CONFIG, isWindowOpen, type TradingWindow } from "@stockastic/config";
import type { ControlStateRow } from "../db/repository";
import { saveControlState } from "../db/repository";

export type WindowName = "tradingRound1" | "fundAllocationWindow" | "tradingRound2";
export type WindowOverride = "open" | "closed" | undefined;

/** Broadcast hook so the websocket gateway can push control-state changes to every connected client instantly. */
export const controlEvents = new EventEmitter();

/**
 * Live, human-operable state that sits ALONGSIDE the config-driven schedule,
 * not instead of it: an admin can force a window open/closed regardless of
 * what CONFIG.windows says, and force-freeze all trading instantly. Backed
 * by the `control_state` singleton row so it survives a restart too.
 */
class ControlState {
  private tradingFrozen = false;
  private windowOverrides: Partial<Record<WindowName, WindowOverride>> = {};

  hydrate(row: ControlStateRow): void {
    this.tradingFrozen = row.tradingFrozen;
    this.windowOverrides = row.windowOverrides as Partial<Record<WindowName, WindowOverride>>;
  }

  isTradingFrozen(): boolean {
    return this.tradingFrozen;
  }

  async setTradingFrozen(frozen: boolean): Promise<void> {
    this.tradingFrozen = frozen;
    await this.persist();
    controlEvents.emit("change", this.snapshot());
  }

  /** An explicit admin override wins over the config schedule; `undefined` defers back to config. */
  isWindowOpenNow(name: WindowName): boolean {
    const override = this.windowOverrides[name];
    if (override === "open") return true;
    if (override === "closed") return false;
    return isWindowOpen(CONFIG.windows[name] as TradingWindow);
  }

  async setWindowOverride(name: WindowName, override: WindowOverride): Promise<void> {
    this.windowOverrides[name] = override;
    await this.persist();
    controlEvents.emit("change", this.snapshot());
  }

  snapshot() {
    return {
      tradingFrozen: this.tradingFrozen,
      windowOverrides: { ...this.windowOverrides },
    };
  }

  private async persist(): Promise<void> {
    await saveControlState({
      tradingFrozen: this.tradingFrozen,
      windowOverrides: this.windowOverrides as Record<string, WindowOverride>,
    });
  }
}

export const controlState = new ControlState();
