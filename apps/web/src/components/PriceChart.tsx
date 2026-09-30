import { useEffect, useRef } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { PricesUpdate } from "@/lib/types";
import { cssVar } from "@/lib/theme";
import type { ColorType } from "lightweight-charts";

const chartColors = () => ({
  layout: { background: { type: "solid" as unknown as ColorType.Solid, color: cssVar("--paper") }, textColor: cssVar("--ink-2"), fontFamily: cssVar("--mono") },
  grid: { vertLines: { color: cssVar("--rule") }, horzLines: { color: cssVar("--rule") } },
  rightPriceScale: { borderColor: cssVar("--rule-strong") },
  timeScale: { borderColor: cssVar("--rule-strong") },
});

export type HistoryPoint = { price: number; timestamp: number };

type Time = import("lightweight-charts").UTCTimestamp;
// The chart library shows times in UTC. Shift each point by this computer's offset so the axis shows local time,
// the same clock as the news and everything else on screen.
const localTime = (ms: number) => (Math.floor(ms / 1000) - new Date(ms).getTimezoneOffset() * 60) as Time;

export function PriceChart({ symbol, onHistory }: { symbol: string; onHistory?: (h: HistoryPoint[]) => void }) {
  const onHistoryRef = useRef(onHistory);
  onHistoryRef.current = onHistory;
  const containerRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    let disposed = false;
    let chart: import("lightweight-charts").IChartApi | undefined;
    let series: import("lightweight-charts").ISeriesApi<"Line"> | undefined;

    async function init() {
      const { createChart } = await import("lightweight-charts");
      if (disposed || !containerRef.current) return;

      chart = createChart(containerRef.current, {
        ...chartColors(),
        width: containerRef.current.clientWidth,
        height: containerRef.current.clientHeight,
        timeScale: { timeVisible: true, secondsVisible: true, borderColor: cssVar("--rule-strong") },
        crosshair: { mode: 0 },
      });
      series = chart.addLineSeries({ color: cssVar("--ink"), lineWidth: 2, priceLineColor: cssVar("--flag") });

      // Load the history; if the server is busy or asks us to slow down, wait a moment and try again.
      let history: HistoryPoint[] | null = null;
      for (let attempt = 0; history === null && attempt < 5; attempt++) {
        try {
          history = await api.get<HistoryPoint[]>(`/api/symbols/${symbol}/history`);
        } catch {
          await new Promise((r) => setTimeout(r, 1000 * (attempt + 1)));
        }
        if (disposed) return; // the page moved on to another company while this was loading
      }
      if (disposed || !history) return;
      onHistoryRef.current?.(history);
      // One point per second at most (the library needs strictly increasing times): the latest price in a second wins.
      const points: { time: Time; value: number }[] = [];
      for (const h of history) {
        const time = localTime(h.timestamp);
        if (points.length && points[points.length - 1].time >= time) points[points.length - 1] = { time: points[points.length - 1].time, value: h.price };
        else points.push({ time, value: h.price });
      }
      series.setData(points);
      let last = points.length ? points[points.length - 1].time : 0;
      // Show the whole history across the chart's width, however short or long it is.
      chart.timeScale().fitContent();

      const resize = () => {
        if (containerRef.current && chart) {
          chart.applyOptions({ width: containerRef.current.clientWidth, height: containerRef.current.clientHeight });
        }
      };
      window.addEventListener("resize", resize);

      const socket = getSocket();
      const onPrices = (u: PricesUpdate) => {
        const p = u.prices.find((x) => x.symbol === symbol);
        if (!p || !series) return;
        const time = localTime(u.at);
        if (time < last) return; // an update older than what is shown (after a reconnect, say) is skipped
        last = time;
        series.update({ time, value: p.price });
      };
      socket.on("prices", onPrices);

      return () => {
        window.removeEventListener("resize", resize);
        socket.off("prices", onPrices);
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
