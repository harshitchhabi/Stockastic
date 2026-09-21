import { useEffect, useState } from "react";
import { api, ApiError } from "@/lib/api";
import { useSession } from "@/lib/session";
import { useUniverse } from "@/lib/universe";
import type { MyFund, TradeSide } from "@/lib/types";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

interface TradeResponse {
  deduped?: boolean;
  trade: { qty: number; price: number; value: number; side: TradeSide };
}

/**
 * Buy or sell shares of one company at its current price. There is no price to type and no order type:
 * the trade happens at the price shown when you press the button, and the price you saw is sent with it, so
 * if the price has moved in the meantime the server refuses and shows you the new one. Trades are final.
 */
export function TradeTicket({ symbol, tradingFrozen, onTraded }: { symbol: string; tradingFrozen: boolean; onTraded?: () => void }) {
  const { account, refresh } = useSession();
  const { bySymbol } = useUniverse();
  const price = bySymbol.get(symbol)?.lastPrice ?? null;
  const [side, setSide] = useState<TradeSide>("buy");
  const [qty, setQty] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [done, setDone] = useState<string | null>(null);

  const n = Math.floor(Number(qty)) || 0;
  const value = price != null ? price * n : 0;
  const fund = account?.role === "fund_manager";
  // Two teams share a fund but only one of them places its trades.
  const [desk, setDesk] = useState<MyFund | null>(null);
  useEffect(() => {
    if (!fund) return;
    api
      .get<MyFund>("/api/funds/mine")
      .then(setDesk)
      .catch(() => {});
  }, [fund, account?.id]);
  const viewOnly = fund && desk != null && !desk.canTrade;

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    if (!account || price == null || n < 1) return;
    setError(null);
    setDone(null);
    setBusy(true);
    try {
      // A fresh id for each press; a retry of this same press reuses it, so a dropped connection can never
      // trade twice: the server returns the original result.
      const res = await api.postIdempotent<TradeResponse>("/api/trades", {
        clientTradeId: crypto.randomUUID(),
        symbol,
        side,
        qty: n,
        expectedPrice: price,
      });
      setDone(`${res.trade.side === "buy" ? "Bought" : "Sold"} ${res.trade.qty} at ₹${money(res.trade.price)}`);
      setQty("");
      void refresh();
      onTraded?.();
    } catch (err) {
      setError(err instanceof ApiError || err instanceof Error ? err.message : "The trade failed.");
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="panel ticket">
      <div className="panel-header">
        {fund ? "Trade for your fund" : "Trade"} <span className="label">{symbol}</span>
      </div>
      <div className="panel-body">
        <form onSubmit={submit} className="stack">
          <div className="seg">
            <button type="button" className="buy" aria-pressed={side === "buy"} onClick={() => setSide("buy")}>
              BUY
            </button>
            <button type="button" className="sell" aria-pressed={side === "sell"} onClick={() => setSide("sell")}>
              SELL
            </button>
          </div>

          <div className="ticket-summary">
            <div>
              <span className="label">Price now</span>
              <span className="mono">{price != null ? money(price) : "—"}</span>
            </div>
          </div>

          <label className="field">
            <span className="label">Number of shares</span>
            <input type="number" step="1" min="1" required value={qty} onChange={(e) => setQty(e.target.value)} />
          </label>

          <div className="ticket-summary">
            <div>
              <span className="label">{side === "buy" ? "You pay" : "You receive"}</span>
              <span className="mono">{value > 0 ? money(value) : "—"}</span>
            </div>
            <div>
              <span className="label">{fund ? "Fund cash" : "Cash available"}</span>
              <span className="mono">{account ? money(account.cashBalance) : "—"}</span>
            </div>
          </div>

          {error && <div className="down">{error}</div>}
          {done && <div className="up">{done}</div>}
          {tradingFrozen && <div className="down">Trading is frozen by the organisers.</div>}
          {viewOnly && <div className="down">{desk?.traderName} places this fund's trades. You can watch the fund from here.</div>}

          <button type="submit" className="solid" disabled={!account || busy || tradingFrozen || viewOnly || price == null || n < 1} style={{ padding: 10 }}>
            {busy ? "Trading…" : `${side === "buy" ? "Buy" : "Sell"} ${n > 0 ? n : ""} ${symbol}`}
          </button>
        </form>
      </div>
    </div>
  );
}
