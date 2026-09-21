import { useState } from "react";
import type { Ticket } from "@/lib/adminTypes";
import { ago, fmtMinSec } from "@/lib/format";
import { ActionButton, Badge, LoadError, useDo, useNow, usePoll } from "./shared";

const CATEGORY: Record<string, string> = {
  incorrect_transaction: "Incorrect transaction",
  missed_announcement: "Missed announcement",
  fund_allocation: "Fund allocation",
  news_release_timing: "News release timing",
  suspected_violation: "Suspected violation",
  final_settlement: "Final settlement",
  other: "Other",
};

export function Disputes() {
  const { data, error, at, reload } = usePoll<Ticket[]>("/api/admin/disputes", 5000);
  const run = useDo(reload);
  const now = useNow(1000);
  const open = (data ?? []).filter((t) => t.status === "open").sort((a, b) => a.dueBy - b.dueBy);

  const [fillId, setFillId] = useState("");
  const [note, setNote] = useState("");

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Disputes</h1>
      </div>

      <h2 className="section">
        Waiting <span className="dim mono" style={{ fontSize: 13 }}>{open.length}</span>
      </h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>Team</th>
            <th>Issue</th>
            <th>Raised</th>
            <th>Decide by</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {open.map((t) => {
            const left = t.dueBy ? t.dueBy - now : null;
            return (
              <tr key={t.id}>
                <td>
                  <strong>{t.accountName}</strong>
                </td>
                <td style={{ textAlign: "left", fontFamily: "var(--sans)", maxWidth: 380 }}>
                  <span className="label">{CATEGORY[t.category] ?? t.category}</span>
                  {t.late && (
                    <>
                      {" "}
                      <Badge tone="flag">Raised late</Badge>
                    </>
                  )}
                  <div>{t.summary}</div>
                </td>
                <td className="dim" style={{ fontFamily: "var(--sans)" }}>{ago(t.raisedAt, now)}</td>
                <td>{left === null ? <span className="dim">no target</span> : left <= 0 ? <span className="down">overdue</span> : <span className={left < 120000 ? "down" : ""}>{fmtMinSec(left)}</span>}</td>
                <td>
                  <ActionButton
                    className="solid"
                    label="Resolve"
                    title="Resolve this dispute"
                    description="Marks this dispute as resolved."
                    run={() => run(`/api/admin/disputes/${t.id}/resolve`, {}, "Dispute resolved")}
                  />
                </td>
              </tr>
            );
          })}
          {open.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                none waiting
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <h2 className="section">Correct a trade</h2>
      <p className="dim" style={{ marginTop: 0 }}>
        Trades are final. A correction is recorded against the trade as an adjustment and the trade itself is never changed.
      </p>
      <div className="stack" style={{ maxWidth: 460 }}>
        <label className="field">
          <span className="label">Trade id</span>
          <input value={fillId} onChange={(e) => setFillId(e.target.value)} />
        </label>
        <label className="field">
          <span className="label">What the correction is</span>
          <input value={note} onChange={(e) => setNote(e.target.value)} />
        </label>
        <ActionButton
          className="solid"
          disabled={!fillId.trim() || !note.trim()}
          label="Record correction"
          title="Record a correction against this trade"
          run={() =>
            run("/api/admin/trade-adjustments", { fillId: fillId.trim(), adjustment: { note: note.trim() } }, "Correction recorded").then(() => {
              setFillId("");
              setNote("");
            })
          }
        />
      </div>
    </div>
  );
}
