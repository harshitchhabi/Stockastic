import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { SymbolInfo } from "@/lib/types";
import type { Overview, TimelineBlock } from "@/lib/adminTypes";
import { ActionButton, Badge, LoadError, useDo, usePoll } from "./shared";

export function ControlRoom() {
  const { data, error, at, reload } = usePoll<Overview>("/api/admin/overview", 2000);
  const run = useDo(reload);
  const [jumpTo, setJumpTo] = useState("");
  const [symbols, setSymbols] = useState<SymbolInfo[]>([]);
  const [pauseSym, setPauseSym] = useState("");
  const [announce, setAnnounce] = useState("");

  useEffect(() => {
    api
      .get<SymbolInfo[]>("/api/symbols")
      .then((s) => {
        setSymbols(s);
        setPauseSym((cur) => cur || s[0]?.symbol || "");
      })
      .catch(() => {});
  }, []);

  if (!data) return <div className="page"><LoadError error={error} at={at} />{!error && <div className="empty">loading the control room…</div>}</div>;

  const { clock, timeline, control } = data;
  const started = clock.status !== "not_started";
  const ended = clock.status === "ended";
  const current = started && !ended ? timeline[clock.blockIndex] : undefined;
  const next = !started ? timeline[0] : ended ? undefined : timeline[clock.blockIndex + 1];
  const phaseOf = (b?: TimelineBlock) => (!b ? "" : b.stage === "phase1" ? "Phase 1" : b.stage === "closing" ? "Closed" : "Phase 2");

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Control room</h1>
        <Badge tone={current ? "up" : undefined}>{!started ? "Not started" : ended ? "Event closed" : phaseOf(current)}</Badge>
        {control.tradingFrozen && <Badge tone="down">Trading frozen</Badge>}
      </div>

      <section className="clockface">
        <div className="now-block">
          <div className="label">Now</div>
          <div className="blk">{current ? current.label : ended ? "The event has closed" : "Waiting to start"}</div>
          <div className="dim">
            {current?.marketOpen ? "The market is open." : current?.allocationWindow != null ? `Allocation window ${current.allocationWindow} is open; the market is closed.` : "The market is closed."}
          </div>
        </div>
      </section>

      <div className="btn-row">
        {next && (
          <ActionButton
            className="solid"
            label={started ? `Next step: ${next.label}` : `Start: ${next.label}`}
            title={started ? `Move to "${next.label}"?` : `Start the event with "${next.label}"?`}
            description={
              next.freezeSnapshot === "phase1"
                ? "Phase 1 trading stops for everyone and the results are frozen. Then form the funds on the Funds page."
                : next.freezeSnapshot === "final"
                  ? "Trading stops for everyone and the final results are frozen. This is the end of the event."
                  : next.allocationWindow != null
                    ? `The market closes and allocation window ${next.allocationWindow} opens.`
                    : next.marketOpen
                      ? "The market opens."
                      : undefined
            }
            run={() => run("/api/admin/clock/next", { blockId: next.id }, `Now: ${next.label}`)}
          />
        )}
        <ActionButton
          danger
          label="Reset the whole event"
          title="Reset the whole event"
          description="Every team goes back to its starting cash with no shares, prices go back to their opening values, and trades, funds, news, disputes and snapshots are erased. Teams keep their accounts and passwords, and warnings are cleared. This cannot be undone."
          run={() => run("/api/admin/event/reset", {}, "The event was reset")}
        />
        <ActionButton
          danger
          label="Start completely fresh"
          title="Start completely fresh"
          description="Deletes every team and teammate, with all their trades, funds, watchlists, news and disputes, and signs everyone out. Prices and the clock go back to the start. Only the organisers' logins, the audit log, the rules text and the sign-up settings remain. Everyone registers again from the beginning. This cannot be undone."
          run={() => run("/api/admin/event/start-fresh", {}, "Everything was cleared. People can register again.")}
        />
      </div>

      <h2 className="section">Steps</h2>
      <ol className="timeline">
        {timeline.map((b, i) => {
          const state = !started || i > clock.blockIndex ? "next" : i === clock.blockIndex && !ended ? "now" : "past";
          return (
            <li key={b.id} className={state}>
              <span className="t mono">{i + 1}</span>
              <span className="l">{b.label}</span>
              <span className="tags">
                {b.marketOpen && <Badge tone="up">Market open</Badge>}
                {b.allocationWindow !== null && <Badge tone="flag">Window {b.allocationWindow}</Badge>}
                {b.freezeSnapshot && <Badge>Results frozen</Badge>}
              </span>
              <span />
            </li>
          );
        })}
      </ol>
      {started && (
        <div className="row-field" style={{ maxWidth: 560, marginTop: 8 }}>
          <select value={jumpTo} onChange={(e) => setJumpTo(e.target.value)} aria-label="Go to a step">
            <option value="">Go to a step (to correct a mistake)…</option>
            {timeline.map((b, i) => (
              <option key={b.id} value={b.id}>
                {i + 1}. {b.label}
              </option>
            ))}
          </select>
          <ActionButton
            label="Go"
            disabled={!jumpTo}
            danger
            title="Go to another step?"
            description="Use this only to correct a mistake. Steps skipped over count as having happened, including frozen results."
            run={() => run("/api/admin/clock/jump", { blockId: jumpTo }, "Moved to another step").then(() => setJumpTo(""))}
          />
        </div>
      )}

      <h2 className="section">Kill switch</h2>
      <div className="switches">
        <div className="switch killswitch">
          <div>
            <div className="label">All trading</div>
            <div className="dim">{control.tradingFrozen ? "All trading is stopped." : "Stops all trading immediately, whatever the step."}</div>
          </div>
          <ActionButton
            className={control.tradingFrozen ? "solid" : "solid danger"}
            danger={!control.tradingFrozen}
            label={control.tradingFrozen ? "Resume trading" : "Freeze trading"}
            title={control.tradingFrozen ? "Resume trading" : "Freeze all trading"}
            description={control.tradingFrozen ? "Trading opens again if the current step has the market open." : "Every participant's trading stops at once. Use it for a fault or a ruling."}
            run={() => run("/api/admin/control/freeze", { frozen: !control.tradingFrozen }, control.tradingFrozen ? "Trading resumed" : "Trading frozen")}
          />
        </div>
      </div>

      <h2 className="section">Pause one company</h2>
      <p className="dim" style={{ marginTop: 0 }}>
        Stops trading in a single company while everything else keeps trading.
      </p>
      <div className="row-field" style={{ maxWidth: 520 }}>
        <select value={pauseSym} onChange={(e) => setPauseSym(e.target.value)} aria-label="Company to pause">
          {symbols.map((s) => (
            <option key={s.symbol} value={s.symbol}>
              {s.displayName} ({s.symbol})
            </option>
          ))}
        </select>
        <ActionButton
          danger
          disabled={!pauseSym || control.pausedSymbols.includes(pauseSym)}
          label="Pause"
          title={`Pause trading in ${pauseSym}`}
          run={() => run(`/api/admin/control/symbols/${encodeURIComponent(pauseSym)}`, { paused: true }, `${pauseSym} paused`)}
        />
      </div>
      {control.pausedSymbols.length > 0 && (
        <div className="pausedlist">
          {control.pausedSymbols.map((sym) => (
            <span key={sym} className="row-field" style={{ alignItems: "center" }}>
              <Badge tone="down">{sym} paused</Badge>
              <ActionButton
                className="ghost"
                label="Resume"
                title={`Resume trading in ${sym}`}
                run={() => run(`/api/admin/control/symbols/${encodeURIComponent(sym)}`, { paused: false }, `${sym} resumed`)}
              />
            </span>
          ))}
        </div>
      )}

      <h2 className="section">Announce to everyone</h2>
      <p className="dim" style={{ marginTop: 0 }}>Appears at once on every participant's news column as a desk notice, and stays in it.</p>
      <div className="row-field" style={{ maxWidth: 720 }}>
        <input value={announce} maxLength={280} placeholder="For example: Allocation window 1 opens in five minutes" onChange={(e) => setAnnounce(e.target.value)} />
        <ActionButton
          className="solid"
          disabled={announce.trim().length < 3}
          label="Announce"
          title="Send this announcement to every participant"
          description={<strong>{announce}</strong>}
          run={() => run("/api/admin/announce", { text: announce.trim() }, "Announcement sent").then(() => setAnnounce(""))}
        />
      </div>

    </div>
  );
}
