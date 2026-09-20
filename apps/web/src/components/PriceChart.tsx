import { useEffect, useRef } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { Fill } from "@/lib/types";
import { cssVar } from "@/lib/theme";
import type { ColorType } from "lightweight-charts";

const chartColors = () => ({
  layout: { background: { type: "solid" as unknown as ColorType.Solid, color: cssVar("--paper") }, textColor: cssVar("--ink-2"), fontFamily: cssVar("--mono") },
  grid: { vertLines: { color: cssVar("--rule") }, horzLines: { color: cssVar("--rule") } },
  rightPriceScale: { borderColor: cssVar("--rule-strong") },
  timeScale: { borderColor: cssVar("--rule-strong") },
});

export type HistoryPoint = { price: number; timestamp: number };

export function PriceChart({ symbol, onHistory }: { symbol: string; onHistory?: (h: HistoryPoint[]) => void }) {
  const onHistoryRef = useRef(onHistory);
  onHistoryRef.current = onHistory;
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let disposed = false;
    let chart: import("lightweight-charts").IChartApi | undefined;
    let series: import("lightweight-charts").ISeriesApi<"Line"> | undefined;

    async function init() {
      const { createChart, ColorType } = await import("lightweight-charts");
      if (disposed || !containerRef.current) return;

      chart = createChart(containerRef.current, {
        ...chartColors(),
        width: containerRef.current.clientWidth,
        height: containerRef.current.clientHeight,
        timeScale: { timeVisible: true, secondsVisible: true, borderColor: cssVar("--rule-strong") },
        crosshair: { mode: 0 },
      });
      series = chart.addLineSeries({ color: cssVar("--ink"), lineWidth: 2, priceLineColor: cssVar("--flag") });

      const history = await api.get<{ price: number; timestamp: number }[]>(
        `/api/symbols/${symbol}/history`
      );
      onHistoryRef.current?.(history);
      series.setData(
        history.map((h) => ({ time: Math.floor(h.timestamp / 1000) as import("lightweight-charts").UTCTimestamp, value: h.price }))
      );

      const resize = () => {
        if (containerRef.current && chart) {
          chart.applyOptions({ width: containerRef.current.clientWidth, height: containerRef.current.clientHeight });
        }
      };
      window.addEventListener("resize", resize);

      const socket = getSocket();
      socket.emit("subscribe:symbol", symbol);
      const onTrade = (fill: Fill) => {
        if (fill.symbol !== symbol || !series) return;
        series.update({
          time: Math.floor(fill.timestamp / 1000) as import("lightweight-charts").UTCTimestamp,
          value: fill.price,
        });
      };
      socket.on("trade", onTrade);

      return () => {
        window.removeEventListener("resize", resize);
        socket.off("trade", onTrade);
        socket.emit("unsubscribe:symbol", symbol);
      };
    }

    const cleanupPromise = init();

    return () => {
      disposed = true;
      cleanupPromise.then((cleanup) => cleanup?.());
      chart?.remove();
    };
  }, [symbol]);

  return (
    <div className="panel">
      <div className="panel-body" style={{ padding: 0 }}>
        <div ref={containerRef} style={{ width: "100%", height: "100%" }} />
      </div>
    </div>
  );
}
