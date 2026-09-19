"use client";

import { useState } from "react";
import { useSession } from "@/lib/session";
import { useControlState } from "@/lib/useControlState";
import { Watchlist } from "./Watchlist";
import { OrderBookLadder } from "./OrderBookLadder";
import { OrderTicket } from "./OrderTicket";
import { PriceChart } from "./PriceChart";
import { Portfolio } from "./Portfolio";
import { Leaderboard } from "./Leaderboard";
import { FundBrowser } from "./FundBrowser";
import { FundManagerPanel } from "./FundManagerPanel";
import { NewsFeed } from "./NewsFeed";

const DEFAULT_SYMBOL = "ACME";

export function DashboardShell() {
  const { account, logout } = useSession();
  const { tradingFrozen } = useControlState();
  const [symbol, setSymbol] = useState(DEFAULT_SYMBOL);

  if (!account) return null;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100vh", padding: 8, gap: 8 }}>
      <div
        className="panel"
        style={{ flexDirection: "row", alignItems: "center", padding: "8px 12px", gap: 16, flexShrink: 0 }}
      >
        <strong>Stockastic</strong>
        <span style={{ color: "var(--text-dim)" }}>
          {account.displayName} · {account.role}
          {account.isAdmin ? " · admin" : ""}
        </span>
        {tradingFrozen && (
          <span
            style={{
              background: "var(--red)",
              color: "#210608",
              fontWeight: 700,
              padding: "2px 8px",
              borderRadius: 4,
              fontSize: 11,
              letterSpacing: "0.04em",
            }}
          >
            TRADING FROZEN
          </span>
        )}
        <span className="mono" style={{ marginLeft: "auto" }}>
          cash {account.cashBalance.toFixed(2)}
        </span>
        {account.isAdmin && <a href="/admin">Admin console</a>}
        <button onClick={logout}>Sign out</button>
      </div>

      <div
        style={{
          display: "grid",
          gridTemplateColumns: "220px 1fr 280px",
          gridTemplateRows: "1fr 220px",
          gap: 8,
          flex: 1,
          minHeight: 0,
        }}
      >
        <div style={{ gridRow: "1 / 3" }}>
          <Watchlist selected={symbol} onSelect={setSymbol} />
        </div>

        <div style={{ display: "flex", flexDirection: "column", gap: 8, minHeight: 0 }}>
          <PriceChart symbol={symbol} />
          <div style={{ flex: 1, minHeight: 0 }}>
            <OrderBookLadder symbol={symbol} />
          </div>
        </div>

        <div style={{ gridRow: "1 / 3" }}>
          <OrderTicket symbol={symbol} tradingFrozen={tradingFrozen} />
        </div>

        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8 }}>
          <Portfolio />
          <Leaderboard />
        </div>
      </div>

      {account.role === "investor" && (
        <div style={{ height: 220, flexShrink: 0 }}>
          <FundBrowser />
        </div>
      )}

      {account.role === "fund_manager" && (
        <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 8, height: 220, flexShrink: 0 }}>
          <FundManagerPanel />
          <NewsFeed title="Fund Manager News (early feed)" />
        </div>
      )}
    </div>
  );
}
