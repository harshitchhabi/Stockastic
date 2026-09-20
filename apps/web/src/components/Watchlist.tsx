import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { Fill, SymbolInfo } from "@/lib/types";
import { Tick } from "./Tick";

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
    <div className="panel" style={{ flex: 1 }}>
      <div className="panel-header">Watchlist</div>
      <div className="panel-body" style={{ padding: 0 }}>
        {symbols.map((s) => (
          <button
            key={s.symbol}
            className="watch-row"
            aria-current={s.symbol === selected}
            onClick={() => onSelect(s.symbol)}
          >
            <span>
              <span className="sym">{s.symbol}</span>
              <br />
              <span className="name">{s.displayName}</span>
            </span>
            <Tick value={s.lastPrice} />
          </button>
        ))}
      </div>
    </div>
  );
}
