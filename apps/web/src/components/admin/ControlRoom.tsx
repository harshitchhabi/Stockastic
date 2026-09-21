import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { SymbolInfo } from "@/lib/types";
import type { Override, Overview, TimelineBlock } from "@/lib/adminTypes";
import { fmtClock, fmtMinSec } from "@/lib/format";
import { ActionButton, Badge, ChoiceControl, LoadError, useDo, useNow, usePoll } from "./shared";

const OVERRIDE_OPTIONS: { value: "schedule" | "open" | "closed"; label: string }[] = [
  { value: "schedule", label: "Follow schedule" },
  { value: "open", label: "Force open" },
  { value: "closed", label: "Force closed" },
];
const toChoice = (o: Override) => (o ?? "schedule");
const fromChoice = (c: string): Override => (c === "schedule" ? null : (c as Override));

/** How long one block lasts. Changing it moves every later block, so the event can run shorter or longer. */
function BlockEditor({ b, run }: { b: TimelineBlock; run: ReturnType<typeof useDo> }) {
  const shown = String(Math.round(b.durationMin));
  const [v, setV] = useState(shown);
  useEffect(() => setV(shown), [shown]);
  const n = Number(v);
  const valid = Number.isInteger(n) && n >= 1 && n <= 1440;
  return (
    <span className="row-field">
      <input type="number" min="1" max="1440" step="1" value={v} onChange={(e) => setV(e.target.value)} style={{ width: 72, flex: "none" }} aria-label={`Minutes for ${b.label}`} />
      <ActionButton
        label="Set"
        disabled={!valid || n === Math.round(b.durationMin)}
        title={`Make this block ${valid ? n : "…"} minutes long`}
        description={`${b.label}. Everything after it moves earlier or later to match. It cannot be shorter than the time already spent in it.`}
        run={() => run("/api/admin/clock/block-duration", { blockId: b.id, minutes: n }, `Block set to ${n} min`)}
      />
    </span>
  );
}

