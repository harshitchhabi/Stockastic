import { useUniverse } from "@/lib/universe";
import { companyPath, pagePath } from "@/lib/router";
import { Tick } from "../Tick";
import { Change } from "../Change";
import { StarButton } from "../StarButton";

/** The companies you have starred, kept together with their live prices. */
export function WatchlistPage() {
  const { companies, starred, loaded } = useUniverse();
  const rows = companies.filter((c) => starred.has(c.symbol));

  return (
    <div className="page">
      <div className="page-head">
        <h1>Watchlist</h1>
        <span className="dim">{rows.length} followed</span>
      </div>

      {rows.length > 0 ? (
        <table className="roomy">
          <thead>
            <tr>
              <th>Company</th>
              <th>Sector</th>
              <th>Price</th>
              <th>Change</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {rows.map((c) => (
              <tr key={c.symbol}>
                <td>
                  <a href={companyPath(c.symbol)} className="rowlink">
                    <strong>{c.displayName}</strong> <span className="label">{c.symbol}</span>
                  </a>
                </td>
                <td className="dim" style={{ fontFamily: "var(--sans)" }}>
                  {c.sector ?? "—"}
                </td>
                <td>
                  <Tick value={c.lastPrice} />
                </td>
                <td>
                  <Change change={c.change} pct={c.changePct} />
                </td>
                <td>
                  <StarButton symbol={c.symbol} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      ) : (
        loaded && (
          <div className="empty">
            nothing followed yet. Star a company on <a href={pagePath("explore")}>Explore</a> to keep it here
          </div>
        )
      )}
    </div>
  );
}
