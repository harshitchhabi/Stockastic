import { useState } from "react";
import type { Override, Overview } from "@/lib/adminTypes";
import { fmtClock, fmtMinSec } from "@/lib/format";
import { ActionButton, Badge, ChoiceControl, LoadError, useDo, useNow, usePoll } from "./shared";

const OVERRIDE_OPTIONS: { value: "schedule" | "open" | "closed"; label: string }[] = [
  { value: "schedule", label: "Follow schedule" },
  { value: "open", label: "Force open" },
  { value: "closed", label: "Force closed" },
];
const toChoice = (o: Override) => (o ?? "schedule");
const fromChoice = (c: string): Override => (c === "schedule" ? null : (c as Override));

export function ControlRoom() {
  const { data, error, at, reload } = usePoll<Overview>("/api/admin/overview", 2000);
  const now = useNow(1000);
  const run = useDo(reload);
  const [jumpTo, setJumpTo] = useState("");
  const [compress, setCompress] = useState("");

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
            run={(reason) => run("/api/admin/clock/start", { reason }, "Event started")}
          />
        )}
        {running && (
          <ActionButton
            label="Pause"
            title="Pause the event clock"
            description="The schedule stops. Trading follows whatever the current block and your overrides say."
            run={(reason) => run("/api/admin/clock/pause", { reason }, "Event paused")}
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
            run={(reason) => run("/api/admin/clock/resume", { reason, compressBlockId: compress || undefined }, "Event resumed")}
          />
        )}
        {(running || paused) &&
          [-5, -1, 1, 5].map((m) => (
            <ActionButton
              key={m}
              label={`${m > 0 ? "+" : "−"}${Math.abs(m)} min`}
              title={`Move the clock ${m > 0 ? "forward" : "back"} ${Math.abs(m)} minute${Math.abs(m) === 1 ? "" : "s"}`}
              run={(reason) => run("/api/admin/clock/nudge", { reason, minutes: m }, `Clock moved ${m} min`)}
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
              run={(reason) => run("/api/admin/clock/jump", { reason, blockId: jumpTo }, "Clock jumped").then(() => setJumpTo(""))}
            />
          </span>
        )}
      </div>

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
            run={(reason) =>
              run("/api/admin/control/freeze", { reason, frozen: !control.tradingFrozen }, control.tradingFrozen ? "Trading resumed" : "Trading frozen")
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
            onChoose={(n, reason) => run("/api/admin/control/market", { reason, override: fromChoice(n) }, "Market override changed")}
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
              onChoose={(n, reason) => run(`/api/admin/control/windows/${i}`, { reason, override: fromChoice(n) }, `Window ${i} override changed`)}
            />
          </div>
        ))}
      </div>

      <h2 className="section">Schedule</h2>
      <ol className="timeline">
        {timeline.map((b, i) => {
          const state = i < clock.blockIndex ? "past" : i === clock.blockIndex ? "now" : "next";
          return (
            <li key={b.id} className={state}>
              <span className="t mono">
                {Math.floor(b.startMin / 60)}:{String(b.startMin % 60).padStart(2, "0")}
              </span>
              <span className="l">{b.label}</span>
              <span className="tags">
                {b.marketOpen && <Badge tone="up">Market open</Badge>}
                {b.allocationWindow !== null && <Badge tone="flag">Window {b.allocationWindow}</Badge>}
                <span className="dim mono">{b.durationMin} min</span>
              </span>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