const hhmm = (min: number) => {
  const m = Math.round(min);
  return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}`;
};

export function ControlRoom() {
  const { data, error, at, reload } = usePoll<Overview>("/api/admin/overview", 2000);
  const now = useNow(1000);
  const run = useDo(reload);
  const [jumpTo, setJumpTo] = useState("");
  const [compress, setCompress] = useState("");
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
  const drift = clock.status === "running" && at ? now - at : 0;
  const elapsed = Math.min(clock.totalMs, clock.elapsedMs + drift);
  const remaining = Math.max(0, clock.remainingMs - drift);
  const block = timeline[clock.blockIndex];
  const running = clock.status === "running";
  const paused = clock.status === "paused";
  const statusText = { not_started: "Not started", running: "Running", paused: "Paused", ended: "Ended" }[clock.status];

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Control room</h1>
        <Badge tone={running ? "up" : paused ? "flag" : undefined}>{statusText}</Badge>
        {control.tradingFrozen && <Badge tone="down">Trading frozen</Badge>}
      </div>

      <section className="clockface">
        <div>
          <div className="label">Event time</div>
          <div className="digits">{fmtClock(elapsed)}</div>
          <div className="dim">of {fmtClock(clock.totalMs)}</div>
        </div>
        <div className="now-block">
          <div className="label">Now</div>
          <div className="blk">{block ? block.label : clock.status === "ended" ? "The event has ended" : "Waiting to start"}</div>
          {block && (
            <div className="dim">
              {fmtMinSec(remaining)} left in this block
              {timeline[clock.blockIndex + 1] ? `, next: ${timeline[clock.blockIndex + 1].label}` : ""}
            </div>
          )}
        </div>
      </section>
      <div className="progress" aria-hidden>
        <i style={{ width: `${clock.totalMs ? (elapsed / clock.totalMs) * 100 : 0}%` }} />
      </div>

      <div className="btn-row">
        {clock.status === "not_started" && (
          <ActionButton
            className="solid"
            label="Start the event"
            title="Start the event clock"
            description="The clock starts now and the schedule begins running. This cannot be undone."
            run={() => run("/api/admin/clock/start", {}, "Event started")}
          />
        )}
        {running && (
          <ActionButton
            label="Pause"
            title="Pause the event clock"
            description="The schedule stops. Trading follows whatever the current block and your overrides say."
            run={() => run("/api/admin/clock/pause", {}, "Event paused")}
          />
        )}
        {paused && (
          <ActionButton
            className="solid"
            label="Resume"
            title="Resume the event clock"
            description={
              <label className="field" style={{ marginTop: 6 }}>
                <span className="label">Make up lost time by shortening a later block (optional)</span>
                <select value={compress} onChange={(e) => setCompress(e.target.value)}>
                  <option value="">Do not shorten anything</option>
                  {timeline.slice(clock.blockIndex + 1).map((b) => (
                    <option key={b.id} value={b.id}>
                      {b.label}
                    </option>
                  ))}
                </select>
              </label>
            }
            run={() => run("/api/admin/clock/resume", { compressBlockId: compress || undefined }, "Event resumed")}
          />
        )}
        {(running || paused) &&
          [-5, -1, 1, 5].map((m) => (
            <ActionButton
              key={m}
              label={`${m > 0 ? "+" : "−"}${Math.abs(m)} min`}
              title={`Move the clock ${m > 0 ? "forward" : "back"} ${Math.abs(m)} minute${Math.abs(m) === 1 ? "" : "s"}`}
              run={() => run("/api/admin/clock/nudge", { minutes: m }, `Clock moved ${m} min`)}
            />
          ))}
        {(running || paused) && (
          <span className="jump">
            <select value={jumpTo} onChange={(e) => setJumpTo(e.target.value)} aria-label="Jump to block">
              <option value="">Jump to block…</option>
              {timeline.map((b) => (
                <option key={b.id} value={b.id}>
                  {b.label}
                </option>
              ))}
            </select>
            <ActionButton
              label="Jump"
              disabled={!jumpTo}
              danger
              title="Jump the clock to another block"
              description="Every block skipped is treated as having happened, including any freeze snapshot. Use it to recover, not to plan."
              run={() => run("/api/admin/clock/jump", { blockId: jumpTo }, "Clock jumped").then(() => setJumpTo(""))}
            />
          </span>
        )}
      </div>

      {(running || paused) && block && (
        <div className="btn-row" style={{ marginTop: 6 }}>
          <span className="label" style={{ marginRight: 4 }}>This block</span>
          {[-5, 5, 15].map((m) => (
            <ActionButton
              key={m}
              label={`${m > 0 ? "+" : "−"}${Math.abs(m)} min`}
              title={`${m > 0 ? "Add" : "Take off"} ${Math.abs(m)} minutes ${m > 0 ? "to" : "from"} the current block`}
              description={`${block.label}. Everything after it moves to match, and the event ${m > 0 ? "runs longer" : "ends sooner"}.`}
              run={() => run("/api/admin/clock/block-duration", { blockId: block.id, minutes: Math.round(block.durationMin) + m }, `Current block ${m > 0 ? "+" : "−"}${Math.abs(m)} min`)}
            />
          ))}
          <span style={{ marginLeft: "auto" }}>
            <ActionButton
              danger
              label="End the event now"
              title="End the event now"
              description="The clock goes to the end of the schedule, the market closes, and any freeze snapshots still due are taken. This cannot be undone."
              run={() => run("/api/admin/clock/end", {}, "Event ended")}
            />
          </span>
        </div>
      )}

      <h2 className="section">Trading switches</h2>
      <div className="switches">
        <div className="switch killswitch">
          <div>
            <div className="label">Kill switch</div>
            <div className="dim">{control.tradingFrozen ? "All order entry is stopped." : "Stops all order entry immediately, including orders already queued."}</div>
          </div>
          <ActionButton
            className={control.tradingFrozen ? "solid" : "solid danger"}
            danger={!control.tradingFrozen}
            label={control.tradingFrozen ? "Resume trading" : "Freeze trading"}
            title={control.tradingFrozen ? "Resume trading" : "Freeze all trading"}
            description={
              control.tradingFrozen
                ? "Order entry opens again, subject to the schedule and your other overrides."
                : "Every participant's order entry stops at once. Use it for a fault or a ruling. It is recorded against your name."
            }
            run={() =>
              run("/api/admin/control/freeze", { frozen: !control.tradingFrozen }, control.tradingFrozen ? "Trading resumed" : "Trading frozen")
            }
          />
        </div>

        <div className="switch">
          <div>
            <div className="label">Market</div>
            <div className="dim">{control.marketOpen ? "Open now" : "Closed now"}</div>
          </div>
          <ChoiceControl
            title="Change the market override"
            value={toChoice(control.marketOverride)}
            options={OVERRIDE_OPTIONS}
            describe={(n) => `The market will ${n === "schedule" ? "follow the schedule again" : n === "open" ? "be forced open" : "be forced closed"}.`}
            onChoose={(n) => run("/api/admin/control/market", { override: fromChoice(n) }, "Market override changed")}
          />
        </div>

        {control.windowsOpen.map((isOpen, i) => (
          <div className="switch" key={i}>
            <div>
              <div className="label">Allocation window {i}</div>
              <div className="dim">{isOpen ? "Open now" : "Closed now"}</div>
            </div>
            <ChoiceControl
              title={`Change the override for window ${i}`}
              value={toChoice(control.windowOverrides[i] ?? null)}
              options={OVERRIDE_OPTIONS}
              describe={(n) => `Window ${i} will ${n === "schedule" ? "follow the schedule again" : n === "open" ? "be forced open" : "be forced closed"}.`}
              onChoose={(n) => run(`/api/admin/control/windows/${i}`, { override: fromChoice(n) }, `Window ${i} override changed`)}
            />
          </div>
        ))}
      </div>

      <h2 className="section">Pause one company</h2>
      <p className="dim" style={{ marginTop: 0 }}>
        Stops new orders in a single company while everything else keeps trading. Orders already in its book stay there and can still be cancelled.
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

      <h2 className="section">Schedule</h2>
      <p className="dim" style={{ marginTop: 0, maxWidth: "70ch" }}>
        The event does not have to last five hours. Change the length of any block that has not finished and everything after it moves. The total at the top
        updates to match.
      </p>
      <ol className="timeline">
        {timeline.map((b, i) => {
          const state = i < clock.blockIndex ? "past" : i === clock.blockIndex ? "now" : "next";
          return (
            <li key={b.id} className={state}>
              <span className="t mono">{hhmm(b.startMin)}</span>
              <span className="l">{b.label}</span>
              <span className="tags">
                {b.marketOpen && <Badge tone="up">Market open</Badge>}
                {b.allocationWindow !== null && <Badge tone="flag">Window {b.allocationWindow}</Badge>}
              </span>
              {clock.status !== "ended" && (clock.status === "not_started" || i >= clock.blockIndex) ? (
                <BlockEditor b={b} run={run} />
              ) : (
                <span className="dim mono">{Math.round(b.durationMin)} min</span>
              )}
            </li>
          );
        })}
      </ol>
    </div>
  );
}
