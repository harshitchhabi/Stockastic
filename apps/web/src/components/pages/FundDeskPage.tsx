import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useUniverse } from "@/lib/universe";
import { companyPath, pagePath } from "@/lib/router";
import type { MyFund } from "@/lib/types";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const RISKS = ["Conservative", "Balanced", "Aggressive"];

/** The fund manager's desk: the fund's figures and holdings, its published profile, and the fees so far. */
export function FundDeskPage() {
  const { bySymbol } = useUniverse();
  const [data, setData] = useState<MyFund | null>(null);
  const [form, setForm] = useState({ name: "", philosophy: "", risk: "Balanced", strategy: "" });
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const [loadedProfile, setLoadedProfile] = useState(false);
  const [failed, setFailed] = useState(false);

  const load = useCallback(() => {
    api
      .get<MyFund>("/api/funds/mine")
      .then((d) => {
        setData(d);
        setFailed(false);
        setLoadedProfile((done) => {
          if (!done) setForm({ name: d.fund.name, philosophy: d.fund.philosophy, risk: d.fund.risk || "Balanced", strategy: d.fund.strategy });
          return true;
        });
      })
      .catch(() => setFailed(true));
  }, []);

  useEffect(load, [load]);
  useEffect(() => {
    const socket = getSocket();
    socket.on("trade", load);
    socket.on("portfolio", load);
    socket.on("connect", load);
    const t = setInterval(load, 15_000);
    return () => {
      socket.off("trade", load);
      socket.off("portfolio", load);
      socket.off("connect", load);
      clearInterval(t);
    };
  }, [load]);

  async function save(e: React.FormEvent) {
    e.preventDefault();
    setMessage(null);
    try {
      await api.put("/api/funds/mine/profile", form);
      setMessage({ ok: true, text: "Published. Investors see this now." });
      load();
    } catch (err) {
      setMessage({ ok: false, text: err instanceof Error ? err.message : "That did not save." });
    }
  }

  if (!data) {
    return (
      <div className="page">
        <div className="empty" style={{ textAlign: "left" }}>
          {failed ? "You are not assigned to a fund. The organisers will tell you when your fund is ready." : "loading…"}
        </div>
      </div>
    );
  }
  const f = data.fund;

  return (
    <div className="page">
      <div className="page-head">
        <h1>{f.name}</h1>
        <span className="label">Fund {f.number} · {f.managers.join(" · ")}</span>
      </div>

      <div className="figures">
        <div className="figure">
          <div className="label">NAV per unit</div>
          <div className="v">{money(f.nav)}</div>
        </div>
        <div className="figure">
          <div className="label">Return</div>
          <div className={`v ${f.returnPct >= 0 ? "up" : "down"}`}>
            {f.returnPct >= 0 ? "+" : "−"}
            {Math.abs(f.returnPct).toFixed(2)}%
          </div>
        </div>
        <div className="figure">
          <div className="label">Assets under management</div>
          <div className="v">{money(f.aum)}</div>
        </div>
        <div className="figure">
          <div className="label">Cash in the fund</div>
          <div className="v">{money(data.cash)}</div>
        </div>
        <div className="figure">
          <div className="label">Investors</div>
          <div className="v">{f.investors}</div>
        </div>
        <div className="figure">
          <div className="label">Largest fall</div>
          <div className="v">{(data.maxDrawdown * 100).toFixed(2)}%</div>
        </div>
        <div className="figure">
          <div className="label">Capital kept</div>
          <div className="v">{(data.retention * 100).toFixed(1)}%</div>
        </div>
      </div>
      {!data.canTrade && (
        <div className="chip alert" style={{ marginTop: 8 }}>
          Your fund is run by two teams. {data.traderName} places its trades; you can see everything and edit the profile.
        </div>
      )}
      <p className="dim" style={{ marginTop: 8 }}>
        {data.canTrade ? "To trade for the fund, open any company from" : "Trades for the fund are placed by your teammate team. Company pages are open from"} <a href={pagePath("explore")}>Explore</a>. Trades use the fund's cash and holdings, and your fund shares one allowance of trades per minute.
      </p>

      <h2 className="section">Holdings</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>Company</th>
            <th>Qty</th>
            <th>Avg cost</th>
            <th>Value</th>
            <th>Returns</th>
          </tr>
        </thead>
        <tbody>
          {data.holdings.map((h) => (
            <tr key={h.symbol}>
              <td>
                <a href={companyPath(h.symbol)} className="rowlink">
                  <strong>{bySymbol.get(h.symbol)?.displayName ?? h.symbol}</strong> <span className="label">{h.symbol}</span>
                </a>
              </td>
              <td>{h.qty}</td>
              <td>{money(h.avgPrice)}</td>
              <td>{money(h.marketValue)}</td>
              <td className={h.unrealizedPnl >= 0 ? "up" : "down"}>
                {h.unrealizedPnl >= 0 ? "+" : "−"}
                {money(Math.abs(h.unrealizedPnl))}
              </td>
            </tr>
          ))}
          {data.holdings.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                the fund holds no shares yet
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <h2 className="section">Fund profile</h2>
      <form onSubmit={save} className="stack" style={{ maxWidth: 560 }}>
        <label className="field">
          <span className="label">Fund name (a made-up name, never a real company)</span>
          <input value={form.name} maxLength={40} onChange={(e) => setForm({ ...form, name: e.target.value })} required />
        </label>
        <label className="field">
          <span className="label">Investment philosophy</span>
          <textarea rows={3} maxLength={400} value={form.philosophy} onChange={(e) => setForm({ ...form, philosophy: e.target.value })} />
        </label>
        <label className="field">
          <span className="label">Risk profile</span>
          <select value={form.risk} onChange={(e) => setForm({ ...form, risk: e.target.value })}>
            {RISKS.map((r) => (
              <option key={r}>{r}</option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="label">Investment strategy (for example Growth, Value, Macro, Sector-focused, Momentum)</span>
          <input value={form.strategy} maxLength={60} onChange={(e) => setForm({ ...form, strategy: e.target.value })} />
        </label>
        <div>
          <button type="submit" className="solid">
            Publish
          </button>
        </div>
        {message && <div className={message.ok ? "up" : "down"}>{message.text}</div>}
      </form>

      {data.checkpoints.length > 0 && (
        <>
          <h2 className="section">Checkpoints and fees</h2>
          <table className="roomy">
            <thead>
              <tr>
                <th>Checkpoint</th>
                <th>NAV</th>
                <th>AUM</th>
                <th>Average AUM</th>
                <th>Management fee</th>
                <th>Performance fee</th>
              </tr>
            </thead>
            <tbody>
              {data.checkpoints.map((c) => (
                <tr key={c.name}>
                  <td>{c.name}</td>
                  <td>{money(c.nav)}</td>
                  <td>{money(c.aum)}</td>
                  <td>{money(c.avgAum)}</td>
                  <td>{money(c.mgmtFee)}</td>
                  <td>{money(c.perfFee)}</td>
                </tr>
              ))}
            </tbody>
          </table>
          <p className="dim">Fees are a separate score for managers. They are never taken from what investors hold.</p>
        </>
      )}
    </div>
  );
}
