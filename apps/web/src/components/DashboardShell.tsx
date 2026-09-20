import { useEffect, useState } from "react";
import { useSession } from "@/lib/session";
import { useControlState } from "@/lib/useControlState";
import { useConnection } from "@/lib/useConnection";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { Fill, SymbolInfo } from "@/lib/types";
import { Watchlist } from "./Watchlist";
import { OrderBookLadder } from "./OrderBookLadder";
import { OrderTicket } from "./OrderTicket";
import { PriceChart } from "./PriceChart";
import { Portfolio } from "./Portfolio";
import { Leaderboard } from "./Leaderboard";
import { FundBrowser } from "./FundBrowser";
import { FundManagerPanel } from "./FundManagerPanel";
import { NewsFeed } from "./NewsFeed";
import { ErrorBoundary } from "./ErrorBoundary";
import { Tick } from "./Tick";

const DEFAULT_SYMBOL = "ACME";

type Tab = "portfolio" | "leaderboard" | "funds" | "ops" | "news";

export function DashboardShell() {
  const { account, logout } = useSession();
  const { tradingFrozen } = useControlState();
  const connection = useConnection();
  const [symbol, setSymbol] = useState(DEFAULT_SYMBOL);
  const [tab, setTab] = useState<Tab>("portfolio");
  const [info, setInfo] = useState<SymbolInfo | null>(null);
  const [live, setLive] = useState<number | null>(null);
  const [lastTick, setLastTick] = useState<number | null>(null);

  useEffect(() => {
    setLive(null);
    const socket = getSocket();
    socket.emit("subscribe:symbol", symbol);
    const onTrade = (f: Fill) => {
      if (f.symbol !== symbol) return;
      setLive(f.price);
      setLastTick(Date.now());
    };
    socket.on("trade", onTrade);
    return () => {
      socket.off("trade", onTrade);
    };
  }, [symbol]);

  useEffect(() => {
    let alive = true;
    api
      .get<SymbolInfo[]>("/api/symbols")
      .then((all) => alive && setInfo(all.find((s) => s.symbol === symbol) ?? null))
      .catch(() => {});
    return () => {
      alive = false;
    };
  }, [symbol]);

  if (!account) return null;

  const tabs: { id: Tab; label: string }[] = [
    { id: "portfolio", label: "Holdings" },
    { id: "leaderboard", label: "Standings" },
    ...(account.role === "investor" ? [{ id: "funds" as Tab, label: "Funds" }] : []),
    ...(account.role === "fund_manager"
      ? [
          { id: "ops" as Tab, label: "Fund desk" },
          { id: "news" as Tab, label: "Wire" },
        ]
      : []),
  ];

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <header className="masthead">
        <div className="brand">
          Stockastic<small>Live Financial Ecosystem</small>
        </div>
        {tradingFrozen && <span className="chip alert">Trading frozen</span>}
        <span style={{ marginLeft: "auto" }} className="dim">
          {account.displayName} <span className="label">· {account.role.replace("_", " ")}</span>
        </span>
        <span>
          <span className="label">Cash </span>
          <span className="mono">{account.cashBalance.toFixed(2)}</span>
        </span>
        {account.isAdmin && <a href="/admin">Console</a>}
        <button onClick={logout}>Sign out</button>
      </header>

      {connection !== "open" && (
        <div role="status" className="banner">
          {connection === "unauthenticated"
            ? "Your session is no longer valid. Please sign in again."
            : "Live connection lost — reconnecting. Prices and your orders may be out of date until it returns; orders you place are safe to retry."}
        </div>
      )}

      <div className="terminal">
        <aside className="col-left">
          <ErrorBoundary name="Watchlist">
            <Watchlist selected={symbol} onSelect={setSymbol} />
          </ErrorBoundary>
        </aside>

        <main className="col-center">
          <div className="symbol-head">
            <h1>{symbol}</h1>
            <span className="dim">{info?.displayName}</span>
            <Tick value={live ?? info?.lastPrice} className="last" />
          </div>

          <div className="chart-row">
            <ErrorBoundary name="Price chart">
              <PriceChart symbol={symbol} />
            </ErrorBoundary>
            <ErrorBoundary name="Order book">
              <OrderBookLadder symbol={symbol} />
            </ErrorBoundary>
          </div>

          <section className="lower">
            <div className="tabs" role="tablist">
              {tabs.map((t) => (
                <button key={t.id} role="tab" aria-selected={tab === t.id} onClick={() => setTab(t.id)}>
                  {t.label}
                </button>
              ))}
            </div>
            <div className="tabpane">
              <ErrorBoundary name={tab}>
                {tab === "portfolio" && <Portfolio />}
                {tab === "leaderboard" && <Leaderboard />}
                {tab === "funds" && <FundBrowser />}
                {tab === "ops" && <FundManagerPanel />}
                {tab === "news" && <NewsFeed title="Fund manager wire (early feed)" />}
              </ErrorBoundary>
            </div>
          </section>
        </main>

        <aside className="col-right">
          <ErrorBoundary name="Order ticket">
            <OrderTicket symbol={symbol} tradingFrozen={tradingFrozen} />
          </ErrorBoundary>
        </aside>
      </div>

      <footer className="statusbar">
        <span>
          <i className={`dot ${connection === "open" ? "" : "off"}`} />
          {connection === "open" ? "Live" : "Reconnecting"}
        </span>
        <span>Last trade {lastTick ? new Date(lastTick).toLocaleTimeString() : "—"}</span>
        <span style={{ marginLeft: "auto" }}>Trades are final</span>
      </footer>
    </div>
  );
}
