import { useSyncExternalStore } from "react";
import { getSocket, type WireStatus } from "./socket";

/** The live-connection status, for a banner that tells the user their data may be stale. */
export function useConnection(): WireStatus {
  const wire = getSocket();
  return useSyncExternalStore(wire.subscribeStatus, () => wire.status);
}
