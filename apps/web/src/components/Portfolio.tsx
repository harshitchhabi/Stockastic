import { useCallback, useEffect } from "react";
import { useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";
import type { Portfolio as PortfolioData } from "@/lib/types";

export function Portfolio() {
  const { account } = useSession();
  const [portfolio, setPortfolio] = useState<PortfolioData | null>(null);

  const load = useCallback(() => {
    if (!account) return;
    api.get<PortfolioData>("/api/portfolio/me").then(setPortfolio);
  }, [account]);

  useEffect(() => {
    load();
  }, [load]);

  useEffect(() => {
    if (!account) return;
    const socket = getSocket();
    socket.on("fill", load);
    // Reconnection resync: re-fetch from the server rather than trust
    // whatever this panel held before a wifi drop.
    socket.on("connect", load);
    return () => {
      socket.off("fill", load);
      socket.off("connect", load);
    };
  }, [account, load]);

  if (!account || !portfolio) {
    return (
      <div className="panel">
        <div className="panel-header">Portfolio</div>
        <div className="panel-body" style={{ color: "var(--text-dim)" }}>
          —
        </div>
      </div>
    );
  }

  return (
    <div className="panel">
      <div className="panel-header">Portfolio</div>
      <div className="panel-body">
        <div className="figures">
          <div className="figure">
            <div className="label">Net worth</div>
            <div className="v">{portfolio.totalValue.toFixed(2)}</div>
          </div>
          <div className="figure">
            <div className="label">Cash</div>
            <div className="v">{portfolio.cashBalance.toFixed(2)}</div>
          </div>
        </div>
        <table>
          <thead>
            <tr>
              <th>Sym</th>
              <th>Qty</th>
              <th>Avg</th>
              <th>P&L</th>
            </tr>
          </thead>
          <tbody>
            {portfolio.holdings.map((h) => (
              <tr key={h.symbol}>
                <td>{h.symbol}</td>
                <td className="mono">{h.qty}</td>
                <td className="mono">{h.avgPrice.toFixed(2)}</td>
                <td className={`mono ${h.unrealizedPnl >= 0 ? "up" : "down"}`}>
                  {h.unrealizedPnl.toFixed(2)}
                </td>
              </tr>
            ))}
            {portfolio.holdings.length === 0 && (
              <tr>
                <td colSpan={4} className="empty">
                  no holdings yet
                </td>
              </tr>
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
