"use client";

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
      <div className="panel-header">Order Book · {symbol}</div>
      <div className="panel-body">
        <table>
          <thead>
            <tr>
              <th>Price</th>
              <th>Qty</th>
              <th>Orders</th>
            </tr>
          </thead>
          <tbody>
            {[...(depth?.asks ?? [])].reverse().map((level) => (
              <tr key={`ask-${level.price}`} className="ask-row">
                <td className="mono">{level.price.toFixed(2)}</td>
                <td className="mono" style={depthBarStyle(level.qty, maxQty, "var(--red)")}>
                  {level.qty}
                </td>
                <td className="mono">{level.orderCount}</td>
              </tr>
            ))}
            <tr>
              <td colSpan={3} style={{ textAlign: "center", padding: "4px 0", color: "var(--text-dim)" }}>
                spread{" "}
                {depth?.bids[0] && depth?.asks[0]
                  ? (depth.asks[0].price - depth.bids[0].price).toFixed(2)
                  : "—"}
              </td>
            </tr>
            {(depth?.bids ?? []).map((level) => (
              <tr key={`bid-${level.price}`} className="bid-row">
                <td className="mono">{level.price.toFixed(2)}</td>
                <td className="mono" style={depthBarStyle(level.qty, maxQty, "var(--green)")}>
                  {level.qty}
                </td>
                <td className="mono">{level.orderCount}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}

function depthBarStyle(qty: number, maxQty: number, color: string): React.CSSProperties {
  const pct = Math.round((qty / maxQty) * 100);
  return {
    background: `linear-gradient(to left, ${color}22 ${pct}%, transparent ${pct}%)`,
  };
}
