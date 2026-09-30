
import { useEffect, useState } from "react";
import { getSocket } from "./socket";

export interface ControlStateSnapshot {
  tradingFrozen: boolean;
  windowOverrides: Record<string, "open" | "closed" | undefined>;
  marketOpen?: boolean;
  /** "" before the event starts, then phase1, transition, phase2 or closing. */
  stage?: string;
  step?: string;
  /** The allocation window open now, or -1. */
  openWindow?: number;
  /** The organisers have paused the whole event (a break): nothing moves until they resume. */
  paused?: boolean;
  /** Players may see the standings now (Phase 1 trading only). */
  standingsOpen?: boolean;
}

/**
 * Live admin control state (force-freeze, window overrides), pushed over the
 * socket the instant an admin changes it — see server ws/gateway.ts
 * `controlEvents`. Also delivered fresh on every connect/reconnect so a
 * client that was offline when a freeze happened still finds out immediately.
 */
export function useControlState(): ControlStateSnapshot {
  const [state, setState] = useState<ControlStateSnapshot>({ tradingFrozen: false, windowOverrides: {} });

  useEffect(() => {
    const socket = getSocket();
    const onControlState = (snapshot: ControlStateSnapshot) => setState(snapshot);
    socket.on("controlState", onControlState);
    return () => {
      socket.off("controlState", onControlState);
    };
  }, []);

  return state;
}
