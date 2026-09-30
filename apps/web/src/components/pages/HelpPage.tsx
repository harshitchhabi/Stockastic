import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";

type Report = {
  id: string;
  category: string;
  summary: string;
  raisedAt: number;
  incidentAt: number;
  late: boolean;
  status: string;
  resolution?: string;
};

const CATEGORIES: { value: string; label: string }[] = [
  { value: "incorrect_transaction", label: "A trade or payment looks wrong" },
  { value: "fund_allocation", label: "Money into or out of a fund" },
  { value: "missed_announcement", label: "I missed an announcement" },
  { value: "news_release_timing", label: "News arrived at the wrong time" },
  { value: "suspected_violation", label: "Someone may be breaking the rules" },
  { value: "final_settlement", label: "My final result" },
  { value: "other", label: "Something else" },
];
const labelOf = (v: string) => CATEGORIES.find((c) => c.value === v)?.label ?? v;

/** The Help Desk: report a problem to the organisers, and see what they decided. */
export function HelpPage() {
  const [reports, setReports] = useState<Report[]>([]);
  const [category, setCategory] = useState("");
  const [summary, setSummary] = useState("");
  const [when, setWhen] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);

  const load = useCallback(() => {
    api
      .get<Report[]>("/api/disputes/mine")
      .then((r) => setReports(r ?? []))
      .catch(() => {});
  }, []);
  useEffect(load, [load]);
  useEffect(() => {
    const t = setInterval(load, 30_000); // the organisers' decisions appear without a refresh
    return () => clearInterval(t);
  }, [load]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (busy) return;
    setMessage(null);
    if (!category) return setMessage({ ok: false, text: "Choose what the problem is about first." });
    if (summary.trim().length < 10) return setMessage({ ok: false, text: "Describe what happened in at least 10 characters." });
    let incidentAt: number | undefined;
    if (when) {
      const [h, m] = when.split(":").map(Number);
      const d = new Date();
      d.setHours(h, m, 0, 0);
      incidentAt = d.getTime();
    }
    setBusy(true);
    try {
      await api.post("/api/disputes", { category, summary: summary.trim(), incidentAt });
      setSummary("");
      setWhen("");
      setCategory("");
      setMessage({ ok: true, text: "Sent to the organisers. Their decision will appear below." });
      load();
    } catch (err) {
      setMessage({ ok: false, text: err instanceof Error ? err.message : "That did not send. Try again." });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="page" style={{ maxWidth: 820 }}>
      <div className="page-head">
        <h1>Help Desk</h1>
      </div>
      <p className="dim">
        Something wrong? Tell the organisers here as soon as you can. A screenshot showing the time helps: keep it to show them. Their decision is final.
      </p>
      <form onSubmit={submit} className="stack" style={{ maxWidth: 640 }}>
        <label className="field">
          <span className="label">What is it about?</span>
          <select value={category} onChange={(e) => setCategory(e.target.value)}>
            <option value="">Choose…</option>
            {CATEGORIES.map((c) => (
              <option key={c.value} value={c.value}>
                {c.label}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="label">What happened?</span>
          <textarea rows={4} maxLength={1000} value={summary} onChange={(e) => setSummary(e.target.value)} placeholder="What you did, what you expected, and what happened instead." />
        </label>
        <label className="field" style={{ maxWidth: 200 }}>
          <span className="label">When did it happen? (optional)</span>
          <input type="time" value={when} onChange={(e) => setWhen(e.target.value)} />
        </label>
        <div>
          <button type="submit" className="solid" disabled={busy}>
            {busy ? "Sending…" : "Send to the organisers"}
          </button>
        </div>
        {message && <div className={message.ok ? "up" : "down"}>{message.text}</div>}
      </form>

      <h2 className="section">Your team's reports</h2>
      {reports.length === 0 ? (
        <div className="empty" style={{ textAlign: "left" }}>
          none yet
        </div>
      ) : (
        <table className="roomy">
          <thead>
            <tr>
              <th>Sent</th>
              <th>About</th>
              <th>What happened</th>
              <th>Status</th>
            </tr>
          </thead>
          <tbody>
            {reports.map((r) => (
              <tr key={r.id}>
                <td className="mono">{new Date(r.raisedAt).toLocaleTimeString()}</td>
                <td>{labelOf(r.category)}</td>
                <td>{r.summary}</td>
                <td>
                  {r.status === "open" ? <span className="chip">Waiting for the organisers</span> : <span className="chip">Decided by the organisers</span>}
                  {r.resolution && <div className="dim">{r.resolution}</div>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
