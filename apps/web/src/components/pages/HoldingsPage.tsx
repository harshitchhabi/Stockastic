import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";
import { usePendingOrders } from "@/lib/useOrders";
import { useUniverse } from "@/lib/universe";
import { companyPath, pagePath } from "@/lib/router";
import type { Portfolio } from "@/lib/types";
import { Tick } from "../Tick";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** What you own, what it has earned, and the orders still working. */
export function HoldingsPage() {
  const { account } = useSession();
  const { bySymbol } = useUniverse();
  const { pending, cancel } = usePendingOrders();
  const [portfolio, setPortfolio] = useState<Portfolio | null>(null);

  const load = useCallback(() => {
    if (!account) return;
    api
      .get<Portfolio>("/api/portfolio/me")
      .then(setPortfolio)
      .catch(() => {});
  }, [account]);

  useEffect(load, [load]);

  useEffect(() => {
    const socket = getSocket();
    socket.on("fill", load);
    socket.on("connect", load);
    return () => {
      socket.off("fill", load);
      socket.off("connect", load);
    };
  }, [load]);

  const holdings = portfolio?.holdings ?? [];
  const invested = holdings.reduce((sum, h) => sum + h.qty * h.avgPrice, 0);
  const current = holdings.reduce((sum, h) => sum + h.marketValue, 0);
  const pnl = current - invested;
  const pnlPct = invested > 0 ? (pnl / invested) * 100 : 0;
  const cls = pnl >= 0 ? "up" : "down";

  return (
    <div className="page">
      <div className="page-head">
        <h1>Holdings</h1>
      </div>

      <div className="figures">
        <div className="figure">
          <div className="label">Net worth</div>
          <div className="v">{portfolio ? money(portfolio.totalValue) : "—"}</div>
        </div>
        <div className="figure">
          <div className="label">Invested</div>
          <div className="v">{portfolio ? money(invested) : "—"}</div>
        </div>
        <div className="figure">
          <div className="label">Current value</div>
          <div className="v">{portfolio ? money(current) : "—"}</div>
        </div>
        <div className="figure">
          <div className="label">Returns</div>
          <div className={`v ${cls}`}>
            {portfolio ? `${pnl >= 0 ? "+" : "−"}${money(Math.abs(pnl))}` : "—"}
            {portfolio && invested > 0 && <small> {pnlPct >= 0 ? "+" : "−"}{Math.abs(pnlPct).toFixed(2)}%</small>}
          </div>
        </div>
        <div className="figure">
          <div className="label">Cash</div>
          <div className="v">{portfolio ? money(portfolio.cashBalance) : "—"}</div>
        </div>
      </div>

      <h2 className="section">Stocks</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>Company</th>
            <th>Qty</th>
            <th>Avg cost</th>
            <th>Price</th>
            <th>Current value</th>
            <th>Returns</th>
          </tr>
        </thead>
        <tbody>
          {holdings.map((h) => {
            const c = bySymbol.get(h.symbol);
            const cost = h.qty * h.avgPrice;
            const ret = h.unrealizedPnl;
            return (
              <tr key={h.symbol}>
                <td>
                  <a href={companyPath(h.symbol)} className="rowlink">
                    <strong>{c?.displayName ?? h.symbol}</strong> <span className="label">{h.symbol}</span>
                  </a>
                </td>
                <td>{h.qty}</td>
                <td>{money(h.avgPrice)}</td>
                <td>
                  <Tick value={c?.lastPrice} />
                </td>
                <td>{money(h.marketValue)}</td>
                <td className={ret >= 0 ? "up" : "down"}>
                  {ret >= 0 ? "+" : "−"}
                  {money(Math.abs(ret))}
                  {cost > 0 && <small> {ret >= 0 ? "+" : "−"}{Math.abs((ret / cost) * 100).toFixed(2)}%</small>}
                </td>
              </tr>
            );
          })}
          {portfolio && holdings.length === 0 && (
            <tr>
              <td colSpan={6} className="empty">
                nothing held yet. Find a company on <a href={pagePath("explore")}>Explore</a>
              </td>
            </tr>
          )}
        </tbody>
      </table>

      <h2 className="section">Working orders</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>Company</th>
            <th>Side</th>
            <th>Price</th>
            <th>Qty left</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {pending.map((o) => (
            <tr key={o.id}>
              <td>
                <a href={companyPath(o.symbol)} className="rowlink">
                  <strong>{bySymbol.get(o.symbol)?.displayName ?? o.symbol}</strong> <span className="label">{o.symbol}</span>
                </a>
              </td>
              <td className={o.side === "buy" ? "up" : "down"}>{o.side}</td>
              <td>{money(o.price)}</td>
              <td>{o.remainingQty}</td>
              <td>
                <button className="ghost" onClick={() => cancel(o)}>
                  Cancel
                </button>
              </td>
            </tr>
          ))}
          {pending.length === 0 && (
            <tr>
              <td colSpan={5} className="empty">
                nothing working
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
