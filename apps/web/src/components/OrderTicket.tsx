import { useState } from "react";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import { usePendingOrders } from "@/lib/useOrders";
import type { OrderSide } from "@/lib/types";

export function OrderTicket({ symbol, tradingFrozen }: { symbol: string; tradingFrozen: boolean }) {
  const { account } = useSession();
  const { pending, reload, cancel } = usePendingOrders();
  const [side, setSide] = useState<OrderSide>("buy");
  const [orderType, setOrderType] = useState<"limit" | "market">("limit");
  const [price, setPrice] = useState("");
  const [qty, setQty] = useState("");
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const mine = pending.filter((o) => o.symbol === symbol);
  const value = (Number(price) || 0) * (Number(qty) || 0);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!account) return;
    setError(null);
    setSubmitting(true);
    try {
      // Client-generated idempotency key: safe to retry this exact submit (a flaky connection, or a
      // reconnect-and-retry) without risking a duplicate order — the server dedupes on (account, this id).
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
      reload();
    } catch (err) {
      setError(err instanceof Error ? err.message : "order failed");
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="panel ticket">
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

          <div className="ticket-summary">
            <div>
              <span className="label">{orderType === "market" ? "Up to" : "Order value"}</span>
              <span className="mono">{value > 0 ? value.toFixed(2) : "—"}</span>
            </div>
            <div>
              <span className="label">Cash available</span>
              <span className="mono">{account ? account.cashBalance.toFixed(2) : "—"}</span>
            </div>
          </div>

          {error && <div className="down">{error}</div>}
          {tradingFrozen && <div className="down">Trading is frozen by the organizer.</div>}

          <button type="submit" className="solid" disabled={!account || submitting || tradingFrozen} style={{ padding: 10 }}>
            {submitting ? "Submitting…" : `Place ${orderType} ${side} order`}
          </button>
        </form>

        {mine.length > 0 && (
          <div style={{ marginTop: 22 }}>
            <div className="label" style={{ marginBottom: 6 }}>
              Working orders in {symbol}
            </div>
            <table>
              <tbody>
                {mine.map((o) => (
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
              </tbody>
            </table>
          </div>
        )}
      </div>
    </div>
  );
}
