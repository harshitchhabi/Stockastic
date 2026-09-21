import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";
import { useUniverse } from "@/lib/universe";
import { companyPath, pagePath } from "@/lib/router";
import type { Portfolio, Trade } from "@/lib/types";
import { Tick } from "../Tick";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** What you own, what it has earned, fund units, and your trades. */
export function HoldingsPage() {
  const { account } = useSession();
  const { bySymbol } = useUniverse();
  const [portfolio, setPortfolio] = useState<Portfolio | null>(null);
  const [trades, setTrades] = useState<Trade[]>([]);

  const load = useCallback(() => {
    if (!account) return;
    api
      .get<Portfolio>("/api/portfolio/me")
      .then(setPortfolio)
      .catch(() => {});
    api
      .get<Trade[]>("/api/trades/mine")
      .then(setTrades)
      .catch(() => {});
  }, [account]);

  useEffect(load, [load]);

  useEffect(() => {
    const socket = getSocket();
    socket.on("trade", load);
    socket.on("portfolio", load);
    socket.on("connect", load);
    return () => {
      socket.off("trade", load);
      socket.off("portfolio", load);
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

      {(portfolio?.fundPositions.length ?? 0) > 0 && (
        <>
          <h2 className="section">Funds</h2>
          <table className="roomy">
            <thead>
              <tr>
                <th>Fund</th>
                <th>Units</th>
                <th>NAV</th>
                <th>Invested</th>
                <th>Current value</th>
                <th>Returns</th>
              </tr>
            </thead>
            <tbody>
              {portfolio!.fundPositions.map((f) => (
                <tr key={f.fundId}>
                  <td>
                    <strong>{f.name}</strong>
                  </td>
                  <td>{f.units.toFixed(2)}</td>
                  <td>{money(f.nav)}</td>
                  <td>{money(f.contributed)}</td>
                  <td>{money(f.value)}</td>
                  <td className={f.pnl >= 0 ? "up" : "down"}>
                    {f.pnl >= 0 ? "+" : "−"}
                    {money(Math.abs(f.pnl))}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </>
      )}

      <h2 className="section">Your trades</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th>Time</th>
            <th>Company</th>
            <th>Side</th>
            <th>Qty</th>
            <th>Price</th>
            <th>Value</th>
          </tr>
        </thead>
        <tbody>
          {trades.map((t) => (
            <tr key={t.id}>
              <td>{new Date(t.timestamp).toLocaleTimeString()}</td>
              <td>
                <a href={companyPath(t.symbol)} className="rowlink">
                  <strong>{bySymbol.get(t.symbol)?.displayName ?? t.symbol}</strong> <span className="label">{t.symbol}</span>
                </a>
              </td>
              <td className={t.side === "buy" ? "up" : "down"}>{t.side}</td>
              <td>{t.qty}</td>
              <td>{money(t.price)}</td>
              <td>{money(t.value)}</td>
            </tr>
          ))}
          {trades.length === 0 && (
            <tr>
              <td colSpan={6} className="empty">
                no trades yet
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
