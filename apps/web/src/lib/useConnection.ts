import { useEffect, useState, useSyncExternalStore } from "react";
import { getSocket, type WireStatus } from "./socket";

/** The live-connection status, for a banner that tells the user their data may be stale. */
export function useConnection(): WireStatus {
  const wire = getSocket();
  return useSyncExternalStore(wire.subscribeStatus, () => wire.status);
}

/**
 * Whether to warn that prices may be out of date: only when the live connection has really been lost for a few
 * seconds. Connecting when a page opens, or a blip that reconnects at once, shows nothing.
 */
export function useConnectionProblem(graceMs = 4000): WireStatus | null {
  const status = useConnection();
  const [shown, setShown] = useState<WireStatus | null>(null);
  useEffect(() => {
    if (status === "open" || status === "connecting") {
      setShown(null);
      return;
    }
    if (status === "unauthenticated") {
      setShown(status);
      return;
    }
    const t = setTimeout(() => setShown(status), graceMs);
    return () => clearTimeout(t);
  }, [status, graceMs]);
  return shown;
}
