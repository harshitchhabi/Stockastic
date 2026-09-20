import { useUniverse } from "@/lib/universe";

export function StarButton({ symbol }: { symbol: string }) {
  const { starred, toggleStar } = useUniverse();
  const on = starred.has(symbol);
  return (
    <button
      className="star"
      aria-pressed={on}
      aria-label={on ? `Remove ${symbol} from watchlist` : `Add ${symbol} to watchlist`}
      title={on ? "Remove from watchlist" : "Add to watchlist"}
      onClick={(e) => {
        e.stopPropagation();
        toggleStar(symbol);
      }}
    >
      {on ? "★" : "☆"}
    </button>
  );
}
