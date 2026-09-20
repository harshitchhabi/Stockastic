import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { BookDepth } from "@/lib/types";

export function OrderBookLadder({ symbol }: { symbol: string }) {
  const [depth, setDepth] = useState<BookDepth | null>(null);

  useEffect(() => {
    let cancelled = false;
    const fetchDepth = () => {
      api.get<BookDepth>(`/api/symbols/${symbol}/depth`).then((d) => {
        if (!cancelled) setDepth(d);
      });
    };

    const socket = getSocket();
    const subscribe = () => socket.emit("subscribe:symbol", symbol);
    subscribe();
    fetchDepth();

    const onUpdate = (d: BookDepth) => {
      if (d.symbol === symbol) setDepth(d);
    };
    socket.on("bookUpdate", onUpdate);

    // Reconnection resync: a fresh socket connection has no room membership
    // and the client's in-memory depth may be stale relative to whatever
    // happened while wifi was down — always trust a fresh REST fetch here.
    socket.on("connect", subscribe);
    socket.on("connect", fetchDepth);

    return () => {
      cancelled = true;
      socket.off("bookUpdate", onUpdate);
      socket.off("connect", subscribe);
      socket.off("connect", fetchDepth);
      socket.emit("unsubscribe:symbol", symbol);
    };
  }, [symbol]);

  const maxQty = Math.max(
    1,
    ...(depth?.bids.map((b) => b.qty) ?? []),
    ...(depth?.asks.map((a) => a.qty) ?? [])
  );

  return (
    <div className="panel">
      <div className="panel-header">The book</div>
      <div className="panel-body" style={{ padding: "0 0 8px" }}>
        <table className="ladder">
          <thead>
            <tr>
              <th>Price</th>
              <th>Qty</th>
              <th>Ord</th>
            </tr>
          </thead>
          <tbody>
            {[...(depth?.asks ?? [])].reverse().map((level) => (
              <tr key={`ask-${level.price}`} className="ask">
                <td>{level.price.toFixed(2)}</td>
                <td>
                  <i className="bar" style={bar(level.qty, maxQty, "var(--down-wash)")} />
                  <span>{level.qty}</span>
                </td>
                <td className="dim">{level.orderCount}</td>
              </tr>
            ))}
            <tr className="mid">
              <td colSpan={3}>
                spread{" "}
                {depth?.bids[0] && depth?.asks[0] ? (depth.asks[0].price - depth.bids[0].price).toFixed(2) : "—"}
              </td>
            </tr>
            {(depth?.bids ?? []).map((level) => (
              <tr key={`bid-${level.price}`} className="bid">
                <td>{level.price.toFixed(2)}</td>
                <td>
                  <i className="bar" style={bar(level.qty, maxQty, "var(--up-wash)")} />
                  <span>{level.qty}</span>
                </td>
                <td className="dim">{level.orderCount}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function bar(qty: number, maxQty: number, color: string): React.CSSProperties {
  return { width: `${Math.max(2, Math.round((qty / maxQty) * 100))}%`, background: color };
}
