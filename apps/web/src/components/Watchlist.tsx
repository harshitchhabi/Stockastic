import { useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { Fill, SymbolInfo } from "@/lib/types";
import { Tick } from "./Tick";

const STAR_KEY = "stockastic.starred";

// Starred symbols are a per-viewer convenience: storage may be unavailable, so it is always guarded.
function loadStarred(): Set<string> {
  try {
    const raw = localStorage.getItem(STAR_KEY);
    return new Set(raw ? (JSON.parse(raw) as string[]) : []);
  } catch {
    return new Set();
  }
}
function saveStarred(s: Set<string>) {
  try {
    localStorage.setItem(STAR_KEY, JSON.stringify([...s]));
  } catch {
    /* ignore */
  }
}

type View = "all" | "starred";

/** The whole company universe: search by symbol or name, filter by sector, star the ones you follow. */
export function Watchlist({ selected, onSelect }: { selected: string; onSelect: (symbol: string) => void }) {
  const [symbols, setSymbols] = useState<SymbolInfo[]>([]);
  const [query, setQuery] = useState("");
  const [sector, setSector] = useState<string | null>(null);
  const [view, setView] = useState<View>("all");
  const [starred, setStarred] = useState<Set<string>>(loadStarred);

  useEffect(() => {
    api.get<SymbolInfo[]>("/api/symbols").then(setSymbols).catch(() => {});
  }, []);

  useEffect(() => {
    const socket = getSocket();
    for (const s of symbols) socket.emit("subscribe:symbol", s.symbol);

    const onTrade = (fill: Fill) => {
      setSymbols((prev) => prev.map((s) => (s.symbol === fill.symbol ? { ...s, lastPrice: fill.price } : s)));
    };
    socket.on("trade", onTrade);
    return () => {
      socket.off("trade", onTrade);
    };
  }, [symbols.length]);

  const sectors = useMemo(() => {
    const counts = new Map<string, number>();
    for (const s of symbols) if (s.sector) counts.set(s.sector, (counts.get(s.sector) ?? 0) + 1);
    return [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [symbols]);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return symbols.filter(
      (s) =>
        (view === "all" || starred.has(s.symbol)) &&
        (!sector || s.sector === sector) &&
        (!q || s.symbol.toLowerCase().includes(q) || s.displayName.toLowerCase().includes(q))
    );
  }, [symbols, query, sector, view, starred]);

  function toggleStar(symbol: string) {
    setStarred((prev) => {
      const next = new Set(prev);
      if (!next.delete(symbol)) next.add(symbol);
      saveStarred(next);
      return next;
    });
  }

  return (
    <div className="panel" style={{ flex: 1 }}>
      <div className="tabs" role="tablist">
        <button role="tab" aria-selected={view === "all"} onClick={() => setView("all")}>
          All companies
        </button>
        <button role="tab" aria-selected={view === "starred"} onClick={() => setView("starred")}>
          Starred{starred.size > 0 ? ` (${starred.size})` : ""}
        </button>
      </div>

      <div className="filters">
        <input
          type="search"
          placeholder="Search symbol or company"
          aria-label="Search companies"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {sectors.length > 0 && (
          <div className="chips" role="group" aria-label="Filter by sector">
            <button aria-pressed={sector === null} onClick={() => setSector(null)}>
              All
            </button>
            {sectors.map(([name, n]) => (
              <button key={name} aria-pressed={sector === name} onClick={() => setSector(sector === name ? null : name)}>
                {name} <span className="n">{n}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      <div className="panel-body" style={{ padding: 0 }}>
        {shown.map((s) => (
          <div key={s.symbol} className="watch-row" aria-current={s.symbol === selected}>
            <button
              className="star"
              aria-label={starred.has(s.symbol) ? `Unstar ${s.symbol}` : `Star ${s.symbol}`}
              aria-pressed={starred.has(s.symbol)}
              onClick={() => toggleStar(s.symbol)}
            >
              {starred.has(s.symbol) ? "★" : "☆"}
            </button>
            <button className="pick" onClick={() => onSelect(s.symbol)}>
              <span>
                <span className="sym">{s.symbol}</span>
                <br />
                <span className="name">
                  {s.displayName}
                  {s.sector ? ` · ${s.sector}` : ""}
                </span>
              </span>
              <Tick value={s.lastPrice} />
            </button>
          </div>
        ))}
        {shown.length === 0 && (
          <div className="empty">{view === "starred" && starred.size === 0 ? "star a company to follow it" : "no company matches"}</div>
        )}
      </div>

      <div className="count label">
        {shown.length} of {symbols.length} companies
      </div>
    </div>
  );
}
