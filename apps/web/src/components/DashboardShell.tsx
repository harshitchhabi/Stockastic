import { useEffect, useState } from "react";
import { useSession } from "@/lib/session";
import { useControlState } from "@/lib/useControlState";
import { useConnection } from "@/lib/useConnection";
import { UniverseProvider, useUniverse } from "@/lib/universe";
import { pagePath, useRoute, type Page } from "@/lib/router";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { PublicConfig } from "@/lib/types";
import { ExplorePage } from "./pages/ExplorePage";
import { WatchlistPage } from "./pages/WatchlistPage";
import { HoldingsPage } from "./pages/HoldingsPage";
import { CompanyPage } from "./pages/CompanyPage";
import { FundsPage } from "./pages/FundsPage";
import { FundDeskPage } from "./pages/FundDeskPage";
import { Leaderboard } from "./Leaderboard";
import { NewsFeed } from "./NewsFeed";
import { ErrorBoundary } from "./ErrorBoundary";

export function DashboardShell() {
  return (
    <UniverseProvider>
      <Shell />
    </UniverseProvider>
  );
}

function Shell() {
  const { account, logout, refresh } = useSession();
  const { tradingFrozen } = useControlState();
  const connection = useConnection();
  const route = useRoute();
  const { starred } = useUniverse();
  const [standingsVisible, setStandingsVisible] = useState(false);
  const [lastTrade, setLastTrade] = useState<number | null>(null);

  // Whether teams may see the standings at all is a rulebook decision, read from the server's public config.
  useEffect(() => {
    api
      .get<PublicConfig>("/api/config")
      .then((c) => setStandingsVisible(c.leaderboard.visibleToParticipants === true))
      .catch(() => {});
  }, []);

  useEffect(() => {
    const socket = getSocket();
    const onTrade = () => {
      setLastTrade(Date.now());
      void refresh(); // the cash in the header changes with every trade
    };
    // The funds are formed at the start of Phase 2: qualifying teams become fund managers, so reload who we are.
    const onRole = () => void refresh();
    socket.on("trade", onTrade);
    socket.on("portfolio", onRole);
    socket.on("fundsFormed", onRole);
    socket.on("connect", onRole);
    return () => {
      socket.off("trade", onTrade);
      socket.off("portfolio", onRole);
      socket.off("fundsFormed", onRole);
      socket.off("connect", onRole);
    };
  }, [refresh]);

  if (!account) return null;

  const nav: { page: Page; label: string }[] = [
    { page: "explore", label: "Explore" },
    { page: "watchlist", label: `Watchlist${starred.size > 0 ? ` (${starred.size})` : ""}` },
    { page: "holdings", label: "Holdings" },
    ...(account.role === "investor" ? [{ page: "funds" as Page, label: "Funds" }] : []),
    ...(account.role === "fund_manager" ? [{ page: "desk" as Page, label: "Fund desk" }] : []),
    ...(standingsVisible ? [{ page: "standings" as Page, label: "Standings" }] : []),
  ];

  // The role decides which pages exist; anything else falls back to Explore.
  const allowed = new Set<Page>(["explore", "watchlist", "holdings", "company", ...nav.map((n) => n.page)]);
  const page: Page = allowed.has(route.page) ? route.page : "explore";
  const activeNav: Page = page === "company" ? "explore" : page;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <header className="masthead">
        <div className="brand">
          Stockastic
        </div>
        <nav className="nav" aria-label="Pages">
          {nav.map((n) => (
            <a key={n.page} href={pagePath(n.page)} aria-current={activeNav === n.page ? "page" : undefined}>
              {n.label}
            </a>
          ))}
        </nav>
        {tradingFrozen && <span className="chip alert">Trading frozen</span>}
        <span style={{ marginLeft: "auto" }} className="dim">
          {account.displayName} <span className="label">· {account.role.replace("_", " ")}</span>
        </span>
        <span>
          <span className="label">{account.role === "fund_manager" ? "Fund cash " : "Cash "}</span>
          <span className="mono">{account.cashBalance.toFixed(2)}</span>
        </span>
        {account.isAdmin && <a href="/admin">Console</a>}
        <button onClick={logout}>Sign out</button>
      </header>

      {connection !== "open" && (
        <div role="status" className="banner">
          {connection === "unauthenticated"
            ? "Your session is no longer valid. Please sign in again."
            : "Live connection lost, reconnecting. Prices may be out of date until it returns. A trade you send is safe to retry: it can never happen twice."}
        </div>
      )}

      <div className="body">
        <main className="main">
          <ErrorBoundary name={page}>
            {page === "explore" && <ExplorePage />}
            {page === "watchlist" && <WatchlistPage />}
            {page === "holdings" && <HoldingsPage />}
            {page === "company" && route.symbol && <CompanyPage key={route.symbol} symbol={route.symbol} tradingFrozen={tradingFrozen} />}
            {page === "funds" && <FundsPage />}
            {page === "desk" && <FundDeskPage />}
            {page === "standings" && (
              <div className="page">
                <Leaderboard />
              </div>
            )}
          </ErrorBoundary>
        </main>

        <aside className="rail">
          <ErrorBoundary name="News">
            <NewsFeed title={account.role === "fund_manager" ? "The wire · early" : "The wire"} />
          </ErrorBoundary>
        </aside>
      </div>

      <footer className="statusbar">
        <span>
          <i className={`dot ${connection === "open" ? "" : "off"}`} />
          {connection === "open" ? "Live" : "Reconnecting"}
        </span>
        <span>Last trade {lastTrade ? new Date(lastTrade).toLocaleTimeString() : "—"}</span>
      </footer>
    </div>
  );
}
