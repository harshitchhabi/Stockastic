import { usePoll } from "./shared";

type Point = { at: number; cash: number; value: number };
type Act = { at: number; type: string; ip: string; userAgent: string; detail: string };
type Row = { at: number; type: string; symbol: string; sharesDelta: number; cashDelta: number };
type Past = { available: boolean; behind?: number; wallet?: Point[]; activity?: Act[]; ledger?: Row[] };

const rupees = (paise: number) => (paise / 100).toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const WHAT: Record<string, string> = {
  signup: "Signed up",
  login: "Signed in",
  login_failed: "Wrong password",
  trade_buy: "Bought",
  trade_sell: "Sold",
  grant: "Shares given",
  cash_adjust: "Cash changed by an organiser",
  fund_invest: "Put into a fund",
  fund_redeem: "Taken out of a fund",
};

/** Net worth over time as a plain line, from the once a minute record kept in the database. */
function Line({ points }: { points: Point[] }) {
  if (points.length < 2) return <div className="empty">the wallet is recorded once a minute while the event runs</div>;
  const w = 720;
  const h = 160;
  const t0 = points[0].at;
  const t1 = points[points.length - 1].at;
  const vs = points.map((p) => p.value);
  const lo = Math.min(...vs);
  const hi = Math.max(...vs);
  const x = (t: number) => ((t - t0) / Math.max(1, t1 - t0)) * (w - 8) + 4;
  const y = (v: number) => h - 8 - ((v - lo) / Math.max(1, hi - lo)) * (h - 16);
  const d = points.map((p, i) => `${i ? "L" : "M"}${x(p.at).toFixed(1)},${y(p.value).toFixed(1)}`).join(" ");
  return (
    <div>
      <svg viewBox={`0 0 ${w} ${h}`} width="100%" height={h} role="img" aria-label="Net worth over time" preserveAspectRatio="none">
        <path d={d} fill="none" stroke="currentColor" strokeWidth="2" vectorEffect="non-scaling-stroke" />
      </svg>
      <div className="meta">
        <span className="dim">Lowest {rupees(lo)}</span>
        <span className="dim">Highest {rupees(hi)}</span>
        <span className="dim">{points.length} points, from {new Date(t0).toLocaleTimeString()} to {new Date(t1).toLocaleTimeString()}</span>
      </div>
    </div>
  );
}

/** What the database kept about one team: wallet over time, what they did, every change to cash and shares. */
export function TeamRecord({ id }: { id: string }) {
  const { data } = usePoll<Past>(`/api/admin/accounts/${encodeURIComponent(id)}/history`, 20000);
  if (!data) return null;
  if (!data.available) {
    return (
      <>
        <h2 className="section">Record</h2>
        <div className="empty">The database is not switched on, so wallet history and sign-in records are not kept.</div>
      </>
    );
  }
  const acts = data.activity ?? [];
  const rows = data.ledger ?? [];
  return (
    <>
      <h2 className="section">Net worth over time</h2>
      <Line points={data.wallet ?? []} />

      <h2 className="section">Sign-ins and activity</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>When</th>
            <th>What</th>
            <th>Address</th>
            <th>Browser</th>
          </tr>
        </thead>
        <tbody>
          {acts.map((a, i) => (
            <tr key={i}>
              <td style={{ whiteSpace: "nowrap", fontFamily: "var(--mono)" }}>{new Date(a.at).toLocaleString()}</td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left" }}>
                {WHAT[a.type] ?? a.type}
                {a.detail ? ` (${a.detail})` : ""}
              </td>
              <td style={{ fontFamily: "var(--mono)" }}>{a.ip}</td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left", maxWidth: 320, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={a.userAgent}>
                {a.userAgent}
              </td>
            </tr>
          ))}
          {acts.length === 0 && (
            <tr>
              <td colSpan={4} className="empty">
                nothing recorded yet
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <h2 className="section">Every change to cash and shares</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>When</th>
            <th>What</th>
            <th>Company</th>
            <th>Shares</th>
            <th>Cash</th>
          </tr>
        </thead>
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              <td style={{ whiteSpace: "nowrap", fontFamily: "var(--mono)" }}>{new Date(r.at).toLocaleTimeString()}</td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left" }}>{WHAT[r.type] ?? r.type}</td>
              <td>{r.symbol}</td>
              <td className={r.sharesDelta >= 0 ? "up" : "down"}>{r.sharesDelta === 0 ? "" : `${r.sharesDelta > 0 ? "+" : "−"}${Math.abs(r.sharesDelta)}`}</td>
              <td className={r.cashDelta >= 0 ? "up" : "down"}>{r.cashDelta === 0 ? "" : `${r.cashDelta > 0 ? "+" : "−"}${rupees(Math.abs(r.cashDelta))}`}</td>
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                nothing recorded yet
              </td>
            </tr>
          )}
        </tbody>
      </table>
      {data.behind ? <div className="dim">{data.behind} newest records are still being filed and will appear shortly.</div> : null}
    </>
  );
}
