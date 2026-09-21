import type { Systems as SystemsData } from "@/lib/adminTypes";
import { ago, fmtUptime } from "@/lib/format";
import { ActionButton, Badge, LoadError, useDo, useNow, usePoll } from "./shared";

function Tile({ label, value, sub, tone }: { label: string; value: React.ReactNode; sub?: string; tone?: "up" | "down" }) {
  return (
    <div className="tile">
      <div className="label">{label}</div>
      <div className={`v ${tone ?? ""}`}>{value}</div>
      {sub && <div className="dim">{sub}</div>}
    </div>
  );
}

/** Is the platform healthy right now, and if a symbol has stopped, bring it back. */
export function Systems() {
  const { data, error, at, reload } = usePoll<SystemsData>("/api/admin/systems", 3000);
  const run = useDo(reload);
  const now = useNow(1000);

  if (!data) return <div className="page"><LoadError error={error} at={at} />{!error && <div className="empty">loading…</div>}</div>;

  const slow = data.commitP99Ms > 250;
  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Systems</h1>
        <Badge tone={data.dbOk && data.halted.length === 0 && data.journalErrors === 0 ? "up" : "down"}>
          {data.dbOk && data.halted.length === 0 && data.journalErrors === 0 ? "All clear" : "Needs attention"}
        </Badge>
        <span className="dim">up {fmtUptime(data.uptimeSec)}</span>
      </div>

      <div className="tiles">
        <Tile label="People connected" value={data.connected} />
        <Tile label="Orders per minute" value={data.ordersPerMin} />
        <Tile label="Trades per minute" value={data.tradesPerMin} />
        <Tile label="Open orders" value={data.openOrders} />
        <Tile label="Database" value={data.dbOk ? "Reachable" : "Down"} tone={data.dbOk ? "up" : "down"} />
        <Tile label="Order save time (median)" value={`${data.commitP50Ms} ms`} />
        <Tile label="Order save time (slowest 1%)" value={`${data.commitP99Ms} ms`} tone={slow ? "down" : undefined} sub={slow ? "slower than usual" : undefined} />
        <Tile label="Failed saves" value={data.journalErrors} tone={data.journalErrors > 0 ? "down" : "up"} sub="orders refused rather than lost" />
      </div>

      <h2 className="section">Stopped symbols</h2>
      <p className="dim" style={{ marginTop: 0 }}>
        If a symbol’s matching hits an unexpected fault it stops on its own, and every other symbol keeps trading. Resuming rebuilds it from the saved orders.
        {` ${data.symbolsTotal} symbols in total.`}
      </p>
      <table className="roomy">
        <thead>
          <tr>
            <th>Symbol</th>
            <th>Stopped</th>
            <th>Reason</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {data.halted.map((h) => (
            <tr key={h.symbol}>
              <td>
                <strong>{h.symbol}</strong>
              </td>
              <td>{ago(h.since, now)}</td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left" }}>{h.reason}</td>
              <td>
                <ActionButton
                  className="solid"
                  label="Resume"
                  title={`Resume ${h.symbol}`}
                  description="Rebuilds the order book from the saved orders, then accepts orders again."
                  run={() => run(`/api/admin/symbols/${encodeURIComponent(h.symbol)}/resume`, {}, `${h.symbol} resumed`)}
                />
              </td>
            </tr>
          ))}
          {data.halted.length === 0 && (
            <tr>
              <td colSpan={4} className="empty">
                every symbol is trading
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <h2 className="section">Recent server errors</h2>
      <table className="roomy">
        <tbody>
          {data.recentErrors.map((e, i) => (
            <tr key={i}>
              <td style={{ width: 110, whiteSpace: "nowrap" }} className="dim">
                {ago(e.at, now)}
              </td>
              <td style={{ fontFamily: "var(--mono)", textAlign: "left" }}>{e.message}</td>
            </tr>
          ))}
          {data.recentErrors.length === 0 && (
            <tr>
              <td className="empty">no errors</td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
