import { useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import type { ControlStateSnapshot } from "@/lib/useControlState";
import type { NewsItem } from "@/lib/types";

/**
 * "The wire": everything happening in the event, newest first — market news plus desk notices
 * (trading frozen/resumed, allocation windows opened/closed by the organiser).
 *
 * One component serves everyone. The two-tier timing (fund managers first, the public after the
 * configured delay) lives entirely on the server: it delivers the same `news` event to each socket at
 * its own time, so a fund manager's wire simply shows items earlier.
 */
interface Entry {
  id: string;
  kind: "news" | "notice";
  headline: string;
  body?: string;
  at: number;
}

const MAX_ENTRIES = 100;

function merge(prev: Entry[], incoming: Entry[]): Entry[] {
  const seen = new Set(prev.map((e) => e.id));
  const fresh = incoming.filter((e) => !seen.has(e.id));
  if (fresh.length === 0) return prev;
  return [...fresh, ...prev].sort((a, b) => b.at - a.at).slice(0, MAX_ENTRIES);
}

const fromNews = (n: NewsItem): Entry => ({ id: n.id, kind: n.kind === "notice" ? "notice" : "news", headline: n.headline, body: n.body, at: n.createdAt });

export function NewsFeed({ title = "The wire" }: { title?: string }) {
  const [entries, setEntries] = useState<Entry[]>([]);
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const control = useRef<ControlStateSnapshot | null>(null);

  useEffect(() => {
    const socket = getSocket();

    const add = (list: Entry[], live: boolean) => {
      setEntries((prev) => merge(prev, list));
      if (!live) return;
      setFresh((f) => new Set([...f, ...list.map((e) => e.id)]));
      setTimeout(() => setFresh((f) => new Set([...f].filter((id) => !list.some((e) => e.id === id)))), 4000);
    };

    // History, and again after every reconnect so nothing missed while offline stays missing.
    const loadHistory = () =>
      api
        .get<NewsItem[]>("/api/news")
        .then((items) => add(items.map(fromNews), false))
        .catch(() => {});
    loadHistory();

    const onNews = (item: NewsItem) => add([fromNews(item)], true);

    // The first snapshot after connecting is only the baseline; later differences are events.
    const onControl = (next: ControlStateSnapshot) => {
      const prev = control.current;
      control.current = next;
      if (!prev) return;
      const at = Date.now();
      if (next.tradingFrozen !== prev.tradingFrozen) {
        add(
          [
            {
              id: `notice-freeze-${at}`,
              kind: "notice",
              headline: next.tradingFrozen ? "Trading frozen by the organisers" : "Trading resumed",
              at,
            },
          ],
          true
        );
      }
      for (const w of new Set([...Object.keys(prev.windowOverrides), ...Object.keys(next.windowOverrides)])) {
        if (prev.windowOverrides[w] !== next.windowOverrides[w] && next.windowOverrides[w]) {
          add(
            [{ id: `notice-${w}-${at}`, kind: "notice", headline: `${w} ${next.windowOverrides[w] === "open" ? "opened" : "closed"} by the organisers`, at }],
            true
          );
        }
      }
    };

    socket.on("news", onNews);
    socket.on("controlState", onControl);
    socket.on("connect", loadHistory);
    return () => {
      socket.off("news", onNews);
      socket.off("controlState", onControl);
      socket.off("connect", loadHistory);
    };
  }, []);

  return (
    <div className="panel" style={{ flex: 1 }}>
      <div className="panel-header">{title}</div>
      <div className="panel-body" style={{ padding: 0 }}>
        {entries.length === 0 && <div className="empty">nothing yet. Events appear here as they happen</div>}
        {entries.map((e) => (
          <article key={e.id} className={`wire-item ${e.kind} ${fresh.has(e.id) ? "tick-up" : ""}`}>
            <div className="label">
              {e.kind === "notice" ? "Desk notice" : "News"} · <span className="mono">{new Date(e.at).toLocaleTimeString()}</span>
            </div>
            <div className="hl">{e.headline}</div>
            {e.body && <div className="dim">{e.body}</div>}
          </article>
        ))}
      </div>
    </div>
  );
}
