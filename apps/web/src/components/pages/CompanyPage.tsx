import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";
import { useUniverse } from "@/lib/universe";
import { pagePath } from "@/lib/router";
import type { Portfolio } from "@/lib/types";
import { PriceChart, type HistoryPoint } from "../PriceChart";
import { TradeTicket } from "../TradeTicket";
import { ErrorBoundary } from "../ErrorBoundary";
import { Tick } from "../Tick";
import { Change } from "../Change";
import { StarButton } from "../StarButton";

const money = (n: number) => n.toLocaleString("en-IN", { minimumFractionDigits: 2, maximumFractionDigits: 2 });

/** One company: its price and movement, chart, the day's range, your position, and the trade ticket. */
export function CompanyPage({ symbol, tradingFrozen }: { symbol: string; tradingFrozen: boolean }) {
  const { account } = useSession();
  const { bySymbol, loaded } = useUniverse();
  const company = bySymbol.get(symbol);
  const [history, setHistory] = useState<HistoryPoint[]>([]);
  const [portfolio, setPortfolio] = useState<Portfolio | null>(null);

  useEffect(() => setHistory([]), [symbol]);

  const loadPortfolio = useCallback(() => {
    if (!account) return;
    api
      .get<Portfolio>("/api/portfolio/me")
      .then(setPortfolio)
      .catch(() => {});
  }, [account]);
  useEffect(loadPortfolio, [loadPortfolio]);
  useEffect(() => {
    const socket = getSocket();
    socket.on("trade", loadPortfolio);
    socket.on("portfolio", loadPortfolio);
    socket.on("connect", loadPortfolio);
    return () => {
      socket.off("trade", loadPortfolio);
      socket.off("portfolio", loadPortfolio);
      socket.off("connect", loadPortfolio);
    };
  }, [loadPortfolio]);

  const range = useMemo(() => {
    const prices = history.map((h) => h.price);
    if (company?.lastPrice != null) prices.push(company.lastPrice);
    if (prices.length === 0) return null;
    return { open: company?.base ?? prices[0], high: Math.max(...prices), low: Math.min(...prices) };
  }, [history, company?.lastPrice, company?.base]);

  const mine = portfolio?.holdings.find((h) => h.symbol === symbol);

  if (loaded && !company) {
    return (
      <div className="page">
        <a href={pagePath("explore")} className="back">
          ← Explore
        </a>
        <div className="empty">no company with the symbol “{symbol}”</div>
      </div>
    );
  }

  return (
    <div className="page company-page">
      <a href={pagePath("explore")} className="back">
        ← Explore
      </a>

      <header className="company-head">
        <div>
          <h1>{company?.displayName ?? symbol}</h1>
          <div className="meta">
            <span className="label">{symbol}</span>
            {company?.sector && <span className="chip">{company.sector}</span>}
            <StarButton symbol={symbol} />
          </div>
        </div>
        <div className="quote">
          <Tick value={company?.lastPrice} className="big" />
          <Change change={company?.change ?? null} pct={company?.changePct ?? null} />
        </div>
      </header>

      <div className="company-cols">
        <div className="company-main">
          <div className="chart-box">
            <ErrorBoundary name="Price chart">
              <PriceChart symbol={symbol} onHistory={setHistory} />
            </ErrorBoundary>
          </div>

          <dl className="stats">
            <div>
              <dt className="label">Open</dt>
              <dd className="mono">{range ? range.open.toFixed(2) : "—"}</dd>
            </div>
            <div>
              <dt className="label">High</dt>
              <dd className="mono">{range ? range.high.toFixed(2) : "—"}</dd>
            </div>
            <div>
              <dt className="label">Low</dt>
              <dd className="mono">{range ? range.low.toFixed(2) : "—"}</dd>
            </div>
            <div>
              <dt className="label">Sector</dt>
              <dd>{company?.sector ?? "—"}</dd>
            </div>
          </dl>

          <h2 className="section">Your position</h2>
          {mine ? (
            <div className="figures">
              <div className="figure">
                <div className="label">Shares</div>
                <div className="v">{mine.qty}</div>
              </div>
              <div className="figure">
                <div className="label">Avg cost</div>
                <div className="v">{money(mine.avgPrice)}</div>
              </div>
              <div className="figure">
                <div className="label">Current value</div>
                <div className="v">{money(mine.marketValue)}</div>
              </div>
              <div className="figure">
                <div className="label">Returns</div>
                <div className={`v ${mine.unrealizedPnl >= 0 ? "up" : "down"}`}>
                  {mine.unrealizedPnl >= 0 ? "+" : "−"}
                  {money(Math.abs(mine.unrealizedPnl))}
                </div>
              </div>
            </div>
          ) : (
            <div className="empty" style={{ textAlign: "left" }}>
              you don’t hold {company?.displayName ?? symbol} yet
            </div>
          )}
        </div>

        <aside className="company-side">
          <ErrorBoundary name="Trade ticket">
            <TradeTicket symbol={symbol} tradingFrozen={tradingFrozen} onTraded={loadPortfolio} />
          </ErrorBoundary>
        </aside>
      </div>
    </div>
  );
}
