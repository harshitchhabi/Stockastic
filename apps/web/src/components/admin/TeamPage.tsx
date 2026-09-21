import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { TeamDetail } from "@/lib/adminTypes";
import type { SymbolInfo } from "@/lib/types";
import { ago } from "@/lib/format";
import { ActionButton, Badge, ChoiceControl, LoadError, useDo, useNow, usePoll } from "./shared";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });
const STATUS = { active: ["Active", "up"], warned: ["Warned", "flag"], disqualified: ["Disqualified", "down"] } as const;

type Run = ReturnType<typeof useDo>;

/** One holding with a box to set it to an exact number of shares. */
function HoldingRow({ h, base, name, run }: { h: TeamDetail["holdings"][number]; base: string; name: string; run: Run }) {
  const [qty, setQty] = useState(String(h.qty));
  useEffect(() => setQty(String(h.qty)), [h.qty]);
  const target = Number(qty);
  const valid = Number.isInteger(target) && target >= 0 && qty.trim() !== "";
  return (
    <tr>
      <td>
        <strong>{h.symbol}</strong>
      </td>
      <td>{h.qty}</td>
      <td>{money(h.avgPrice)}</td>
      <td>{money(h.marketValue)}</td>
      <td className={h.unrealizedPnl >= 0 ? "up" : "down"}>
        {h.unrealizedPnl >= 0 ? "+" : "−"}
        {money(Math.abs(h.unrealizedPnl))}
      </td>
      <td>
        <span className="row-field" style={{ justifyContent: "flex-end" }}>
          <input type="number" min="0" step="1" value={qty} onChange={(e) => setQty(e.target.value)} style={{ width: 90, flex: "none" }} aria-label={`Set ${h.symbol} shares`} />
          <ActionButton
            label="Set"
            disabled={!valid || target === h.qty}
            title={`Set ${name}'s ${h.symbol} to exactly ${valid ? target : "…"} shares`}
            description={`Now ${h.qty}. Shares held back for a working sell order cannot be taken.`}
            run={() => run(`${base}/shares`, { direction: "set", symbol: h.symbol, qty: target }, `${name} now holds ${target} ${h.symbol}`)}
          />
        </span>
      </td>
    </tr>
  );
}

