import { useState } from "react";
import type { AdminAccount, AdminFund, LogEntrant, Prizes, PrizeRow, Qualification, StrategyLogs } from "@/lib/adminTypes";
import { ActionButton, Badge, LoadError, useDo, usePoll } from "./shared";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const pct = (n: number) => `${n >= 0 ? "+" : "−"}${Math.abs(n).toFixed(2)}%`;

const BY: Record<string, string> = {
  value: "portfolio value",
  peak_portfolio_value: "higher peak value",
  fewer_transactions: "fewer trades",
  coin_toss: "coin toss",
  unresolved: "unresolved tie",
};

/** Phase 1 result and fund formation, then the funds, the prizes and the strategy-log judging. */
export function FundsAdmin() {
  const q = usePoll<Qualification>("/api/admin/qualification", 5000);
  const f = usePoll<AdminFund[]>("/api/admin/funds", 5000);
  const p = usePoll<Prizes>("/api/admin/prizes", 8000);
  const l = usePoll<StrategyLogs>("/api/admin/strategy-logs", 10000);
  const accts = usePoll<AdminAccount[]>("/api/admin/accounts", 8000);
  const reloadAll = () => Promise.all([q.reload(), f.reload(), p.reload(), l.reload(), accts.reload()]);
  const run = useDo(reloadAll);
  const qual = q.data;
  const funds = f.data ?? [];
  const names = new Map((accts.data ?? []).map((a) => [a.id, a.displayName]));

  return (
    <div className="page">
      <LoadError error={q.error ?? f.error} at={q.at} />
      <div className="page-head">
        <h1>Funds and prizes</h1>
      </div>

      <h2 className="section">Phase 1 result</h2>
      {!qual?.ready ? (
        <>
          <div className="empty" style={{ textAlign: "left" }}>
            Phase 1 has not been frozen yet. The ranking appears when trading freezes: at a schedule block set to freeze the standings, or when you do it here.
          </div>
          <div className="btn-row">
            <ActionButton
              label="Freeze Phase 1 standings now"
              title="Freeze the Phase 1 standings now"
              description="Every team's value is recorded at today's prices. The ranking that forms the funds comes from it. It can be taken once."
              run={() => run("/api/admin/snapshots/phase1", {}, "Phase 1 standings frozen")}
            />
          </div>
        </>
      ) : (
        <>
          <div className="btn-row">
            {qual.done ? (
              <Badge tone="up">Funds formed</Badge>
            ) : (
              <ActionButton
                className="solid"
                label={`Form the ${qual.cutoff / 2} funds`}
                title="Form the funds from this ranking"
                description={
                  <>
                    The top {qual.cutoff} teams become fund managers, paired first with last, second with second last, and so on. This can be done once.
                    {qual.boundaryTie && <> The last qualifying place is decided by a tie-break{qual.coinToss ? ", which reaches the coin toss. Have it supervised before you continue." : "."}</>}
                  </>
                }
                run={() => run("/api/admin/qualification/run", {}, "Funds formed")}
              />
            )}
            {qual.boundaryTie && !qual.done && <Badge tone="flag">{qual.coinToss ? "Coin toss at the cut" : "Tie-break at the cut"}</Badge>}
          </div>
          <table className="roomy">
            <thead>
              <tr>
                <th>Rank</th>
                <th>Team</th>
                <th>Final value</th>
                <th>Peak value</th>
                <th>Trades</th>
                <th>Placed by</th>
                <th>Fund</th>
              </tr>
            </thead>
            <tbody>
              {qual.rows.slice(0, Math.max(qual.cutoff + 6, 26)).map((r) => (
                <tr key={r.accountId} style={r.rank === qual.cutoff ? { borderBottom: "2px solid var(--ink)" } : undefined}>
                  <td className="mono">{r.rank}</td>
                  <td>
                    <a href={`#/team/${r.accountId}`} className="rowlink">
                      <strong>{r.team}</strong>
                    </a>{" "}
                    {r.qualifies && <span className="label">qualifies</span>}
                  </td>
                  <td>{money(r.value)}</td>
                  <td>{money(r.peak)}</td>
                  <td>{r.trades}</td>
                  <td className="dim" style={{ fontFamily: "var(--sans)" }}>{BY[r.decidedBy] ?? r.decidedBy}</td>
                  <td>{r.fund ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}

      {!qual?.done && (
        <ManualPairs
          accounts={accts.data ?? []}
          count={qual ? qual.cutoff / 2 : 10}
          ranked={qual?.rows ?? []}
          run={run}
        />
      )}

      <h2 className="section">Funds</h2>
      {funds.length > 0 && (
        <div className="btn-row">
          <ActionButton
            danger
            label="Dissolve the funds"
            title="Dissolve the funds"
            description="Takes the funds apart so they can be formed again, and moves their teams back to investors. Only possible before anyone has invested."
            run={() => run("/api/admin/funds/dissolve", {}, "Funds dissolved")}
          />
        </div>
      )}
      <table className="roomy">
        <thead>
          <tr>
            <th>Fund</th>
            <th>NAV</th>
            <th>Return</th>
            <th>AUM</th>
            <th>Investors</th>
            <th>Largest fall</th>
            <th>Kept</th>
            <th>In profit</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {funds.map((x) => (
            <tr key={x.id}>
              <td>
                <strong>{x.name}</strong> {x.disqualified && <Badge tone="down">Out of Prize 1</Badge>}
                <div className="label">
                  {x.id} · {x.managers.join(", ")} · {x.risk}
                </div>
                <div className="label">
                  Places the trades:{" "}
                  <select
                    value={x.trader}
                    aria-label={`Who trades for ${x.name}`}
                    onChange={(e) => void run(`/api/admin/funds/${x.id}/trader`, { accountId: e.target.value }, "Trader changed").catch(() => {})}
                  >
                    {x.memberIds.map((m) => (
                      <option key={m} value={m}>
                        {names.get(m) ?? m}
                      </option>
                    ))}
                  </select>
                </div>
              </td>
              <td>{money(x.nav)}</td>
              <td className={x.returnPct >= 0 ? "up" : "down"}>{pct(x.returnPct)}</td>
              <td>{money(x.aum)}</td>
              <td>{x.investors}</td>
              <td>{(x.maxDrawdown * 100).toFixed(2)}%</td>
              <td>{(x.retention * 100).toFixed(1)}%</td>
              <td>{(x.profitability * 100).toFixed(0)}%</td>
              <td>
                {!x.disqualified && (
                  <ActionButton
                    danger
                    label="Remove from Prize 1"
                    title={`Remove ${x.name} from Prize 1`}
                    description="The fund keeps trading but is no longer eligible for the best fund prize."
                    run={() => run(`/api/admin/funds/${x.id}/disqualify`, {}, `${x.name} removed from Prize 1`)}
                  />
                )}
              </td>
            </tr>
          ))}
          {funds.length === 0 && (
            <tr>
              <td colSpan={9} className="empty">
                no funds yet
              </td>
            </tr>
          )}
        </tbody>
      </table>

      {funds.some((x) => x.checkpoints.length > 0) && (
        <>
          <h2 className="section">Checkpoint fees</h2>
          <p className="dim" style={{ marginTop: 0 }}>Management fee on the average AUM of each period, and performance fee on new profit above the fund's high-water mark. Never taken from investors.</p>
          <table className="roomy">
            <thead>
              <tr>
                <th>Fund</th>
                <th>Checkpoint</th>
                <th>NAV</th>
                <th>Average AUM</th>
                <th>Management fee</th>
                <th>Performance fee</th>
              </tr>
            </thead>
            <tbody>
              {funds.flatMap((x) =>
                x.checkpoints.map((c) => (
                  <tr key={x.id + c.name}>
                    <td>{x.name}</td>
                    <td>{c.name}</td>
                    <td>{money(c.nav)}</td>
                    <td>{money(c.avgAum)}</td>
                    <td>{money(c.mgmtFee)}</td>
                    <td>{money(c.perfFee)}</td>
                  </tr>
                ))
              )}
            </tbody>
          </table>
        </>
      )}

      <h2 className="section">Prizes {p.data?.final ? <Badge tone="up">from the final freeze</Badge> : <Badge tone="flag">live, not final</Badge>}</h2>
      {p.data && !p.data.final && (
        <div className="btn-row">
          <ActionButton
            label="Freeze the final standings now"
            title="Freeze the final standings now"
            description="Every team's and fund's value is recorded at today's prices and the prizes are decided from it. It can be taken once."
            run={() => run("/api/admin/snapshots/final", {}, "Final standings frozen")}
          />
        </div>
      )}
      {p.data && (
        <div className="two-col" style={{ gap: 32 }}>
          <PrizeTable title="Prize 1: best fund management team" rows={p.data.prize1} fmt={(n) => n.toFixed(1)} unit="score" />
          <PrizeTable title="Prize 2: best individual investor" rows={p.data.prize2} fmt={money} unit="final value" />
          <PrizeTable title="Prize 3: most creative and strategic investor" rows={p.data.prize3} fmt={(n) => n.toFixed(1)} unit="judges' score" />
          <PrizeTable title="Prize 4: best risk manager" rows={p.data.prize4} fmt={(n) => n.toFixed(1)} unit="score" />
        </div>
      )}

      <h2 className="section">Strategy logs (Prize 3)</h2>
      {l.data && <Logs data={l.data} run={run} />}
    </div>
  );
}

function PrizeTable({ title, rows, fmt, unit }: { title: string; rows: PrizeRow[]; fmt: (n: number) => string; unit: string }) {
  return (
    <div>
      <h3 style={{ margin: "0 0 6px" }}>{title}</h3>
      <table className="roomy">
        <thead>
          <tr>
            <th>#</th>
            <th>Name</th>
            <th>{unit}</th>
          </tr>
        </thead>
        <tbody>
          {rows.slice(0, 10).map((r) => (
            <tr key={r.id}>
              <td className="mono">{r.rank}</td>
              <td>{r.name}</td>
              <td className="mono">{fmt(r.score)}</td>
            </tr>
          ))}
          {rows.length === 0 && (
            <tr>
              <td colSpan={3} className="empty">
                nothing to rank yet
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}

function Logs({ data, run }: { data: StrategyLogs; run: ReturnType<typeof useDo> }) {
  return (
    <>
      <p className="dim" style={{ marginTop: 0 }}>
        Investors who wrote at two or more checkpoints are eligible. Score each criterion from 0 to its maximum. Weights are equal until the organisers publish the rubric.
      </p>
      {data.entrants.map((e) => (
        <Entrant key={e.accountId} e={e} rubric={data.rubric} run={run} />
      ))}
      {data.entrants.length === 0 && <div className="empty" style={{ textAlign: "left" }}>no strategy logs yet</div>}
    </>
  );
}

function Entrant({ e, rubric, run }: { e: LogEntrant; rubric: StrategyLogs["rubric"]; run: ReturnType<typeof useDo> }) {
  const [scores, setScores] = useState<Record<string, string>>(() => Object.fromEntries(rubric.map((r) => [r.criterion, e.scores[r.criterion] != null ? String(e.scores[r.criterion]) : ""])));
  return (
    <div style={{ borderTop: "1px solid var(--rule)", padding: "10px 0" }}>
      <strong>{e.team}</strong> {e.eligible ? <Badge tone="up">eligible</Badge> : <Badge tone="flag">{e.checkpoints} checkpoint{e.checkpoints === 1 ? "" : "s"}</Badge>}{" "}
      {e.total != null && <span className="mono">{e.total.toFixed(1)} / 100</span>}
      {e.logs.map((x, i) => (
        <div key={i} className="dim" style={{ maxWidth: 720 }}>
          <span className="label">Checkpoint {x.checkpoint}</span> {x.text}
        </div>
      ))}
      <div className="btn-row" style={{ alignItems: "flex-end" }}>
        {rubric.map((r) => (
          <label key={r.criterion} className="field" style={{ width: 150 }}>
            <span className="label">{r.criterion} (0 to {r.maxScore})</span>
            <input type="number" min="0" max={r.maxScore} step="0.5" value={scores[r.criterion]} onChange={(ev) => setScores({ ...scores, [r.criterion]: ev.target.value })} />
          </label>
        ))}
        <ActionButton
          className="solid"
          label="Save scores"
          title={`Save the scores for ${e.team}`}
          run={() =>
            run(
              `/api/admin/strategy-logs/${e.accountId}/score`,
              { scores: Object.fromEntries(Object.entries(scores).filter(([, v]) => v !== "").map(([k, v]) => [k, Number(v)])) },
              `Scores saved for ${e.team}`
            )
          }
        />
      </div>
    </div>
  );
}

/**
 * Choose the fund managers yourself: each fund is two teams, and the first one places the fund's trades. Or fill
 * the rows from the Phase 1 ranking (first with last, second with second last, ...) and change what you like.
 */
function ManualPairs({
  accounts,
  count,
  ranked,
  run,
}: {
  accounts: AdminAccount[];
  count: number;
  ranked: { accountId: string; rank: number }[];
  run: ReturnType<typeof useDo>;
}) {
  const teams = accounts.filter((a) => !a.isAdmin && a.status !== "disqualified");
  const [rows, setRows] = useState<[string, string][]>([]);
  const used = new Set(rows.flat().filter(Boolean));
  const ok = rows.length > 0 && rows.every(([a, b]) => a && b && a !== b);

  const fromRanking = () => {
    const top = ranked.slice(0, count * 2);
    setRows(Array.from({ length: Math.min(count, Math.floor(top.length / 2)) }, (_, k) => [top[k].accountId, top[top.length - 1 - k].accountId] as [string, string]));
  };
  const set = (i: number, j: 0 | 1, v: string) => setRows((rs) => rs.map((r, k) => (k === i ? (j === 0 ? [v, r[1]] : [r[0], v]) : r)) as [string, string][]);

  return (
    <>
      <h2 className="section">Choose the fund managers</h2>
      <p className="dim" style={{ marginTop: 0 }}>
        Up to {count} funds of two teams each. The first team of each fund places its trades; you can change that later. Leave this empty and press "Form the funds" above to use the Phase 1 ranking as it stands.
      </p>
      <table className="roomy">
        <tbody>
          {rows.map((r, i) => (
            <tr key={i}>
              <td className="mono">Fund {i + 1}</td>
              {[0, 1].map((j) => (
                <td key={j}>
                  <select value={r[j]} onChange={(e) => set(i, j as 0 | 1, e.target.value)} aria-label={`Fund ${i + 1} ${j === 0 ? "trading team" : "other team"}`}>
                    <option value="">{j === 0 ? "Trading team…" : "Other team…"}</option>
                    {teams.map((t) => (
                      <option key={t.id} value={t.id} disabled={used.has(t.id) && r[j] !== t.id}>
                        {t.displayName}
                      </option>
                    ))}
                  </select>
                </td>
              ))}
              <td>
                <button className="ghost" onClick={() => setRows((rs) => rs.filter((_, k) => k !== i))}>
                  Remove
                </button>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <div className="btn-row">
        <button onClick={() => setRows((rs) => (rs.length < count ? [...rs, ["", ""]] : rs))} disabled={rows.length >= count}>
          Add a fund
        </button>
        {ranked.length >= 2 && <button onClick={fromRanking}>Fill from the ranking</button>}
        <ActionButton
          className="solid"
          disabled={!ok}
          label="Form the funds from these pairs"
          title="Form the funds from these pairs"
          description={`${rows.length} fund${rows.length === 1 ? "" : "s"} will be created and their teams become fund managers.`}
          run={() => run("/api/admin/qualification/run", { pairs: rows }, "Funds formed")}
        />
      </div>
    </>
  );
}
