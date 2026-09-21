import type { SimStatus } from "@/lib/adminTypes";
import { ActionButton, Badge, LoadError, useDo, usePoll } from "./shared";

const at = (min: number) => {
  const s = Math.round(min * 60);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return `${h}:${String(m).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
};

/**
 * The price simulation's schedule: every market event and bull or bear run, when it fires, and whether it has.
 * Real events move prices; fake items and denials are news only. Teams are never told which is which.
 */
export function MarketEvents() {
  const { data, error, at: fetched, reload } = usePoll<SimStatus>("/api/admin/sim", 4000);
  const run = useDo(reload);
  if (!data) return <div className="page"><LoadError error={error} at={fetched} />{!error && <div className="empty">loading…</div>}</div>;

  const items = [...data.items].sort((a, b) => a.atMinute - b.atMinute);
  const fired = items.filter((i) => i.fired).length;
  return (
    <div className="page">
      <LoadError error={error} at={fetched} />
      <div className="page-head">
        <h1>Market events</h1>
        <span className="dim">
          {fired} of {items.length} released · {data.ticks} price updates · {data.activeShocks} price moves in progress · {data.pendingShocks} waiting on the news lead
        </span>
      </div>
      <p className="dim" style={{ marginTop: 0 }}>
        Events fire by themselves at their time in the schedule. Releasing one early sends it now. Times are since the event started.
      </p>
      <table className="roomy">
        <thead>
          <tr>
            <th>At</th>
            <th>Kind</th>
            <th>Headline</th>
            <th>Prices moved</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {items.map((i) => (
            <tr key={i.id} style={i.fired ? { opacity: 0.55 } : undefined}>
              <td className="mono">{at(i.atMinute)}</td>
              <td>
                {i.kind === "news" ? (
                  <Badge tone={i.type === "FAKE" ? "down" : i.type === "DENIAL" ? "flag" : undefined}>{i.type ?? "News"}</Badge>
                ) : (
                  <Badge tone={i.kind === "bull" ? "up" : "down"}>{i.kind === "bull" ? "Bull run" : "Bear run"}</Badge>
                )}
                {i.category && <span className="label"> {i.category.toLowerCase()}</span>}
              </td>
              <td style={{ textAlign: "left", fontFamily: "var(--sans)", maxWidth: 520 }}>{i.headline}</td>
              <td>{i.kind === "news" ? (i.impacts > 0 ? `${i.impacts} target${i.impacts > 1 ? "s" : ""}` : "none") : "market wide"}</td>
              <td>
                {i.fired ? (
                  <span className="dim">released</span>
                ) : (
                  <ActionButton
                    label="Release now"
                    title="Release this event now"
                    description="Sends the news now instead of at its scheduled time. It will not fire a second time."
                    run={() => run(`/api/admin/sim/${i.id}/fire`, {}, "Event released")}
                  />
                )}
              </td>
            </tr>
          ))}
          {items.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                no scheduled events
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
