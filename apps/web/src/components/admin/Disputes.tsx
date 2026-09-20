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

function Queue({ title, tickets, run, now }: { title: string; tickets: Ticket[]; run: ReturnType<typeof useDo>; now: number }) {
  return (
    <>
      <h2 className="section">{title} <span className="dim mono" style={{ fontSize: 13 }}>{tickets.length}</span></h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>Team</th>
            <th>Issue</th>
            <th>Raised</th>
            <th>Due</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {tickets.map((t) => {
            const left = t.dueBy ? t.dueBy - now : null;
            return (
              <tr key={t.id}>
                <td>
                  <strong>{t.accountName}</strong>
                  <div className="dim" style={{ fontSize: 11 }}>dispute {t.sequence} this phase</div>
                </td>
                <td style={{ textAlign: "left", fontFamily: "var(--sans)", maxWidth: 380 }}>
                  <span className="label">{CATEGORY[t.category] ?? t.category}</span>
                  {t.late && <> <Badge tone="flag">Raised late</Badge></>}
                  {t.platformWide && <> <Badge tone="down">Platform wide</Badge></>}
                  <div>{t.summary}</div>
                </td>
                <td className="dim" style={{ fontFamily: "var(--sans)" }}>{ago(t.raisedAt, now)}</td>
                <td>
                  {left === null ? <span className="dim">no target</span> : left <= 0 ? <span className="down">overdue</span> : <span className={left < 120000 ? "down" : ""}>{fmtMinSec(left)}</span>}
                </td>
                <td>
                  <span className="btn-row" style={{ justifyContent: "flex-end", margin: 0 }}>
                    {!t.platformWide && t.queue === "standard" && (
                      <ActionButton
                        label="Mark platform wide"
                        title="Mark this as a platform-wide issue"
                        description="Moves it to the expedited queue whatever the team's allowance."
                        run={(reason) => run(`/api/admin/disputes/${t.id}/triage`, { reason, platformWide: true }, "Marked platform wide")}
                      />
                    )}
                    <ActionButton
                      className="solid"
                      label="Resolve"
                      title="Resolve this dispute"
                      description="The reason you give is the resolution the team is told."
                      run={(reason) => run(`/api/admin/disputes/${t.id}/resolve`, { reason }, "Dispute resolved")}
                    />
                  </span>
                </td>
              </tr>
            );
          })}
          {tickets.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                none waiting
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </>
  );
}

export function Disputes() {
  const { data, error, at, reload } = usePoll<Ticket[]>("/api/admin/disputes", 5000);
  const run = useDo(reload);
  const now = useNow(1000);
  const open = (data ?? []).filter((t) => t.status === "open");

  const [fillId, setFillId] = useState("");
  const [note, setNote] = useState("");

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Disputes</h1>
      </div>

      <Queue title="Expedited" tickets={open.filter((t) => t.queue === "expedited").sort((a, b) => a.dueBy - b.dueBy)} run={run} now={now} />
      <Queue title="Standard" tickets={open.filter((t) => t.queue === "standard")} run={run} now={now} />

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
          run={(reason) =>
            run("/api/admin/trade-adjustments", { reason, fillId: fillId.trim(), adjustment: { note: note.trim() } }, "Correction recorded").then(() => {
              setFillId("");
              setNote("");
            })
          }
        />
      </div>
    </div>
  );
}
