import type { Systems as SystemsData } from "@/lib/adminTypes";
import { ago, fmtUptime } from "@/lib/format";
import { Badge, LoadError, useNow, usePoll } from "./shared";

function Tile({ label, value, sub, tone }: { label: string; value: React.ReactNode; sub?: string; tone?: "up" | "down" }) {
  return (
    <div className="tile">
      <div className="label">{label}</div>
      <div className={`v ${tone ?? ""}`}>{value}</div>
      {sub && <div className="dim">{sub}</div>}
    </div>
  );
}

/** Is the platform healthy right now: connections, trade saving, price updates and disk space. */
export function Systems() {
  const { data, error, at } = usePoll<SystemsData>("/api/admin/systems", 3000);
  const now = useNow(1000);

  if (!data) return <div className="page"><LoadError error={error} at={at} />{!error && <div className="empty">loading…</div>}</div>;

  const slow = data.commitP99Ms > 250;
  const healthy = data.dbOk && data.journalErrors === 0 && !data.diskLow;
  const disk = data.diskFreeMb >= 1024 ? `${(data.diskFreeMb / 1024).toFixed(1)} GB` : `${data.diskFreeMb} MB`;
  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Systems</h1>
        <Badge tone={healthy ? "up" : "down"}>{healthy ? "All clear" : "Needs attention"}</Badge>
        <span className="dim">up {fmtUptime(data.uptimeSec)}</span>
      </div>

      <div className="tiles">
        <Tile label="People connected" value={data.connected} />
        <Tile label="Trades per minute" value={data.tradesPerMin} />
        <Tile label="Data log" value={data.dbOk ? "Writable" : "Down"} tone={data.dbOk ? "up" : "down"} />
        <Tile label="Trade save time (median)" value={`${data.commitP50Ms} ms`} />
        <Tile label="Trade save time (slowest 1%)" value={`${data.commitP99Ms} ms`} tone={slow ? "down" : undefined} sub={slow ? "slower than usual" : undefined} />
        <Tile label="Failed saves" value={data.journalErrors} tone={data.journalErrors > 0 ? "down" : "up"} sub="trades refused rather than lost" />
        <Tile label="Disk space free" value={disk} tone={data.diskLow ? "down" : "up"} sub={data.diskLow ? "trades are refused until space is freed" : undefined} />
        <Tile
          label="Price updates"
          value={data.priceTicks}
          sub={data.lastPriceAt ? `last ${ago(data.lastPriceAt, now)}, one every ${data.tickSeconds} s while the market is open` : `one every ${data.tickSeconds} s while the market is open`}
        />
        <Tile label="Companies" value={data.symbolsTotal} />
      </div>

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
