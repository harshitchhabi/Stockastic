import { useEffect, useRef } from "react";

/** A number that washes green or vermilion for a moment when it moves up or down, then settles. */
export function Tick({ value, digits = 2, className = "" }: { value: number | null | undefined; digits?: number; className?: string }) {
  const prev = useRef(value);
  const dir =
    value != null && prev.current != null && value !== prev.current ? (value > prev.current ? "tick-up" : "tick-down") : "";
  useEffect(() => {
    prev.current = value;
  });
  return (
    <span key={value ?? "none"} className={`mono ${dir} ${className}`}>
      {value != null ? value.toFixed(digits) : "—"}
    </span>
  );
}
