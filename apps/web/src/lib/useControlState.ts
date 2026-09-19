"use client";

import { useEffect, useState } from "react";
import { getSocket } from "./socket";

export interface ControlStateSnapshot {
  tradingFrozen: boolean;
  windowOverrides: Record<string, "open" | "closed" | undefined>;
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
