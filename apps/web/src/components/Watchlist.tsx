import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { Fill, SymbolInfo } from "@/lib/types";

export function Watchlist({
  selected,
  onSelect,
}: {
  selected: string;
  onSelect: (symbol: string) => void;
}) {
  const [symbols, setSymbols] = useState<SymbolInfo[]>([]);

  useEffect(() => {
    api.get<SymbolInfo[]>("/api/symbols").then(setSymbols);
  }, []);

  useEffect(() => {
    const socket = getSocket();
    for (const s of symbols) socket.emit("subscribe:symbol", s.symbol);

    const onTrade = (fill: Fill) => {
      setSymbols((prev) =>
        prev.map((s) => (s.symbol === fill.symbol ? { ...s, lastPrice: fill.price } : s))
      );
    };
    socket.on("trade", onTrade);
    return () => {
      socket.off("trade", onTrade);
    };
  }, [symbols.length]);

  return (
    <div className="panel">
      <div className="panel-header">Watchlist</div>
      <div className="panel-body" style={{ padding: 0 }}>
        <table>
          <thead>
            <tr>
              <th>Symbol</th>
              <th>Last</th>
            </tr>
          </thead>
          <tbody>
            {symbols.map((s) => (
              <tr
                key={s.symbol}
                onClick={() => onSelect(s.symbol)}
                style={{
                  cursor: "pointer",
                  background: s.symbol === selected ? "#1a2233" : undefined,
                }}
              >
                <td>
                  <div>{s.symbol}</div>
                  <div style={{ fontSize: 10, color: "var(--text-dim)" }}>{s.displayName}</div>
                </td>
                <td className="mono">{s.lastPrice != null ? s.lastPrice.toFixed(2) : "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
