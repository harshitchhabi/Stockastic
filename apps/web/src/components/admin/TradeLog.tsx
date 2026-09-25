import { useState } from "react";
import type { AdminTrade } from "@/lib/adminTypes";
import { ago } from "@/lib/format";
import { downloadCsv } from "./download";
import { LoadError, useAdmin, useNow, usePoll } from "./shared";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** Every trade of the event, newest first. Filter by company or team, and export the lot. */
export function TradeLog() {
  const { notify } = useAdmin();
  const [symbol, setSymbol] = useState("");
  const [team, setTeam] = useState("");
  const q = new URLSearchParams({ limit: "300" });
  if (symbol.trim()) q.set("symbol", symbol.trim().toUpperCase());
  const { data, error, at } = usePoll<AdminTrade[]>(`/api/admin/trades?${q.toString()}`, 3000);
  const now = useNow(1000);
  const rows = (data ?? []).filter((t) => !team.trim() || t.team.toLowerCase().includes(team.trim().toLowerCase()));

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Trades</h1>
        <span className="dim">latest {rows.length}</span>
        <span style={{ marginLeft: "auto" }}>
          <button onClick={() => void downloadCsv("/api/admin/export/trades.csv", "trades.csv").catch((e) => notify(String(e.message ?? e), false))}>Export every trade</button>
        </span>
      </div>
      <div className="toolbar">
        <input className="search" placeholder="Company symbol" aria-label="Company symbol" value={symbol} onChange={(e) => setSymbol(e.target.value)} />
        <input className="search" placeholder="Team name" aria-label="Team name" value={team} onChange={(e) => setTeam(e.target.value)} />
      </div>
      <table className="roomy">
        <thead>
          <tr>
            <th>When</th>
            <th>Team</th>
            <th>Company</th>
            <th>Side</th>
            <th>Shares</th>
            <th>Price</th>
            <th>Value</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((t) => (
            <tr key={t.id}>
              <td className="dim" style={{ fontFamily: "var(--sans)" }}>{ago(t.at, now)}</td>
              <td>
                {t.accountId.startsWith("fund:") ? <strong>{t.team}</strong> : (
                  <a href={`#/team/${t.accountId}`} className="rowlink">
                    <strong>{t.team}</strong>
                  </a>
                )}
              </td>
              <td>{t.symbol}</td>
              <td className={t.side === "buy" ? "up" : "down"}>{t.side}</td>
              <td>{t.qty}</td>
              <td>{money(t.price)}</td>
              <td>{money(t.value)}</td>
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td colSpan={7} className="empty">
                no trades yet
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
