import { useMemo, useState } from "react";
import { useUniverse, type Company } from "@/lib/universe";
import { companyPath } from "@/lib/router";
import { Tick } from "../Tick";
import { Change } from "../Change";
import { StarButton } from "../StarButton";

type Sort = "all" | "gainers" | "losers";

const SORTS: { id: Sort; label: string }[] = [
  { id: "all", label: "All companies" },
  { id: "gainers", label: "Top gainers" },
  { id: "losers", label: "Top losers" },
];

/** Every company in the market. Pick one to open it; nothing here draws a chart until you do. */
export function ExplorePage() {
  const { companies, sectors, loaded } = useUniverse();
  const [query, setQuery] = useState("");
  const [sector, setSector] = useState<string | null>(null);
  const [sort, setSort] = useState<Sort>("all");

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    const list = companies.filter(
      (c) => (!sector || c.sector === sector) && (!q || c.symbol.toLowerCase().includes(q) || c.displayName.toLowerCase().includes(q))
    );
    const pct = (c: Company) => c.changePct ?? 0;
    if (sort === "gainers") return [...list].sort((a, b) => pct(b) - pct(a));
    if (sort === "losers") return [...list].sort((a, b) => pct(a) - pct(b));
    return [...list].sort((a, b) => a.symbol.localeCompare(b.symbol));
  }, [companies, query, sector, sort]);

  return (
    <div className="page">
      <div className="page-head">
        <h1>Explore</h1>
        <span className="dim">{loaded ? `${shown.length} of ${companies.length} companies` : "loading the market…"}</span>
      </div>

      <div className="toolbar">
        <div className="tabs inline" role="tablist">
          {SORTS.map((s) => (
            <button key={s.id} role="tab" aria-selected={sort === s.id} onClick={() => setSort(s.id)}>
              {s.label}
            </button>
          ))}
        </div>
        <input
          type="search"
          className="search"
          placeholder="Search symbol or company"
          aria-label="Search companies"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
      </div>

      {sectors.length > 0 && (
        <div className="chips wide" role="group" aria-label="Filter by sector">
          <button aria-pressed={sector === null} onClick={() => setSector(null)}>
            All sectors
          </button>
          {sectors.map(([name, n]) => (
            <button key={name} aria-pressed={sector === name} onClick={() => setSector(sector === name ? null : name)}>
              {name} <span className="n">{n}</span>
            </button>
          ))}
        </div>
      )}

      <div className="company-grid">
        {shown.map((c) => (
          <a key={c.symbol} className="company-card" href={companyPath(c.symbol)}>
            <div className="top">
              <span className="sym">{c.symbol}</span>
              <StarButton symbol={c.symbol} />
            </div>
            <div className="nm">{c.displayName}</div>
            {c.sector && <div className="sec label">{c.sector}</div>}
            <div className="px">
              <Tick value={c.lastPrice} />
            </div>
            <Change change={c.change} pct={c.changePct} />
          </a>
        ))}
      </div>
      {loaded && shown.length === 0 && <div className="empty">no company matches</div>}
    </div>
  );
}
