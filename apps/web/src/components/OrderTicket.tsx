import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";
import type { Order, OrderSide } from "@/lib/types";

export function OrderTicket({ symbol, tradingFrozen }: { symbol: string; tradingFrozen: boolean }) {
  const { account } = useSession();
  const [side, setSide] = useState<OrderSide>("buy");
  const [orderType, setOrderType] = useState<"limit" | "market">("limit");
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
        type: orderType,
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
    <div className="panel" style={{ flex: 1 }}>
      <div className="panel-header">
        Order ticket <span className="label">{symbol}</span>
      </div>
      <div className="panel-body">
        <form onSubmit={submit} className="stack">
          <div className="seg">
            <button type="button" className="buy" aria-pressed={side === "buy"} onClick={() => setSide("buy")}>
              BUY
            </button>
            <button type="button" className="sell" aria-pressed={side === "sell"} onClick={() => setSide("sell")}>
              SELL
            </button>
          </div>

          <div className="seg">
            <button type="button" aria-pressed={orderType === "limit"} onClick={() => setOrderType("limit")}>
              Limit
            </button>
            <button type="button" aria-pressed={orderType === "market"} onClick={() => setOrderType("market")}>
              Market
            </button>
          </div>

          <label className="field">
            <span className="label">{orderType === "market" ? "Worst price you will accept" : "Limit price"}</span>
            <input type="number" step="0.01" min="0" required value={price} onChange={(e) => setPrice(e.target.value)} />
          </label>

          <label className="field">
            <span className="label">Quantity</span>
            <input type="number" step="1" min="1" required value={qty} onChange={(e) => setQty(e.target.value)} />
          </label>

          {error && <div className="down">{error}</div>}
          {tradingFrozen && <div className="down">Trading is frozen by the organizer.</div>}

          <button type="submit" className="solid" disabled={!account || submitting || tradingFrozen} style={{ padding: 10 }}>
            {submitting ? "Submitting…" : `Place ${orderType} ${side} order`}
          </button>
        </form>

        <div style={{ marginTop: 26 }}>
          <div className="label" style={{ marginBottom: 6 }}>
            Working orders
          </div>
          <table>
            <thead>
              <tr>
                <th>Side</th>
                <th>Price</th>
                <th>Left</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {pending.map((o) => (
                <tr key={o.id}>
                  <td className={o.side === "buy" ? "up" : "down"}>{o.side}</td>
                  <td>{o.price.toFixed(2)}</td>
                  <td>{o.remainingQty}</td>
                  <td>
                    <button className="ghost" onClick={() => cancel(o)} aria-label="Cancel order">
                      ✕
                    </button>
                  </td>
                </tr>
              ))}
              {pending.length === 0 && (
                <tr>
                  <td colSpan={4} className="dim" style={{ textAlign: "center", fontFamily: "var(--serif)", fontStyle: "italic" }}>
                    nothing working
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
