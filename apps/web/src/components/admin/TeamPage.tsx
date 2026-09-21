import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { TeamDetail } from "@/lib/adminTypes";
import type { SymbolInfo } from "@/lib/types";
import { ago } from "@/lib/format";
import { ActionButton, Badge, ChoiceControl, LoadError, useDo, useNow, usePoll } from "./shared";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const STATUS = { active: ["Active", "up"], warned: ["Warned", "flag"], disqualified: ["Disqualified", "down"] } as const;

/** One team's wallet and standing, with every correction an organiser may need. All actions need a reason. */
export function TeamPage({ id }: { id: string }) {
  const { data, error, at, reload } = usePoll<TeamDetail>(`/api/admin/accounts/${encodeURIComponent(id)}`, 4000);
  const run = useDo(reload);
  const now = useNow(1000);
  const [symbols, setSymbols] = useState<SymbolInfo[]>([]);
  const [amount, setAmount] = useState("");
  const [direction, setDirection] = useState<"give" | "take">("give");
  const [symbol, setSymbol] = useState("");
  const [qty, setQty] = useState("");
  const [price, setPrice] = useState("");
  const [password, setPassword] = useState("");

  useEffect(() => {
    api
      .get<SymbolInfo[]>("/api/symbols")
      .then((s) => {
        setSymbols(s);
        setSymbol((cur) => cur || s[0]?.symbol || "");
      })
      .catch(() => {});
  }, []);

  if (!data) {
    return (
      <div className="page">
        <a href="#/participants" className="back">← Participants</a>
        <LoadError error={error} at={at} />
        {!error && <div className="empty">loading…</div>}
      </div>
    );
  }

  const { account: a, wallet: w } = data;
  const base = `/api/admin/accounts/${encodeURIComponent(id)}`;
  const [statusLabel, statusTone] = STATUS[a.status];
  const amt = Number(amount);
  const q = Number(qty);
  const p = Number(price);
  const cashOk = Number.isFinite(amt) && amt !== 0 && amount.trim() !== "";
  const sharesOk = symbol && Number.isInteger(q) && q >= 1 && (direction === "take" || p > 0);
  const disqualified = a.status === "disqualified";

  return (
    <div className="page">
      <a href="#/participants" className="back">← Participants</a>
      <LoadError error={error} at={at} />

      <header className="company-head">
        <div>
          <h1>{a.displayName}</h1>
          <div className="meta">
            <span className="dim">{a.email}</span>
            <Badge tone={statusTone}>{statusLabel}{a.warnings > 0 ? ` (${a.warnings} warning${a.warnings > 1 ? "s" : ""})` : ""}</Badge>
            <span className="chip">{a.role === "fund_manager" ? "Fund manager" : "Investor"}</span>
          </div>
        </div>
      </header>

      <div className="figures">
        <div className="figure"><div className="label">Net worth</div><div className="v">{money(w.netWorth)}</div></div>
        <div className="figure"><div className="label">Cash</div><div className="v">{money(w.cash)}</div></div>
        <div className="figure"><div className="label">Held back for orders</div><div className="v">{money(w.reserved)}</div></div>
        <div className="figure"><div className="label">Free to spend</div><div className="v">{money(w.available)}</div></div>
      </div>

      <h2 className="section">Wallet</h2>
      <div className="two-col" style={{ gap: 48 }}>
        <div className="stack">
          <div className="label">Add or remove cash</div>
          <div className="row-field">
            <input type="number" step="0.01" placeholder="Amount in ₹, negative to remove" value={amount} onChange={(e) => setAmount(e.target.value)} />
            <ActionButton
              className="solid"
              disabled={!cashOk}
              label="Apply"
              title={`${amt > 0 ? "Add" : "Remove"} ₹${money(Math.abs(amt || 0))} ${amt > 0 ? "to" : "from"} ${a.displayName}`}
              description="Cash held back for working orders cannot be removed. This is recorded in the team's history."
              run={(reason) => run(`${base}/cash`, { reason, amount: amt }, `Cash adjusted for ${a.displayName}`).then(() => setAmount(""))}
            />
          </div>
        </div>

        <div className="stack">
          <div className="label">Give or take back shares</div>
          <div className="seg" role="group" aria-label="Give or take">
            <button aria-pressed={direction === "give"} onClick={() => setDirection("give")}>Give</button>
            <button aria-pressed={direction === "take"} onClick={() => setDirection("take")}>Take back</button>
          </div>
          <select value={symbol} onChange={(e) => setSymbol(e.target.value)} aria-label="Company">
            {symbols.map((s) => (
              <option key={s.symbol} value={s.symbol}>{s.displayName} ({s.symbol})</option>
            ))}
          </select>
          <div className="row-field">
            <input type="number" min="1" step="1" placeholder="Shares" value={qty} onChange={(e) => setQty(e.target.value)} />
            {direction === "give" && <input type="number" min="0.01" step="0.01" placeholder="Value per share ₹" value={price} onChange={(e) => setPrice(e.target.value)} />}
          </div>
          <ActionButton
            className="solid"
            disabled={!sharesOk}
            label={direction === "give" ? "Give shares" : "Take shares back"}
            title={`${direction === "give" ? "Give" : "Take back"} ${q || "…"} shares of ${symbol} ${direction === "give" ? "to" : "from"} ${a.displayName}`}
            description={direction === "give" ? `Valued at ₹${p ? money(p) : "…"} each.` : "Shares held back for a working sell order cannot be taken."}
            run={(reason) => run(`${base}/shares`, { reason, direction, symbol, qty: q, price: p || undefined }, `Shares ${direction === "give" ? "given to" : "taken from"} ${a.displayName}`).then(() => setQty(""))}
          />
        </div>
      </div>

      <h2 className="section">Standing and access</h2>
      <div className="btn-row">
        {!disqualified && (
          <ActionButton
            label="Warn"
            title={`Issue a formal warning to ${a.displayName}`}
            run={(reason) => run(`${base}/warn`, { reason }, `${a.displayName} warned`)}
          />
        )}
        {!disqualified ? (
          <ActionButton
            danger
            label="Disqualify"
            title={`Disqualify ${a.displayName}`}
            description="Stops all trading and cancels every working order. You can reinstate the team later, but cancelled orders do not come back."
            run={(reason) => run(`${base}/disqualify`, { reason }, `${a.displayName} disqualified`)}
          />
        ) : (
          <ActionButton
            className="solid"
            label="Reinstate"
            title={`Reinstate ${a.displayName}`}
            run={(reason) => run(`${base}/reinstate`, { reason }, `${a.displayName} reinstated`)}
          />
        )}
        <ActionButton
          label="Cancel all working orders"
          disabled={data.orders.length === 0}
          title={`Cancel all ${data.orders.length} working orders of ${a.displayName}`}
          run={(reason) => run(`${base}/cancel-orders`, { reason }, "Orders cancelled")}
        />
        <span style={{ marginLeft: 12 }}>
          <ChoiceControl
            title="Change this team's role"
            value={a.role}
            options={[{ value: "investor", label: "Investor" }, { value: "fund_manager", label: "Fund manager" }]}
            onChoose={(next, reason) => run(`${base}/role`, { reason, role: next }, `${a.displayName} is now ${next.replace("_", " ")}`)}
          />
        </span>
      </div>
      <div className="row-field" style={{ maxWidth: 460, marginTop: 14 }}>
        <input type="text" autoComplete="off" placeholder="New password (8 or more characters)" value={password} onChange={(e) => setPassword(e.target.value)} />
        <ActionButton
          disabled={password.length < 8}
          label="Reset password"
          title={`Set a new password for ${a.displayName}`}
          description="Tell the team the new password yourself. It is not stored in the history."
          run={(reason) => run(`${base}/reset-password`, { reason, password }, "Password reset").then(() => setPassword(""))}
        />
      </div>

      <h2 className="section">Holdings</h2>
      <table className="roomy">
        <thead><tr><th>Company</th><th>Shares</th><th>Avg cost</th><th>Value</th><th>Returns</th></tr></thead>
        <tbody>
          {data.holdings.map((h) => (
            <tr key={h.symbol}>
              <td><strong>{h.symbol}</strong></td>
              <td>{h.qty}</td>
              <td>{money(h.avgPrice)}</td>
              <td>{money(h.marketValue)}</td>
              <td className={h.unrealizedPnl >= 0 ? "up" : "down"}>{h.unrealizedPnl >= 0 ? "+" : "−"}{money(Math.abs(h.unrealizedPnl))}</td>
            </tr>
          ))}
          {data.holdings.length === 0 && <tr><td colSpan={5} className="empty">no holdings</td></tr>}
        </tbody>
      </table>

      <h2 className="section">Working orders</h2>
      <table className="roomy">
        <thead><tr><th>Company</th><th>Side</th><th>Price</th><th>Left</th><th></th></tr></thead>
        <tbody>
          {data.orders.map((o) => (
            <tr key={o.id}>
              <td><strong>{o.symbol}</strong></td>
              <td className={o.side === "buy" ? "up" : "down"}>{o.side}</td>
              <td>{money(o.price)}</td>
              <td>{o.remainingQty}</td>
              <td>
                <ActionButton
                  className="ghost"
                  label="Cancel"
                  title={`Cancel this ${o.side} order in ${o.symbol}`}
                  run={(reason) => run(`${base}/cancel-orders`, { reason, orderId: o.id }, "Order cancelled")}
                />
              </td>
            </tr>
          ))}
          {data.orders.length === 0 && <tr><td colSpan={5} className="empty">nothing working</td></tr>}
        </tbody>
      </table>

      <h2 className="section">Recent trades</h2>
      <table className="roomy">
        <thead><tr><th>When</th><th>Company</th><th>Side</th><th>Price</th><th>Shares</th></tr></thead>
        <tbody>
          {data.fills.slice(0, 30).map((f) => {
            const mine = f.takerAccountId === id ? f.takerSide : f.takerSide === "buy" ? "sell" : "buy";
            return (
              <tr key={f.id + mine}>
                <td className="dim" style={{ fontFamily: "var(--sans)" }}>{ago(f.timestamp, now)}</td>
                <td><strong>{f.symbol}</strong></td>
                <td className={mine === "buy" ? "up" : "down"}>{mine}</td>
                <td>{money(f.price)}</td>
                <td>{f.qty}</td>
              </tr>
            );
          })}
          {data.fills.length === 0 && <tr><td colSpan={5} className="empty">no trades yet</td></tr>}
        </tbody>
      </table>

      <h2 className="section">History</h2>
      <table className="roomy">
        <thead><tr><th>When</th><th>Who</th><th>What</th><th>Why</th></tr></thead>
        <tbody>
          {data.history.map((e) => (
            <tr key={e.id}>
              <td className="mono" style={{ whiteSpace: "nowrap", fontFamily: "var(--mono)" }}>{new Date(e.at).toLocaleTimeString()}</td>
              <td style={{ fontFamily: "var(--sans)" }}>{e.actor}</td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left" }}>{e.action}</td>
              <td style={{ fontFamily: "var(--serif)", fontStyle: "italic", textAlign: "left" }}>{e.reason}</td>
            </tr>
          ))}
          {data.history.length === 0 && <tr><td colSpan={4} className="empty">nothing recorded for this team</td></tr>}
        </tbody>
      </table>
    </div>
  );
}
