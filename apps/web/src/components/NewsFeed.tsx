import { useEffect, useState } from "react";
import { getSocket } from "@/lib/socket";
import type { NewsItem } from "@/lib/types";

/**
 * Same component serves both the public ticker and the fund-manager "early"
 * panel — the two-tier timing lives entirely on the backend (separate
 * dispatch queues, see apps/server/src/news/dispatcher.ts). The server joins
 * a fund_manager socket to `role:fund_manager` from its verified JWT at
 * connection time (never a client-asserted id), so that socket gets the
 * `news` event immediately; everyone else gets the same event after the
 * configured delay.
 */
export function NewsFeed({ title }: { title: string }) {
  const [items, setItems] = useState<NewsItem[]>([]);

  useEffect(() => {
    const socket = getSocket();
    const onNews = (item: NewsItem) => {
      setItems((prev) => [item, ...prev].slice(0, 20));
    };
    socket.on("news", onNews);
    return () => {
      socket.off("news", onNews);
    };
  }, []);

  return (
    <div className="panel">
      <div className="panel-header">{title}</div>
      <div className="panel-body">
        {items.length === 0 && <div style={{ color: "var(--text-dim)" }}>no news yet</div>}
        {items.map((item) => (
          <div key={item.id} style={{ marginBottom: 8 }}>
            <div style={{ fontWeight: 600 }}>{item.headline}</div>
            {item.body && <div style={{ color: "var(--text-dim)", fontSize: 12 }}>{item.body}</div>}
          </div>
        ))}
      </div>
    </div>
  );
}
