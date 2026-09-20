/** "▲ 1.25 (0.90%)" in green, "▼ 0.40 (0.31%)" in vermilion — how far a price is from its reference. */
export function Change({ change, pct, compact = false }: { change: number | null; pct: number | null; compact?: boolean }) {
  if (change == null || pct == null) return <span className="mono dim">—</span>;
  const up = change >= 0;
  const abs = Math.abs(change).toFixed(2);
  const p = Math.abs(pct).toFixed(2);
  return (
    <span className={`mono ${up ? "up" : "down"}`}>
      {up ? "▲" : "▼"} {compact ? `${p}%` : `${abs} (${p}%)`}
    </span>
  );
}
