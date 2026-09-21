import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { AdminAccount } from "@/lib/adminTypes";
import type { SymbolInfo } from "@/lib/types";
import { ActionButton, LoadError, useDo, usePoll } from "./shared";

/**
 * Teams start with cash only and short selling is not allowed, so nobody can sell until shares exist.
 * This is how shares enter the market: an organiser gives a team (or every team) some, valued at a
 * price you choose. Each grant is stored, audited with your reason, and cannot be undone.
 */
export function Shares() {
  const { data: accounts, error, at, reload } = usePoll<AdminAccount[]>("/api/admin/accounts", 15000);
  const run = useDo(reload);
  const [symbols, setSymbols] = useState<SymbolInfo[]>([]);
  const [team, setTeam] = useState("*");
  const [symbol, setSymbol] = useState("");
  const [qty, setQty] = useState("");
  const [price, setPrice] = useState("");

  useEffect(() => {
    api
      .get<SymbolInfo[]>("/api/symbols")
      .then((s) => {
        setSymbols(s);
        setSymbol((cur) => cur || s[0]?.symbol || "");
      })
      .catch(() => {});
  }, []);

  const company = symbols.find((s) => s.symbol === symbol);
  const teams = (accounts ?? []).filter((a) => a.status !== "disqualified");
  const q = Number(qty);
  const p = Number(price);
  const valid = symbol && Number.isInteger(q) && q >= 1 && p > 0;
  const who = team === "*" ? `every team (${teams.length})` : teams.find((t) => t.id === team)?.displayName ?? "this team";

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Shares</h1>
      </div>
      <p className="dim" style={{ marginTop: 0, maxWidth: "62ch" }}>
        Teams start with cash only and cannot sell what they do not own, so no trade can happen until shares exist. Give shares here.
        Each grant is permanent, recorded against your name with the reason you give, and valued at the price you set.
      </p>

      <div className="stack" style={{ maxWidth: 460 }}>
        <label className="field">
          <span className="label">Give to</span>
          <select value={team} onChange={(e) => setTeam(e.target.value)}>
            <option value="*">Every team</option>
            {teams.map((t) => (
              <option key={t.id} value={t.id}>
                {t.displayName}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="label">Company</span>
          <select value={symbol} onChange={(e) => setSymbol(e.target.value)}>
            {symbols.map((s) => (
              <option key={s.symbol} value={s.symbol}>
                {s.displayName} ({s.symbol})
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span className="label">Shares each</span>
          <input type="number" min="1" step="1" value={qty} onChange={(e) => setQty(e.target.value)} />
        </label>
        <label className="field">
          <span className="label">Value per share (₹){company?.lastPrice != null ? `, now ${company.lastPrice.toFixed(2)}` : ""}</span>
          <input type="number" min="0.01" step="0.01" value={price} onChange={(e) => setPrice(e.target.value)} />
        </label>
        <ActionButton
          className="solid"
          disabled={!valid}
          label="Give shares"
          title={`Give ${q || "…"} shares of ${symbol} to ${who}`}
          description={`Valued at ₹${p ? p.toFixed(2) : "…"} each. This cannot be undone.`}
          run={(reason) =>
            run("/api/admin/grants", { reason, accountId: team, symbol, qty: q, price: p }, `Gave ${q} ${symbol} to ${who}`).then(() => setQty(""))
          }
        />
      </div>
    </div>
  );
}
