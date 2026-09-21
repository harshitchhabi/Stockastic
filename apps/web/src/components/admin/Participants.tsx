import { useMemo, useState } from "react";
import type { AdminAccount } from "@/lib/adminTypes";
import { ActionButton, Badge, LoadError, useDo, usePoll } from "./shared";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
type RoleFilter = "all" | "investor" | "fund_manager";

/** Everyone in the event. Warn, disqualify or promote a team, each with a recorded reason. */
export function Participants() {
  const { data, error, at, reload } = usePoll<AdminAccount[]>("/api/admin/accounts", 10000);
  const run = useDo(reload);
  const [query, setQuery] = useState("");
  const [role, setRole] = useState<RoleFilter>("all");

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data ?? [])
      .filter((a) => role === "all" || a.role === role)
      .filter((a) => !q || a.displayName.toLowerCase().includes(q) || a.email.toLowerCase().includes(q))
      .sort((a, b) => b.portfolioValue - a.portfolioValue);
  }, [data, query, role]);

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Participants</h1>
        <span className="dim">{data ? `${rows.length} of ${data.length}` : "loading…"}</span>
      </div>

      <div className="toolbar">
        <div className="tabs inline" role="tablist">
          {(
            [
              ["all", "Everyone"],
              ["investor", "Investors"],
              ["fund_manager", "Fund managers"],
            ] as [RoleFilter, string][]
          ).map(([id, label]) => (
            <button key={id} role="tab" aria-selected={role === id} onClick={() => setRole(id)}>
              {label}
            </button>
          ))}
        </div>
        <input type="search" className="search" placeholder="Search name or email" aria-label="Search participants" value={query} onChange={(e) => setQuery(e.target.value)} />
      </div>

      <table className="roomy">
        <thead>
          <tr>
            <th>Team</th>
            <th>Role</th>
            <th>Status</th>
            <th>Portfolio value</th>
            <th>Cash</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((a) => (
            <tr key={a.id}>
              <td>
                <a href={`#/team/${a.id}`} className="rowlink">
                  <strong>{a.displayName}</strong>
                </a>
                <div className="dim" style={{ fontSize: 11 }}>{a.email}</div>
              </td>
              <td style={{ fontFamily: "var(--sans)" }}>{a.role === "fund_manager" ? "Fund manager" : "Investor"}</td>
              <td style={{ fontFamily: "var(--sans)" }}>
                {a.status === "active" && <Badge tone="up">Active</Badge>}
                {a.status === "warned" && <Badge tone="flag">Warned{a.warnings > 1 ? ` ×${a.warnings}` : ""}</Badge>}
                {a.status === "disqualified" && <Badge tone="down">Disqualified</Badge>}
              </td>
              <td>{money(a.portfolioValue)}</td>
              <td>{money(a.cashBalance)}</td>
              <td>
                <span className="btn-row" style={{ justifyContent: "flex-end", margin: 0 }}>
                  {a.role === "investor" && a.status !== "disqualified" && (
                    <ActionButton
                      label="Promote"
                      title={`Promote ${a.displayName} to fund manager`}
                      description="Moves this team into the fund manager role."
                      run={(reason) => run(`/api/admin/accounts/${a.id}/promote`, { reason }, `${a.displayName} promoted`)}
                    />
                  )}
                  {a.status !== "disqualified" && (
                    <ActionButton
                      label="Warn"
                      title={`Issue a formal warning to ${a.displayName}`}
                      run={(reason) => run(`/api/admin/accounts/${a.id}/warn`, { reason }, `${a.displayName} warned`)}
                    />
                  )}
                  {a.status !== "disqualified" && (
                    <ActionButton
                      label="Disqualify"
                      danger
                      title={`Disqualify ${a.displayName}`}
                      description="Removes this team from the event. If it runs a fund, that fund is frozen at its current NAV."
                      run={(reason) => run(`/api/admin/accounts/${a.id}/disqualify`, { reason }, `${a.displayName} disqualified`)}
                    />
                  )}
                </span>
              </td>
            </tr>
          ))}
          {data && rows.length === 0 && (
            <tr>
              <td colSpan={6} className="empty">
                no one matches
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
