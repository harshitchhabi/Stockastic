import { useState } from "react";
import { LoadError, usePoll } from "./shared";

type TeamRow = { rank: number; accountId: string; team: string; role: string; status: string; value: number; returnPct: number; trades: number };
type FundRow = {
  rank: number;
  fundId: string;
  name: string;
  managers: string[];
  nav: number;
  returnPct: number;
  aum: number;
  investors: number;
  feePercent: number;
  status: string;
};
type Standings = { teams: TeamRow[]; investors: TeamRow[]; funds: FundRow[] };

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const pct = (n: number) => `${n >= 0 ? "+" : "−"}${Math.abs(n).toFixed(2)}%`;

/** A cell a spreadsheet will never run as a formula. */
function csvCell(v: string | number): string {
  let s = String(v);
  if (/^[=+\-@\t\r]/.test(s)) s = "'" + s;
  return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s;
}
function download(name: string, header: string[], rows: (string | number)[][]) {
  const csv = [header, ...rows].map((r) => r.map(csvCell).join(",")).join("\n");
  const url = URL.createObjectURL(new Blob([csv], { type: "text/csv" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = name;
  a.click();
  URL.revokeObjectURL(url);
}

const TABS = [
  { id: "teams", label: "All teams" },
  { id: "investors", label: "Investors" },
  { id: "funds", label: "Funds" },
] as const;

/** Live standings for the organisers: every team, the investors on their own, and the funds. Prizes are decided by
 * the organisers from these. */
export function AdminStandings() {
  const { data, error, at } = usePoll<Standings>("/api/admin/standings", 10000);
  const [tab, setTab] = useState<(typeof TABS)[number]["id"]>("teams");
  if (!data) return <div className="page"><LoadError error={error} at={at} />{!error && <div className="empty">loading…</div>}</div>;

  const teamRows = tab === "investors" ? data.investors : data.teams;
  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Standings</h1>
        <span className="dim">Live. Players see standings only during Phase 1.</span>
      </div>
      <div className="seg" role="group" aria-label="Which standings" style={{ marginBottom: 16 }}>
        {TABS.map((t) => (
          <button key={t.id} aria-pressed={tab === t.id} onClick={() => setTab(t.id)}>
            {t.label} ({t.id === "funds" ? data.funds.length : t.id === "investors" ? data.investors.length : data.teams.length})
          </button>
        ))}
      </div>

      {tab !== "funds" ? (
        <>
          <button
            onClick={() =>
              download(
                `standings-${tab}.csv`,
                ["Rank", "Team", "Role", "Status", "Value", "Return %", "Trades"],
                teamRows.map((r) => [r.rank, r.team, r.role === "fund_manager" ? "Fund manager" : "Investor", r.status, r.value.toFixed(2), r.returnPct.toFixed(2), r.trades])
              )
            }
          >
            Download CSV
          </button>
          <table className="roomy">
            <thead>
              <tr>
                <th>#</th>
                <th style={{ textAlign: "left" }}>Team</th>
                {tab === "teams" && <th>Role</th>}
                <th>Value</th>
                <th>Return</th>
                <th>Trades</th>
              </tr>
            </thead>
            <tbody>
              {teamRows.map((r) => (
                <tr key={r.accountId}>
                  <td className="rank">{r.rank}</td>
                  <td style={{ textAlign: "left" }}>
                    <a href={`#/team/${r.accountId}`}>{r.team}</a>
                    {r.status !== "active" && <span className="label"> · {r.status}</span>}
                  </td>
                  {tab === "teams" && <td className="dim">{r.role === "fund_manager" ? "Fund manager" : "Investor"}</td>}
                  <td className="mono">{money(r.value)}</td>
                  <td className={`mono ${r.returnPct >= 0 ? "up" : "down"}`}>{pct(r.returnPct)}</td>
                  <td className="mono">{r.trades}</td>
                </tr>
              ))}
              {teamRows.length === 0 && (
                <tr>
                  <td colSpan={6} className="empty">
                    no teams yet
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </>
      ) : (
        <>
          <button
            onClick={() =>
              download(
                "standings-funds.csv",
                ["Rank", "Fund", "Name", "Managers", "Unit price", "Return %", "Money managed", "Investors", "Fee %", "Status"],
                data.funds.map((f) => [f.rank, f.fundId, f.name, f.managers.join(" + "), f.nav.toFixed(4), f.returnPct.toFixed(2), f.aum.toFixed(2), f.investors, f.feePercent, f.status])
              )
            }
          >
            Download CSV
          </button>
          <table className="roomy">
            <thead>
              <tr>
                <th>#</th>
                <th style={{ textAlign: "left" }}>Fund</th>
                <th>Unit price</th>
                <th>Return</th>
                <th>Money managed</th>
                <th>Investors</th>
                <th>Fee</th>
              </tr>
            </thead>
            <tbody>
              {data.funds.map((f) => (
                <tr key={f.fundId}>
                  <td className="rank">{f.rank}</td>
                  <td style={{ textAlign: "left" }}>
                    <strong>{f.name}</strong> <span className="label">{f.fundId}</span>
                    {f.status !== "active" && <span className="label"> · {f.status}</span>}
                    <div className="dim">{f.managers.join(" · ")}</div>
                  </td>
                  <td className="mono">{money(f.nav)}</td>
                  <td className={`mono ${f.returnPct >= 0 ? "up" : "down"}`}>{pct(f.returnPct)}</td>
                  <td className="mono">{money(f.aum)}</td>
                  <td className="mono">{f.investors}</td>
                  <td className="mono">{f.feePercent}%</td>
                </tr>
              ))}
              {data.funds.length === 0 && (
                <tr>
                  <td colSpan={7} className="empty">
                    the funds have not been formed yet
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </>
      )}
    </div>
  );
}
