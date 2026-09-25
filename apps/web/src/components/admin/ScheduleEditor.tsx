import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { Schedule, ScheduleBlock, ScheduleCheck } from "@/lib/adminTypes";
import { ActionButton, Badge, useAdmin } from "./shared";

const STAGES: { value: ScheduleBlock["stage"]; label: string }[] = [
  { value: "phase1", label: "Phase 1 (everyone trades for themselves)" },
  { value: "transition", label: "Transition" },
  { value: "phase2", label: "Phase 2 (funds and investors, early news for managers)" },
  { value: "closing", label: "Closing" },
];

const slug = (s: string) =>
  s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "_")
    .replace(/^_|_$/g, "")
    .slice(0, 30);

/**
 * The schedule is yours. The rulebook's timeline is only a suggestion to start from. Add, remove, rename and
 * reorder blocks, set how long each lasts, whether the market is open, whether it is an allocation window, and
 * whether the standings are frozen when it starts. Changes apply straight away, also while the event runs.
 */
export function ScheduleEditor() {
  const { notify } = useAdmin();
  const [blocks, setBlocks] = useState<ScheduleBlock[]>([]);
  const [template, setTemplate] = useState<ScheduleBlock[]>([]);
  const [started, setStarted] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [loaded, setLoaded] = useState(false);
  const [check, setCheck] = useState<ScheduleCheck | null>(null);

  const loadCheck = useCallback(() => {
    api
      .get<ScheduleCheck>("/api/admin/schedule/check")
      .then(setCheck)
      .catch(() => {});
  }, []);

  const load = useCallback(async () => {
    try {
      const s = await api.get<Schedule>("/api/admin/schedule");
      setBlocks(s.blocks);
      setTemplate(s.template);
      setStarted(s.started);
      setDirty(false);
      setLoaded(true);
      loadCheck();
    } catch (err) {
      notify(err instanceof Error ? err.message : "could not load the schedule", false);
    }
  }, [notify, loadCheck]);
  useEffect(() => {
    void load();
  }, [load]);

  const edit = (i: number, patch: Partial<ScheduleBlock>) => {
    setBlocks((bs) => bs.map((b, j) => (j === i ? { ...b, ...patch } : b)));
    setDirty(true);
  };
  const move = (i: number, d: number) => {
    setBlocks((bs) => {
      const j = i + d;
      if (j < 0 || j >= bs.length) return bs;
      const next = [...bs];
      [next[i], next[j]] = [next[j], next[i]];
      return next;
    });
    setDirty(true);
  };
  const remove = (i: number) => {
    setBlocks((bs) => bs.filter((_, j) => j !== i));
    setDirty(true);
  };
  const add = () => {
    setBlocks((bs) => {
      let n = bs.length + 1;
      while (bs.some((b) => b.id === `block_${n}`)) n++;
      return [...bs, { id: `block_${n}`, label: "New block", minutes: 30, stage: "phase1", marketOpen: false, allocationWindow: null, freezeSnapshot: "" }];
    });
    setDirty(true);
  };

  async function save() {
    try {
      await api.put("/api/admin/schedule", { blocks });
      notify("Schedule saved");
      await load();
    } catch (err) {
      notify(err instanceof Error ? err.message : "the schedule was not saved", false);
      throw err;
    }
  }
  async function loadTemplate() {
    try {
      await api.post("/api/admin/schedule/template", {});
      notify("The rulebook timeline is now the schedule");
      await load();
    } catch (err) {
      notify(err instanceof Error ? err.message : "could not load the timeline", false);
      throw err;
    }
  }

  const total = blocks.reduce((s, b) => s + (Number(b.minutes) || 0), 0);
  const usedWindows = blocks.flatMap((b) => (b.allocationWindow != null ? [b.allocationWindow] : []));
  const nextWindow = () => {
    let n = 0;
    while (usedWindows.includes(n)) n++;
    return n;
  };

  if (!loaded) return <div className="page"><div className="empty">loading…</div></div>;

  return (
    <div className="page">
      <div className="page-head">
        <h1>Schedule</h1>
        <span className="dim mono">{blocks.length} blocks · {Math.floor(total / 60)} h {total % 60} min</span>
        {started && <Badge tone="flag">Event running: changes apply now</Badge>}
        {dirty && <Badge tone="down">Not saved</Badge>}
      </div>
      <p className="dim" style={{ marginTop: 0 }}>
        Nothing here is fixed. The blocks run one after another and you can start the event at any of them, pause it, jump between them from the control room, or change them at any time. A block with the market open lets teams trade.
        An allocation window lets investors move money in and out of funds. "Freeze standings" takes the Phase 1 result (call it <code>phase1</code>) or the final result (<code>final</code>) when the block starts; you can also take either by hand from Funds and prizes.
      </p>

      {check && (
        <div style={{ marginBottom: 14 }}>
          <div className="dim">
            {check.dataMinutes > 0 ? (
              <>
                Open trading in this saved schedule: <strong className="mono">{Math.round(check.openMinutes)} min</strong>. The price data covers{" "}
                <strong className="mono">{Math.round(check.dataMinutes)} min</strong> of open trading, and the last news item is due at minute{" "}
                <span className="mono">{Math.round(check.lastNewsMinute)}</span>.
              </>
            ) : (
              <>Open trading in this saved schedule: <strong className="mono">{Math.round(check.openMinutes)} min</strong>.</>
            )}
          </div>
          {check.breaks.length > 0 && (
            <div className="dim">
              Breaks with the market closed between open blocks: {check.breaks.join("; ")}. Prices and news stop during a break and carry on after it.
            </div>
          )}
          {check.warnings.map((w, i) => (
            <div key={i} className="down" style={{ marginTop: 4 }}>
              {w}
            </div>
          ))}
          {check.warnings.length === 0 && <div className="up" style={{ marginTop: 4 }}>The schedule fits the data.</div>}
          {dirty && <div className="dim">These checks are for the last saved schedule. Save your changes to check them.</div>}
        </div>
      )}

      <table className="roomy">
        <thead>
          <tr>
            <th></th>
            <th>Name</th>
            <th>Minutes</th>
            <th>Stage</th>
            <th>Market open</th>
            <th>Window</th>
            <th>Freeze standings</th>
            <th>Clock starts</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {blocks.map((b, i) => (
            <tr key={i}>
              <td style={{ whiteSpace: "nowrap" }}>
                <button className="ghost" onClick={() => move(i, -1)} disabled={i === 0} aria-label="Move up">
                  ↑
                </button>
                <button className="ghost" onClick={() => move(i, 1)} disabled={i === blocks.length - 1} aria-label="Move down">
                  ↓
                </button>
              </td>
              <td>
                <input
                  value={b.label}
                  maxLength={120}
                  style={{ width: 260 }}
                  onChange={(e) => edit(i, { label: e.target.value, id: /^block_\d+$/.test(b.id) ? slug(e.target.value) || b.id : b.id })}
                />
              </td>
              <td>
                <input type="number" min="1" max="1440" step="1" value={b.minutes} style={{ width: 72 }} onChange={(e) => edit(i, { minutes: Number(e.target.value) })} />
              </td>
              <td>
                <select value={b.stage} onChange={(e) => edit(i, { stage: e.target.value as ScheduleBlock["stage"] })}>
                  {STAGES.map((s) => (
                    <option key={s.value} value={s.value}>
                      {s.label}
                    </option>
                  ))}
                </select>
              </td>
              <td>
                <input type="checkbox" checked={b.marketOpen} onChange={(e) => edit(i, { marketOpen: e.target.checked })} aria-label="Market open" />
              </td>
              <td>
                <input
                  type="checkbox"
                  checked={b.allocationWindow != null}
                  onChange={(e) => edit(i, { allocationWindow: e.target.checked ? nextWindow() : null })}
                  aria-label="Allocation window"
                />{" "}
                {b.allocationWindow != null && <span className="mono">{b.allocationWindow}</span>}
              </td>
              <td>
                <select value={b.freezeSnapshot} onChange={(e) => edit(i, { freezeSnapshot: e.target.value })}>
                  <option value="">no</option>
                  <option value="phase1">Phase 1 result</option>
                  <option value="final">Final result</option>
                </select>
              </td>
              <td className="mono dim">
                {(() => {
                  const t = !dirty ? check?.blocks.find((x) => x.id === b.id) : undefined;
                  return t ? `T+${Math.floor(t.clockStartMin / 60)}:${String(Math.round(t.clockStartMin % 60)).padStart(2, "0")}` : "";
                })()}
              </td>
              <td>
                <button className="ghost" onClick={() => remove(i)} aria-label="Remove block" disabled={blocks.length <= 1}>
                  Remove
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <div className="btn-row">
        <button onClick={add}>Add a block</button>
        <ActionButton
          className="solid"
          disabled={!dirty}
          label="Save the schedule"
          title="Save the schedule"
          description={started ? "The event is running. The clock keeps its time, and the blocks are laid out again around it, so the current block can change." : "This replaces the schedule the event will follow."}
          run={save}
        />
        <button onClick={() => void load()} disabled={!dirty}>
          Discard changes
        </button>
        <ActionButton
          label="Load the rulebook timeline"
          title="Load the rulebook's timeline"
          description={`This replaces your schedule with the rulebook's ${template.length} blocks as a starting point. Nothing else changes.`}
          run={loadTemplate}
        />
      </div>
    </div>
  );
}
