import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import { getSocket } from "./socket";
import { useSession } from "./session";
import type { Order } from "./types";

/** The account's working (open or partly filled) orders, kept fresh from the server. */
export function usePendingOrders() {
  const { account } = useSession();
  const [pending, setPending] = useState<Order[]>([]);

  const reload = useCallback(() => {
    if (!account) return;
    api
      .get<Order[]>("/api/orders/pending")
      .then(setPending)
      .catch(() => {});
  }, [account]);

  useEffect(reload, [reload]);

  useEffect(() => {
    if (!account) return;
    const socket = getSocket();
    socket.on("orderAccepted", reload);
    socket.on("orderCancelled", reload);
    socket.on("fill", reload);
    // Reconnection resync: after a dropped connection trust the server's state, not what we held before.
    socket.on("connect", reload);
    return () => {
      socket.off("orderAccepted", reload);
      socket.off("orderCancelled", reload);
      socket.off("fill", reload);
      socket.off("connect", reload);
    };
  }, [account, reload]);

  const cancel = useCallback(
    async (order: Order) => {
      await api.del(`/api/orders/${order.symbol}/${order.id}`);
      reload();
    },
    [reload]
  );

  return { pending, reload, cancel };
}
