import { useEffect, useRef } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { Fill } from "@/lib/types";

export function PriceChart({ symbol }: { symbol: string }) {
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let disposed = false;
    let chart: import("lightweight-charts").IChartApi | undefined;
    let series: import("lightweight-charts").ISeriesApi<"Line"> | undefined;

    async function init() {
      const { createChart, ColorType } = await import("lightweight-charts");
      if (disposed || !containerRef.current) return;

      chart = createChart(containerRef.current, {
        layout: { background: { type: ColorType.Solid, color: "#121722" }, textColor: "#8a93a6" },
        grid: { vertLines: { color: "#1c2431" }, horzLines: { color: "#1c2431" } },
        width: containerRef.current.clientWidth,
        height: containerRef.current.clientHeight,
        timeScale: { timeVisible: true, secondsVisible: true },
      });
      series = chart.addLineSeries({ color: "#4a8cff", lineWidth: 2 });

      const history = await api.get<{ price: number; timestamp: number }[]>(
        `/api/symbols/${symbol}/history`
      );
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
    <div className="panel" style={{ minHeight: 260 }}>
      <div className="panel-header">Price · {symbol}</div>
      <div className="panel-body" style={{ padding: 0 }}>
        <div ref={containerRef} style={{ width: "100%", height: "100%" }} />
      </div>
    </div>
  );
}
