import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";
import type { Order, OrderSide } from "@/lib/types";

export function OrderTicket({ symbol, tradingFrozen }: { symbol: string; tradingFrozen: boolean }) {
  const { account } = useSession();
  const [side, setSide] = useState<OrderSide>("buy");
  const [price, setPrice] = useState("");
  const [qty, setQty] = useState("");
  const [pending, setPending] = useState<Order[]>([]);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const loadPending = useCallback(() => {
    if (!account) return;
    api
      .get<Order[]>("/api/orders/pending")
      .then(setPending)
      .catch(() => {});
  }, [account]);

  useEffect(() => {
    loadPending();
  }, [loadPending]);

  useEffect(() => {
    if (!account) return;
    const socket = getSocket();
    const refresh = () => loadPending();
    socket.on("orderAccepted", refresh);
    socket.on("orderCancelled", refresh);
    socket.on("fill", refresh);
    // Reconnection resync: after a dropped wifi and reconnect, trust the
    // server's current state, not whatever this panel had before the drop.
    socket.on("connect", refresh);
    return () => {
      socket.off("orderAccepted", refresh);
      socket.off("orderCancelled", refresh);
      socket.off("fill", refresh);
      socket.off("connect", refresh);
    };
  }, [account, loadPending]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!account) return;
    setError(null);
    setSubmitting(true);
    try {
      // Client-generated idempotency key: safe to retry this exact submit
      // (e.g. a flaky connection, or a reconnect-and-retry after one) without
      // risking a duplicate order — the server dedupes on (account, this id).
      const clientOrderId = crypto.randomUUID();
      await api.postIdempotent("/api/orders", {
        clientOrderId,
        symbol,
        side,
        price: Number(price),
        qty: Number(qty),
      });
      setPrice("");
      setQty("");
      loadPending();
    } catch (err) {
      setError(err instanceof Error ? err.message : "order failed");
    } finally {
      setSubmitting(false);
    }
  }

  async function cancel(order: Order) {
    await api.del(`/api/orders/${order.symbol}/${order.id}`);
    loadPending();
  }

  return (
    <div className="panel">
      <div className="panel-header">Order Ticket · {symbol}</div>
      <div className="panel-body">
        <form onSubmit={submit} style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          <div style={{ display: "flex", gap: 4 }}>
            <button
              type="button"
              onClick={() => setSide("buy")}
              style={{
                flex: 1,
                background: side === "buy" ? "var(--green)" : "transparent",
                color: side === "buy" ? "#04140b" : "var(--text)",
                border: "1px solid var(--green)",
                borderRadius: 4,
                padding: 6,
                fontWeight: 600,
              }}
            >
              BUY
            </button>
            <button
              type="button"
              onClick={() => setSide("sell")}
              style={{
                flex: 1,
                background: side === "sell" ? "var(--red)" : "transparent",
                color: side === "sell" ? "#210608" : "var(--text)",
                border: "1px solid var(--red)",
                borderRadius: 4,
                padding: 6,
                fontWeight: 600,
              }}
            >
              SELL
            </button>
          </div>

          <label style={{ display: "flex", flexDirection: "column", gap: 2 }}>
            <span style={{ color: "var(--text-dim)", fontSize: 11 }}>Limit price</span>
            <input
              type="number"
              step="0.05"
              required
              value={price}
              onChange={(e) => setPrice(e.target.value)}
            />
          </label>

          <label style={{ display: "flex", flexDirection: "column", gap: 2 }}>
            <span style={{ color: "var(--text-dim)", fontSize: 11 }}>Quantity</span>
            <input type="number" step="1" min="1" required value={qty} onChange={(e) => setQty(e.target.value)} />
          </label>

          {error && <div style={{ color: "var(--red)", fontSize: 11 }}>{error}</div>}
          {tradingFrozen && (
            <div style={{ color: "var(--red)", fontSize: 11 }}>Trading is frozen by the organizer.</div>
          )}

          <button
            type="submit"
            disabled={!account || submitting || tradingFrozen}
            style={{
              background: "var(--accent)",
              color: "#fff",
              border: "none",
              borderRadius: 4,
              padding: 8,
              fontWeight: 600,
            }}
          >
            {submitting ? "Submitting…" : `Place ${side.toUpperCase()} order`}
          </button>
        </form>

        <div style={{ marginTop: 16 }}>
          <div style={{ color: "var(--text-dim)", fontSize: 11, marginBottom: 4 }}>Pending orders</div>
          <table>
            <thead>
              <tr>
                <th>Side</th>
                <th>Price</th>
                <th>Qty left</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {pending.map((o) => (
                <tr key={o.id}>
                  <td className={o.side === "buy" ? "up" : "down"}>{o.side}</td>
                  <td className="mono">{o.price.toFixed(2)}</td>
                  <td className="mono">{o.remainingQty}</td>
                  <td>
                    <button onClick={() => cancel(o)}>✕</button>
                  </td>
                </tr>
              ))}
              {pending.length === 0 && (
                <tr>
                  <td colSpan={4} style={{ color: "var(--text-dim)", textAlign: "center" }}>
                    none
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </div>
    </div>
  );
}
