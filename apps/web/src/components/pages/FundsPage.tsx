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
  const { refresh } = useSession();
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
    const raw = amounts[f.id] ?? "";
    setMessage(null);
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
        <span className={`chip ${view.windowOpen ? "" : "alert"}`}>{view.windowOpen ? `Allocation window ${view.window} is open` : "Allocation window closed"}</span>
      </div>

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
      {!view.compliant && <div className="down">You are below the required share. Invest in a fund before the window closes.</div>}
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
              </td>
              <td className="mono">{money(f.nav)}</td>
              <td className={f.returnPct >= 0 ? "up" : "down"}>{pct(f.returnPct)}</td>
              <td className="mono">{money(f.aum)}</td>
              <td className="mono">{f.myUnits > 0 ? money(f.myValue) : "—"}</td>
              <td className="mono">{view.windowOpen ? money(f.room) : "—"}</td>
              <td>
                {f.disqualified ? (
                  <span className="dim">closed</span>
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
                      disabled={!view.windowOpen}
                    />
                    <button className="solid" disabled={!view.windowOpen || busy !== null || !Number(amounts[f.id])} onClick={() => act(f, "allocate")}>
                      Invest
                    </button>
                    <button disabled={!view.windowOpen || busy !== null || f.myUnits <= 0 || !Number(amounts[f.id])} onClick={() => act(f, "redeem")}>
                      Withdraw
                    </button>
                    <button className="ghost" disabled={!view.windowOpen || busy !== null || f.myUnits <= 0} onClick={() => act(f, "redeem", true)}>
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
  const [logs, setLogs] = useState<StrategyLogEntry[]>([]);
  const [text, setText] = useState("");
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);

  const load = useCallback(() => {
    api
      .get<StrategyLogEntry[]>("/api/strategy-log")
      .then(setLogs)
      .catch(() => {});
  }, []);
  useEffect(load, [load]);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setMessage(null);
    try {
      await api.post("/api/strategy-log", { text });
      setText("");
      setMessage({ ok: true, text: "Saved." });
      load();
    } catch (err) {
      setMessage({ ok: false, text: err instanceof Error ? err.message : "That did not save." });
    }
  }

  return (
    <>
      <h2 className="section">Strategy log</h2>
      <p className="dim">
        To be considered for the creative and strategic investor prize, write 2 to 3 sentences at two or three checkpoints during Phase 2: what you did and why.
      </p>
      <form onSubmit={submit} className="stack" style={{ maxWidth: 640 }}>
        <textarea rows={3} maxLength={600} value={text} onChange={(e) => setText(e.target.value)} placeholder="What did you do, and why?" required />
        <div>
          <button type="submit" className="solid" disabled={!text.trim()}>
            Save entry
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
