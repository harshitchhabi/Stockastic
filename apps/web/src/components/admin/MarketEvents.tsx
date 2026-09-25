import { useState } from "react";
import { api } from "@/lib/api";
import type { SimItem, SimStatus } from "@/lib/adminTypes";
import { ActionButton, Badge, LoadError, useAdmin, useDo, usePoll } from "./shared";

const at = (min: number) => {
  const s = Math.round(min * 60);
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return `${h}:${String(m).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
};

/**
 * The news schedule. News goes out by itself at its time, measured in open-market time so that pausing or
 * changing the schedule cannot put it out of step with the prices. You can hold any item, reword or move it,
 * release it now, or turn the automatic release off and send every item yourself.
 * Prices come from the data table whatever you do here: changing the news changes only what people read.
 */
export function MarketEvents() {
  const { data, error, at: fetched, reload } = usePoll<SimStatus>("/api/admin/sim", 4000);
  const run = useDo(reload);
  const { notify } = useAdmin();
  const [editing, setEditing] = useState<string | null>(null);
  const [text, setText] = useState("");
  const [when, setWhen] = useState("");
  const [shift, setShift] = useState("");

  if (!data) return <div className="page"><LoadError error={error} at={fetched} />{!error && <div className="empty">loading…</div>}</div>;

  const items = [...data.items].sort((a, b) => a.atMinute - b.atMinute);
  const fired = items.filter((i) => i.fired).length;
  const unit = data.newsClock === "market" ? "open-market time" : "event time";
  const clockText = (m: number | null) => (m == null ? "not in this schedule" : `T+${Math.floor(m / 60)}:${String(Math.floor(m % 60)).padStart(2, "0")}`);
  const shiftBy = Number(shift);
  const shiftOk = shift.trim() !== "" && Number.isFinite(shiftBy) && shiftBy !== 0 && Math.abs(shiftBy) <= 1440;

  async function saveEdit(i: SimItem) {
    const minutes = when.trim() === "" ? undefined : Number(when);
    if (minutes !== undefined && (!Number.isFinite(minutes) || minutes < 0)) {
      notify("Enter the time in minutes", false);
      throw new Error("bad time");
    }
    try {
      await api.post(`/api/admin/sim/${i.id}/edit`, { headline: text.trim() && text.trim() !== i.headline ? text.trim() : undefined, atMinute: minutes });
      notify("News item changed");
      setEditing(null);
      await reload();
    } catch (err) {
      notify(err instanceof Error ? err.message : "could not change it", false);
      throw err;
    }
  }

  return (
    <div className="page">
      <LoadError error={error} at={fetched} />
      <div className="page-head">
        <h1>News and market events</h1>
        <Badge tone={data.newsManual ? "flag" : "up"}>{data.newsManual ? "News is manual" : "News is automatic"}</Badge>
        <span className="dim">
          {fired} of {items.length} released
          {data.fromTable ? ` · prices from the data table, step ${data.ticks} of ${data.tableSteps}` : ` · ${data.ticks} price updates`}
        </span>
        <span style={{ marginLeft: "auto" }}>
          <ActionButton
            label={data.newsManual ? "Turn automatic news on" : "Turn automatic news off"}
            className={data.newsManual ? "solid" : ""}
            title={data.newsManual ? "Turn automatic news on" : "Turn automatic news off"}
            description={
              data.newsManual
                ? "Items go out by themselves again at their time. Any whose time has already passed go out straight away."
                : "Nothing goes out by itself. You release every item with its Release now button."
            }
            run={() => run("/api/admin/sim/news-mode", { manual: !data.newsManual }, data.newsManual ? "Automatic news is on" : "Automatic news is off")}
          />
        </span>
      </div>
      <p className="dim" style={{ marginTop: 0 }}>
        Times are minutes of {unit}. In Phase 2 fund managers see each item first and the public 60 seconds later. Bull and bear run announcements go to everyone at once.
        {data.fromTable && " Prices follow the data table exactly: holding, moving or rewording news changes what people read, not what prices do."}
      </p>
      <div className="btn-row" style={{ alignItems: "flex-end" }}>
        <label className="field" style={{ width: 200 }}>
          <span className="label">Move all remaining news by (minutes)</span>
          <input type="number" step="0.5" value={shift} onChange={(e) => setShift(e.target.value)} placeholder="for example 5 or -3" />
        </label>
        <ActionButton
          disabled={!shiftOk}
          label="Move all remaining"
          title={`Move every news item that has not gone out ${shiftBy > 0 ? "later" : "earlier"} by ${Math.abs(shiftBy)} minutes`}
          description="Use it if the event runs behind or ahead of the plan. Items already released are not affected, and nothing can go before minute 0."
          run={() => run("/api/admin/sim/shift", { minutes: shiftBy }, "Remaining news moved").then(() => setShift(""))}
        />
        <ActionButton
          label="Release everything overdue"
          title="Release every overdue news item now"
          description="Sends every item whose time has passed and that has not gone out and is not held. Use it to catch up after turning automatic news off."
          run={async () => {
            const r = await api.post<{ released: number }>("/api/admin/sim/release-overdue", {});
            notify(`${r.released} item${r.released === 1 ? "" : "s"} released`);
            await reload();
          }}
        />
      </div>
      <table className="roomy">
        <thead>
          <tr>
            <th>At</th>
            <th>On the clock</th>
            <th>Kind</th>
            <th>Headline</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {items.map((i) => (
            <tr key={i.id} style={i.fired ? { opacity: 0.55 } : undefined}>
              <td className="mono">{at(i.atMinute)}</td>
              <td className="mono dim">{data.newsClock === "market" ? clockText(i.clockMinute) : ""}</td>
              <td>
                {i.kind === "news" && i.type !== "REGIME" ? (
                  <Badge tone={i.type === "FAKE" ? "down" : i.type === "DENIAL" ? "flag" : undefined}>{i.type ?? "News"}</Badge>
                ) : (
                  <Badge tone={i.type === "REGIME" || i.kind !== "news" ? (i.headline.toUpperCase().includes("BEAR") || i.kind === "bear" ? "down" : "up") : undefined}>
                    {i.headline.toUpperCase().includes("BEAR") || i.kind === "bear" ? "Bear run" : "Bull run"}
                  </Badge>
                )}
                {i.category && <span className="label"> {i.category.toLowerCase()}</span>}
                {i.skipped && <> <Badge tone="flag">Held</Badge></>}
                {i.edited && <> <Badge>Edited</Badge></>}
              </td>
              <td style={{ textAlign: "left", fontFamily: "var(--sans)", maxWidth: 520 }}>
                {editing === i.id ? (
                  <div className="stack">
                    <textarea rows={2} maxLength={300} value={text} onChange={(e) => setText(e.target.value)} />
                    <label className="field" style={{ maxWidth: 220 }}>
                      <span className="label">Time in minutes ({unit})</span>
                      <input type="number" min="0" step="0.5" value={when} onChange={(e) => setWhen(e.target.value)} />
                    </label>
                    <span className="btn-row">
                      <ActionButton className="solid" label="Save" title="Save the change" description="The headline and time change for this item only." run={() => saveEdit(i)} />
                      <button onClick={() => setEditing(null)}>Cancel</button>
                    </span>
                  </div>
                ) : (
                  i.headline
                )}
              </td>
              <td>
                {i.fired ? (
                  <span className="dim">released</span>
                ) : (
                  editing !== i.id && (
                    <span className="btn-row" style={{ margin: 0, justifyContent: "flex-end" }}>
                      {i.kind === "news" && i.type !== "REGIME" && (
                        <button
                          onClick={() => {
                            setEditing(i.id);
                            setText(i.headline);
                            setWhen(String(i.atMinute));
                          }}
                        >
                          Edit
                        </button>
                      )}
                      <ActionButton
                        label={i.skipped ? "Let go out" : "Hold"}
                        title={i.skipped ? "Let this item go out at its time" : "Hold this item"}
                        description={i.skipped ? "It will be released at its time again." : "It will not be released by itself. You can still release it by hand."}
                        run={() => run(`/api/admin/sim/${i.id}/skip`, { skip: !i.skipped }, i.skipped ? "Item back on schedule" : "Item held")}
                      />
                      <ActionButton
                        label="Release now"
                        title="Release this item now"
                        description="It goes out now instead of at its scheduled time, and will not go out a second time."
                        run={() => run(`/api/admin/sim/${i.id}/fire`, {}, "Item released")}
                      />
                    </span>
                  )
                )}
              </td>
            </tr>
          ))}
          {items.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                no scheduled news
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
