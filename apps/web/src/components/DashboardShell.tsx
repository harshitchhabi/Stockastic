import { useEffect, useState } from "react";
import { useSession } from "@/lib/session";
import { useControlState } from "@/lib/useControlState";
import { useConnection, useConnectionProblem } from "@/lib/useConnection";
import { UniverseProvider, useUniverse } from "@/lib/universe";
import { pagePath, useRoute, type Page } from "@/lib/router";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { PublicConfig, Trade } from "@/lib/types";
import { ExplorePage } from "./pages/ExplorePage";
import { WatchlistPage } from "./pages/WatchlistPage";
import { HoldingsPage } from "./pages/HoldingsPage";
import { CompanyPage } from "./pages/CompanyPage";
import { FundsPage } from "./pages/FundsPage";
import { FundDeskPage } from "./pages/FundDeskPage";
import { TeamPage } from "./pages/TeamPage";
import { RulesPage } from "./pages/RulesPage";
import { HelpPage } from "./pages/HelpPage";
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
  const { tradingFrozen, stage = "", marketOpen, openWindow = -1, paused = false, standingsOpen = false } = useControlState();
  const phaseText = stage === "" ? "Not started" : stage === "phase1" ? "Phase 1" : stage === "closing" ? "Event closed" : "Phase 2";
  const phaseDetail = paused ? "paused" : stage === "closing" ? "" : marketOpen ? "market open" : openWindow >= 0 ? `allocation window ${openWindow} open` : "market closed";
  const connection = useConnectionProblem();
  const live = useConnection();
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

  // The team's most recent trade, whoever in the team placed it and whenever this page was opened.
  useEffect(() => {
    api
      .get<Trade[]>("/api/trades/mine")
      .then((ts) => setLastTrade(ts.reduce<number | null>((m, t) => (m == null || t.timestamp > m ? t.timestamp : m), null)))
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
    // The organiser reset the whole event: reload so every screen starts clean.
    const onReset = () => window.location.reload();
    socket.on("eventReset", onReset);
    // The server refused this sign-in (signed out by an organiser, or the event started fresh): check with the
    // server, and if it is no longer valid the app goes back to the sign-in screen.
    const onRefused = () => void api.get("/api/auth/me").catch(() => {});
    socket.on("unauthenticated", onRefused);
    socket.on("connect", onRole);
    return () => {
      socket.off("eventReset", onReset);
      socket.off("unauthenticated", onRefused);
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
    ...(standingsVisible || standingsOpen ? [{ page: "standings" as Page, label: "Standings" }] : []),
    ...(account.teamSize > 1 && !account.isAdmin ? [{ page: "team" as Page, label: "Team" }] : []),
    { page: "rules", label: "Rules" },
    ...(!account.isAdmin ? [{ page: "help" as Page, label: "Help Desk" }] : []),
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
        <span className="chip">
          {phaseText}
          {phaseDetail && ` · ${phaseDetail}`}
        </span>
        {tradingFrozen && <span className="chip alert">Trading frozen</span>}
        <span style={{ marginLeft: "auto" }} className="dim">
          {account.displayName}
          {!account.isLeader && <span> · {account.loginName}</span>} <span className="label">· {account.role.replace("_", " ")}</span>
          {!account.canTrade && <span className="label"> · watching</span>}
        </span>
        <span>
          <span className="label">{account.role === "fund_manager" ? "Fund cash " : "Cash "}</span>
          <span className="mono">{account.cashBalance.toFixed(2)}</span>
        </span>
        {account.isAdmin && <a href="/admin">Console</a>}
        <button onClick={logout}>Sign out</button>
      </header>

      {paused && (
        <div role="status" className="banner">
          The event is paused for a break. Prices are frozen, and nothing can be bought, sold or moved into or out of funds until the organisers resume.
        </div>
      )}
      {connection && (
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
            {page === "team" && <TeamPage />}
            {page === "rules" && <RulesPage />}
            {page === "help" && <HelpPage />}
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
          <i className={`dot ${live === "open" ? "" : "off"}`} />
          {live === "open" ? "Live" : live === "connecting" ? "Connecting" : "Reconnecting"}
        </span>
        <span>Last trade {lastTrade ? new Date(lastTrade).toLocaleTimeString() : "—"}</span>
      </footer>
    </div>
  );
}