/** One team's wallet and standing, with every correction an organiser may need. */
export function TeamPage({ id }: { id: string }) {
  const { data, error, at, reload } = usePoll<TeamDetail>(`/api/admin/accounts/${encodeURIComponent(id)}`, 4000);
  const run = useDo(reload);
  const now = useNow(1000);
  const [symbols, setSymbols] = useState<SymbolInfo[]>([]);
  const [cashMode, setCashMode] = useState<"change" | "set">("change");
  const [amount, setAmount] = useState("");
  const [direction, setDirection] = useState<"give" | "take" | "set">("give");
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
        <a href="#/participants" className="back">
          ← Participants
        </a>
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
  const amountOk = amount.trim() !== "" && Number.isFinite(amt) && (cashMode === "set" ? amt >= 0 : amt !== 0);
  const sharesOk = symbol && qty.trim() !== "" && Number.isInteger(q) && (direction === "set" ? q >= 0 : q >= 1) && (direction !== "give" || p > 0);
  const disqualified = a.status === "disqualified";
  const held = data.holdings.find((h) => h.symbol === symbol)?.qty ?? 0;

  return (
    <div className="page">
      <a href="#/participants" className="back">
        ← Participants
      </a>
      <LoadError error={error} at={at} />

      <header className="company-head">
        <div>
          <h1>{a.displayName}</h1>
          <div className="meta">
            <span className="dim">{a.email}</span>
            <Badge tone={statusTone}>
              {statusLabel}
              {a.warnings > 0 ? ` (${a.warnings} warning${a.warnings > 1 ? "s" : ""})` : ""}
            </Badge>
            {a.locked && <Badge tone="down">Locked</Badge>}
            <span className="chip">{a.role === "fund_manager" ? "Fund manager" : "Investor"}</span>
            <span className={a.online ? "up" : "dim"}>
              <span className={`dot-status ${a.online ? "on" : ""}`} />
              {a.online ? (a.sockets > 1 ? `Online, ${a.sockets} tabs open` : "Online") : a.lastSeen ? `Offline, left ${ago(a.lastSeen, now)}` : "Not seen since the server started"}
            </span>
          </div>
        </div>
      </header>

      <div className="figures">
        <div className="figure">
          <div className="label">Net worth</div>
          <div className="v">{money(w.netWorth)}</div>
        </div>
        <div className="figure">
          <div className="label">Cash</div>
          <div className="v">{money(w.cash)}</div>
        </div>
      </div>

      <h2 className="section">Wallet</h2>
      <div className="two-col" style={{ gap: 48 }}>
        <div className="stack">
          <div className="label">Cash</div>
          <div className="seg" role="group" aria-label="How to change the cash">
            <button aria-pressed={cashMode === "change"} onClick={() => { setCashMode("change"); setAmount(""); }}>
              Add or remove
            </button>
            <button aria-pressed={cashMode === "set"} onClick={() => { setCashMode("set"); setAmount(String(w.cash)); }}>
              Set exactly
            </button>
          </div>
          <div className="row-field">
            <input
              type="number"
              step="0.01"
              placeholder={cashMode === "set" ? "New balance in ₹" : "Amount in ₹, negative to remove"}
              value={amount}
              onChange={(e) => setAmount(e.target.value)}
            />
            <ActionButton
              className="solid"
              disabled={!amountOk}
              label="Apply"
              title={cashMode === "set" ? `Set ${a.displayName}'s cash to ₹${money(amt || 0)}` : `${amt > 0 ? "Add" : "Remove"} ₹${money(Math.abs(amt || 0))} ${amt > 0 ? "to" : "from"} ${a.displayName}`}
              run={() =>
                run(`${base}/cash`, cashMode === "set" ? { setTo: amt } : { amount: amt }, `Cash updated for ${a.displayName}`).then(() => setAmount(""))
              }
            />
          </div>
        </div>

        <div className="stack">
          <div className="label">Shares</div>
          <div className="seg" role="group" aria-label="Give, take or set">
            <button aria-pressed={direction === "give"} onClick={() => setDirection("give")}>Give</button>
            <button aria-pressed={direction === "take"} onClick={() => setDirection("take")}>Take back</button>
            <button aria-pressed={direction === "set"} onClick={() => setDirection("set")}>Set exactly</button>
          </div>
          <select value={symbol} onChange={(e) => setSymbol(e.target.value)} aria-label="Company">
            {symbols.map((s) => (
              <option key={s.symbol} value={s.symbol}>
                {s.displayName} ({s.symbol})
              </option>
            ))}
          </select>
          <div className="row-field">
            <input type="number" min={direction === "set" ? 0 : 1} step="1" placeholder={direction === "set" ? `New total (now ${held})` : "Shares"} value={qty} onChange={(e) => setQty(e.target.value)} />
            {direction === "give" && <input type="number" min="0.01" step="0.01" placeholder="Value per share ₹" value={price} onChange={(e) => setPrice(e.target.value)} />}
          </div>
          <ActionButton
            className="solid"
            disabled={!sharesOk}
            label={direction === "give" ? "Give shares" : direction === "take" ? "Take shares back" : "Set holding"}
            title={
              direction === "set"
                ? `Set ${a.displayName}'s ${symbol} to exactly ${q} shares`
                : `${direction === "give" ? "Give" : "Take back"} ${q || "…"} shares of ${symbol} ${direction === "give" ? "to" : "from"} ${a.displayName}`
            }
            description={direction === "give" ? `Valued at ₹${p ? money(p) : "…"} each.` : direction === "set" ? "Shares added are valued at the company's last price. Shares held back for a working sell order cannot be taken." : "Shares held back for a working sell order cannot be taken."}
            run={() =>
              run(`${base}/shares`, { direction, symbol, qty: q, price: direction === "give" ? p : undefined }, `Shares updated for ${a.displayName}`).then(() => setQty(""))
            }
          />
        </div>
      </div>

      <h2 className="section">Standing and access</h2>
      <div className="btn-row">
        {!disqualified && (
          <ActionButton label="Warn" title={`Issue a formal warning to ${a.displayName}`} run={() => run(`${base}/warn`, {}, `${a.displayName} warned`)} />
        )}
        {!disqualified ? (
          <ActionButton
            danger
            label="Disqualify"
            title={`Disqualify ${a.displayName}`}
            description="Stops all trading for this team. Trades already made stand. You can reinstate the team later."
            run={() => run(`${base}/disqualify`, {}, `${a.displayName} disqualified`)}
          />
        ) : (
          <ActionButton className="solid" label="Reinstate" title={`Reinstate ${a.displayName}`} run={() => run(`${base}/reinstate`, {}, `${a.displayName} reinstated`)} />
        )}
        <ActionButton
          label="Sign out"
          title={`Sign ${a.displayName} out`}
          description="Their open pages go back to the sign-in screen and their old logins stop working. They can sign in again straight away."
          run={() => run(`${base}/sign-out`, {}, `${a.displayName} signed out`)}
        />
        {a.locked ? (
          <ActionButton className="solid" label="Unlock" title={`Unlock ${a.displayName}`} run={() => run(`${base}/unlock`, {}, `${a.displayName} unlocked`)} />
        ) : (
          <ActionButton
            danger
            label="Lock"
            title={`Lock ${a.displayName}`}
            description="Signs them out and stops them signing in at all until you unlock them."
            run={() => run(`${base}/lock`, {}, `${a.displayName} locked`)}
          />
        )}
        <span style={{ marginLeft: 12 }}>
          <ChoiceControl
            title="Change this team's role"
            value={a.role}
            options={[
              { value: "investor", label: "Investor" },
              { value: "fund_manager", label: "Fund manager" },
            ]}
            onChoose={(next) => run(`${base}/role`, { role: next }, `${a.displayName} is now ${next.replace("_", " ")}`)}
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
          run={() => run(`${base}/reset-password`, { password }, "Password reset").then(() => setPassword(""))}
        />
      </div>

      <h2 className="section">Holdings</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>Company</th>
            <th>Shares</th>
            <th>Avg cost</th>
            <th>Value</th>
            <th>Returns</th>
            <th style={{ textAlign: "right" }}>Set to exactly</th>
          </tr>
        </thead>
        <tbody>
          {data.holdings.map((h) => (
            <HoldingRow key={h.symbol} h={h} base={base} name={a.displayName} run={run} />
          ))}
          {data.holdings.length === 0 && (
            <tr>
              <td colSpan={6} className="empty">
                no holdings
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <h2 className="section">Recent trades</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>When</th>
            <th>Company</th>
            <th>Side</th>
            <th>Price</th>
            <th>Shares</th>
          </tr>
        </thead>
        <tbody>
          {data.trades.slice(0, 30).map((f) => (
            <tr key={f.id}>
              <td className="dim" style={{ fontFamily: "var(--sans)" }}>
                {ago(f.timestamp, now)}
              </td>
              <td>
                <strong>{f.symbol}</strong>
              </td>
              <td className={f.side === "buy" ? "up" : "down"}>{f.side}</td>
              <td>{money(f.price)}</td>
              <td>{f.qty}</td>
            </tr>
          ))}
          {data.trades.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                no trades yet
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <h2 className="section">History</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>When</th>
            <th>Who</th>
            <th>What</th>
          </tr>
        </thead>
        <tbody>
          {data.history.map((e) => (
            <tr key={e.id}>
              <td className="mono" style={{ whiteSpace: "nowrap", fontFamily: "var(--mono)" }}>
                {new Date(e.at).toLocaleTimeString()}
              </td>
              <td style={{ fontFamily: "var(--sans)" }}>{e.actor}</td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left" }}>{e.action}</td>
            </tr>
          ))}
          {data.history.length === 0 && (
            <tr>
              <td colSpan={3} className="empty">
                nothing recorded for this team
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
