import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";
import type { FundInfo, FundsView, StrategyLogEntry } from "@/lib/types";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const pct = (n: number) => `${n >= 0 ? "+" : "−"}${Math.abs(n).toFixed(2)}%`;

/**
 * Investors: the ten funds, with their profiles and NAV, and the allocation windows. Money goes in and out only
 * while a window is open; units are bought at the fund's NAV at that moment.
 */
export function FundsPage() {
  const { account, refresh } = useSession();
  const canAct = account?.canTrade !== false;
  const [view, setView] = useState<FundsView | null>(null);
  const [amounts, setAmounts] = useState<Record<string, string>>({});
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState<string | null>(null);

  const load = useCallback(() => {
    api
      .get<FundsView>("/api/funds")
      .then(setView)
      .catch(() => {});
  }, []);

  useEffect(load, [load]);
  useEffect(() => {
    // The window opens and closes on the schedule and by organiser override: pick that up promptly, and
    // keep NAV fresh without asking the server for it every second.
    const socket = getSocket();
    socket.on("controlState", load);
    socket.on("fundsFormed", load);
    socket.on("connect", load);
    const t = setInterval(load, 20_000);
    return () => {
      socket.off("controlState", load);
      socket.off("fundsFormed", load);
      socket.off("connect", load);
      clearInterval(t);
    };
  }, [load]);

  async function act(f: FundInfo, what: "allocate" | "redeem", all = false) {
    if (busy) return; // one at a time: a second click while the first is on its way does nothing
    const raw = amounts[f.id] ?? "";
    setMessage(null);
    // Say what is missing instead of doing nothing.
    if (!view?.windowOpen) return setMessage({ ok: false, text: "Money moves in and out of funds only while an allocation window is open. Wait for the organisers to open the next one." });
    if (what === "redeem" && f.myUnits <= 0) return setMessage({ ok: false, text: `You have nothing in ${f.name} to withdraw.` });
    if (!all && !(Number(raw) > 0)) return setMessage({ ok: false, text: `Type an amount in rupees next to ${f.name} first.` });
    setBusy(`${f.id}:${what}`);
    try {
      const r = await api.post<{ units: number; nav: number; amount: number }>(`/api/funds/${f.id}/${what}`, all ? { all: true } : { amount: Number(raw) });
      setMessage({
        ok: true,
        text: what === "allocate" ? `Invested ₹${money(r.amount)} in ${f.name}: ${r.units.toFixed(2)} units at ₹${money(r.nav)}.` : `Took ₹${money(r.amount)} out of ${f.name}.`,
      });
      setAmounts((p) => ({ ...p, [f.id]: "" }));
      load();
      void refresh();
    } catch (err) {
      setMessage({ ok: false, text: err instanceof Error ? err.message : "That did not work." });
    } finally {
      setBusy(null);
    }
  }

  if (!view) return <div className="page"><div className="empty">loading…</div></div>;
  if (!view.formed) {
    return (
      <div className="page">
        <div className="page-head">
          <h1>Funds</h1>
        </div>
        <div className="empty" style={{ textAlign: "left" }}>
          The ten funds are formed from the Phase 1 result. They appear here as Phase 2 begins.
        </div>
      </div>
    );
  }

  const share = view.myWallet > 0 ? (view.myValueInFunds / view.myWallet) * 100 : 0;

  return (
    <div className="page">
      <div className="page-head">
        <h1>Funds</h1>
        <span className={`chip ${view.windowOpen || view.closed ? "" : "alert"}`}>
          {view.closed ? "Event closed · results final" : view.windowOpen ? `Allocation window ${view.window} is open` : "Allocation window closed"}
        </span>
      </div>
      {!canAct && account && <p className="dim">Only {account.traderName} can move your team's money in and out of funds. You can watch everything from here.</p>}
      {view.windowOpen && view.window === view.lastWindow && (
        <div className="chip alert" style={{ margin: "8px 0", display: "block" }}>
          This is the last allocation window. When it closes, fund money is locked until the end of the event: make your final moves now.
        </div>
      )}

      <div className="figures">
        <div className="figure">
          <div className="label">In funds</div>
          <div className="v">{money(view.myValueInFunds)}</div>
        </div>
        <div className="figure">
          <div className="label">Share of your portfolio</div>
          <div className={`v ${view.compliant ? "" : "down"}`}>{share.toFixed(1)}%</div>
        </div>
        <div className="figure">
          <div className="label">Required</div>
          <div className="v">at least {view.mandatoryPercent}%</div>
        </div>
      </div>
      <p className="dim" style={{ marginTop: 8 }}>
        Every team keeps at least {view.mandatoryPercent}% of its portfolio in funds. The smallest investment in one fund is the lower of ₹{money(view.minAbsolute)} or {view.minWalletPercent}% of
        your portfolio, and no more than {view.maxWalletPercent}% of your portfolio can sit in one fund. Money moves in and out of funds only while a window is open. After the last window,
        fund positions are locked until the end.
      </p>
      {!view.compliant && !view.closed && (
        <div className="down">
          {view.windowOpen
            ? "You are below the required share. Invest in a fund before this window closes."
            : "You are below the required share. Put money into a fund when the next allocation window opens."}
        </div>
      )}
      {message && <div className={message.ok ? "up" : "down"} style={{ margin: "8px 0" }}>{message.text}</div>}

      <table className="roomy">
        <thead>
          <tr>
            <th>Fund</th>
            <th>Style</th>
            <th>NAV</th>
            <th>Return</th>
            <th>AUM</th>
            <th>Yours</th>
            <th>Room now</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {view.funds.map((f) => (
            <tr key={f.id}>
              <td>
                <strong>{f.name}</strong>
                <div className="dim" style={{ maxWidth: 320 }}>{f.philosophy || "No statement yet."}</div>
                <div className="label">{f.managers.join(" · ")}</div>
              </td>
              <td>
                <span className="chip">{f.risk || "—"}</span>
                <div className="dim">{f.strategy}</div>
                <div className="label">Fee {f.feePercent}%</div>
              </td>
              <td className="mono">{money(f.nav)}</td>
              <td className={f.returnPct >= 0 ? "up" : "down"}>{pct(f.returnPct)}</td>
              <td className="mono">{money(f.aum)}</td>
              <td className="mono">{f.myUnits > 0 ? money(f.myValue) : "—"}</td>
              <td className="mono">{view.windowOpen ? money(f.room) : "—"}</td>
              <td>
                {f.disqualified ? (
                  <span className="dim">closed</span>
                ) : view.closed ? (
                  <span className="dim">final</span>
                ) : (
                  <div style={{ display: "flex", gap: 4, alignItems: "center" }}>
                    <input
                      type="number"
                      min="0"
                      step="100"
                      placeholder="₹ amount"
                      style={{ width: 96 }}
                      value={amounts[f.id] ?? ""}
                      onChange={(e) => setAmounts((p) => ({ ...p, [f.id]: e.target.value }))}
                      disabled={!canAct}
                    />
                    <button className="solid" disabled={!canAct || busy !== null} onClick={() => act(f, "allocate")}>
                      {busy === `${f.id}:allocate` ? "…" : "Invest"}
                    </button>
                    <button disabled={!canAct || busy !== null} onClick={() => act(f, "redeem")}>
                      {busy === `${f.id}:redeem` ? "…" : "Withdraw"}
                    </button>
                    <button className="ghost" disabled={!canAct || busy !== null} onClick={() => act(f, "redeem", true)} title="Withdraw everything from this fund">
                      All
                    </button>
                  </div>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>

      <StrategyLog />
    </div>
  );
}

/** Prize 3: 2 to 3 sentences at each checkpoint, what you did and why. No entry, no eligibility. */
function StrategyLog() {
  const { account } = useSession();
  const canAct = account?.canTrade !== false;
  const [logs, setLogs] = useState<StrategyLogEntry[]>([]);
  const [text, setText] = useState("");
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const [saving, setSaving] = useState(false);

  const load = useCallback(() => {
    api
      .get<StrategyLogEntry[]>("/api/strategy-log")
      .then(setLogs)
      .catch(() => {});
  }, []);
  useEffect(load, [load]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (saving) return;
    setMessage(null);
    if (!text.trim()) return setMessage({ ok: false, text: "Write 2 to 3 sentences first: what you did, and why." });
    setSaving(true);
    try {
      await api.post("/api/strategy-log", { text });
      setText("");
      setMessage({ ok: true, text: "Saved. Your entry is recorded for this checkpoint." });
      load();
    } catch (err) {
      setMessage({ ok: false, text: err instanceof Error ? err.message : "That did not save." });
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <h2 className="section">Strategy log</h2>
      <p className="dim">
        To be considered for the creative and strategic investor prize, write 2 to 3 sentences at two or three checkpoints during Phase 2: what you did and why.
      </p>
      <form onSubmit={submit} className="stack" style={{ maxWidth: 640 }}>
        <textarea rows={3} maxLength={600} value={text} onChange={(e) => setText(e.target.value)} placeholder="What did you do, and why?" />
        <div>
          <button type="submit" className="solid" disabled={!canAct || saving} title={canAct ? undefined : "Only the person trading for your team can save the log"}>
            {saving ? "Saving…" : "Save entry"}
          </button>
        </div>
        {message && <div className={message.ok ? "up" : "down"}>{message.text}</div>}
      </form>
      {logs.length > 0 && (
        <table className="roomy">
          <tbody>
            {logs.map((l, i) => (
              <tr key={i}>
                <td className="label">Checkpoint {l.checkpoint}</td>
                <td>{l.text}</td>
                <td className="dim">{new Date(l.at).toLocaleTimeString()}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}
