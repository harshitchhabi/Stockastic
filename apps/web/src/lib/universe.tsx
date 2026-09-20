import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import { api } from "./api";
import { getSocket } from "./socket";
import type { Fill, SymbolInfo } from "./types";

/** A company with its live price and how far it has moved since the session's opening price. */
export interface Company extends SymbolInfo {
  /** The reference price the change is measured from. */
  base: number | null;
  change: number | null;
  changePct: number | null;
}

interface Universe {
  companies: Company[];
  bySymbol: Map<string, Company>;
  sectors: [string, number][];
  starred: Set<string>;
  toggleStar: (symbol: string) => void;
  loaded: boolean;
}

const Ctx = createContext<Universe | null>(null);

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

const FLUSH_MS = 500;

/**
 * One shared source of truth for every company's price. All ~250 symbols stream in, but updates are
 * buffered and applied at most every FLUSH_MS so a busy market cannot make the UI re-render on every
 * single trade.
 */
export function UniverseProvider({ children }: { children: React.ReactNode }) {
  const [symbols, setSymbols] = useState<SymbolInfo[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [starred, setStarred] = useState<Set<string>>(loadStarred);
  const bases = useRef(new Map<string, number>());
  const pending = useRef(new Map<string, number>());

  const load = useCallback(() => {
    api
      .get<SymbolInfo[]>("/api/symbols")
      .then((list) => {
        for (const s of list) {
          // Prefer the server's opening price; otherwise measure from the first price we ever saw.
          const b = s.openPrice ?? s.lastPrice;
          if (b != null && (s.openPrice != null || !bases.current.has(s.symbol))) bases.current.set(s.symbol, b);
        }
        setSymbols(list);
        setLoaded(true);
      })
      .catch(() => {});
  }, []);

  useEffect(load, [load]);

  useEffect(() => {
    const socket = getSocket();
    const subscribeAll = () => {
      for (const s of symbols) socket.emit("subscribe:symbol", s.symbol);
    };
    subscribeAll();
    const onTrade = (f: Fill) => pending.current.set(f.symbol, f.price);
    socket.on("trade", onTrade);
    // After a reconnect the client's view may be stale and its room memberships are gone: refetch and resubscribe.
    const onConnect = () => {
      subscribeAll();
      load();
    };
    socket.on("connect", onConnect);

    const timer = setInterval(() => {
      if (pending.current.size === 0) return;
      const batch = pending.current;
      pending.current = new Map();
      setSymbols((prev) => prev.map((s) => (batch.has(s.symbol) ? { ...s, lastPrice: batch.get(s.symbol)! } : s)));
    }, FLUSH_MS);

    return () => {
      clearInterval(timer);
      socket.off("trade", onTrade);
      socket.off("connect", onConnect);
    };
  }, [symbols.length, load]);

  const toggleStar = useCallback((symbol: string) => {
    setStarred((prev) => {
      const next = new Set(prev);
      if (!next.delete(symbol)) next.add(symbol);
      try {
        localStorage.setItem(STAR_KEY, JSON.stringify([...next]));
      } catch {
        /* ignore */
      }
      return next;
    });
  }, []);

  const value = useMemo<Universe>(() => {
    const companies: Company[] = symbols.map((s) => {
      const base = bases.current.get(s.symbol) ?? null;
      const change = s.lastPrice != null && base != null ? s.lastPrice - base : null;
      return { ...s, base, change, changePct: change != null && base ? (change / base) * 100 : null };
    });
    const counts = new Map<string, number>();
    for (const c of companies) if (c.sector) counts.set(c.sector, (counts.get(c.sector) ?? 0) + 1);
    return {
      companies,
      bySymbol: new Map(companies.map((c) => [c.symbol, c])),
      sectors: [...counts.entries()].sort((a, b) => a[0].localeCompare(b[0])),
      starred,
      toggleStar,
      loaded,
    };
  }, [symbols, starred, toggleStar, loaded]);

  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export function useUniverse(): Universe {
  const v = useContext(Ctx);
  if (!v) throw new Error("useUniverse outside UniverseProvider");
  return v;
}
