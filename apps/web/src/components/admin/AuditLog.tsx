import { useMemo, useState } from "react";
import type { AuditEntry } from "@/lib/adminTypes";
import { Badge, LoadError, usePoll } from "./shared";

/** Every organiser action, newest first: who did what, to what, and why. Read-only. */
export function AuditLog() {
  const { data, error, at } = usePoll<AuditEntry[]>("/api/admin/audit", 5000);
  const [query, setQuery] = useState("");

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data ?? [])
      .filter((e) => !q || `${e.actor} ${e.action} ${e.target} ${e.reason}`.toLowerCase().includes(q))
      .sort((a, b) => b.at - a.at);
  }, [data, query]);

  const anyReason = (data ?? []).some((e) => e.reason);

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Audit log</h1>
        <span className="dim">{data ? `${rows.length} of ${data.length} entries` : "loading…"}</span>
      </div>
      <div className="toolbar" style={{ justifyContent: "flex-end" }}>
        <input type="search" className="search" placeholder="Search who or what" aria-label="Search the audit log" value={query} onChange={(e) => setQuery(e.target.value)} />
      </div>
      <table className="roomy">
        <thead>
          <tr>
            <th>When</th>
            <th>Who</th>
            <th>Action</th>
            <th>Target</th>
            {anyReason && <th>Note</th>}
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((e) => (
            <tr key={e.id}>
              <td className="mono" style={{ whiteSpace: "nowrap", fontFamily: "var(--mono)" }}>
                {new Date(e.at).toLocaleTimeString()}
              </td>
              <td style={{ fontFamily: "var(--sans)" }}>{e.actor}</td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left" }}>{e.action}</td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left" }}>{e.target}</td>
              {anyReason && <td style={{ fontFamily: "var(--serif)", fontStyle: "italic", textAlign: "left" }}>{e.reason}</td>}
              <td>{e.ok ? null : <Badge tone="down">Failed</Badge>}</td>
            </tr>
          ))}
          {data && rows.length === 0 && (
            <tr>
              <td colSpan={anyReason ? 6 : 5} className="empty">
                nothing recorded
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
