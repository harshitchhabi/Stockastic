import { useMemo, useState } from "react";
import type { AdminAccount } from "@/lib/adminTypes";
import { LoadError, usePoll } from "./shared";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** Every team's wallet in one table. Open a team to change its cash or holdings. */
export function Wallets() {
  const { data, error, at } = usePoll<AdminAccount[]>("/api/admin/accounts", 6000);
  const [query, setQuery] = useState("");

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data ?? []).filter((a) => !q || a.displayName.toLowerCase().includes(q) || a.email.toLowerCase().includes(q));
  }, [data, query]);

  const totals = useMemo(() => {
    const all = data ?? [];
    return {
      cash: all.reduce((s, a) => s + a.cashBalance, 0),
      held: all.reduce((s, a) => s + (a.portfolioValue - a.cashBalance), 0),
      worth: all.reduce((s, a) => s + a.portfolioValue, 0),
    };
  }, [data]);

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Wallets</h1>
        <span className="dim">{data ? `${rows.length} of ${data.length} teams` : "loading…"}</span>
      </div>

      <div className="figures">
        <div className="figure">
          <div className="label">Cash in play</div>
          <div className="v">{money(totals.cash)}</div>
        </div>
        <div className="figure">
          <div className="label">Held in shares</div>
          <div className="v">{money(totals.held)}</div>
        </div>
        <div className="figure">
          <div className="label">Total net worth</div>
          <div className="v">{money(totals.worth)}</div>
        </div>
      </div>

      <div className="toolbar" style={{ justifyContent: "flex-end" }}>
        <input type="search" className="search" placeholder="Search team or email" aria-label="Search teams" value={query} onChange={(e) => setQuery(e.target.value)} />
      </div>

      <table className="roomy">
        <thead>
          <tr>
            <th>Team</th>
            <th>Cash</th>
            <th>In shares</th>
            <th>Net worth</th>
            <th>Positions</th>
          </tr>
        </thead>
        <tbody>
          {[...rows]
            .sort((a, b) => b.portfolioValue - a.portfolioValue)
            .map((a) => (
              <tr key={a.id}>
                <td>
                  <span className={`dot-status ${a.online ? "on" : ""}`} title={a.online ? "Online" : "Offline"} />
                  <a href={`#/team/${a.id}`} className="rowlink">
                    <strong>{a.displayName}</strong>
                  </a>
                  {a.status === "disqualified" && <span className="label"> disqualified</span>}
                  {a.locked && <span className="label"> locked</span>}
                </td>
                <td>{money(a.cashBalance)}</td>
                <td>{money(a.portfolioValue - a.cashBalance)}</td>
                <td>{money(a.portfolioValue)}</td>
                <td>{a.positions}</td>
              </tr>
            ))}
          {data && rows.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                no team matches
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
