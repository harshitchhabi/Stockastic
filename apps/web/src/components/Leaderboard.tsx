import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { LeaderboardRow, PublicConfig } from "@/lib/types";

// Rulebook Sec 15: the leaderboard is a periodic snapshot (refreshed every rulebook.leaderboard
// refreshSeconds on the server), NOT a live feed — that is a deliberate anti-copy-trading and load
// choice. The server enforces the cadence; the client just picks up each new snapshot promptly by
// polling a cheap cached endpoint at most every 30s, with jitter so ~750 clients don't fire together.
const MAX_POLL_MS = 30_000;

export function Leaderboard() {
  const [rows, setRows] = useState<LeaderboardRow[]>([]);

  useEffect(() => {
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    let pollMs = MAX_POLL_MS;
    const load = () => api.get<LeaderboardRow[]>("/api/leaderboard").then((r) => !stopped && setRows(r)).catch(() => {});
    const schedule = () => {
      timer = setTimeout(() => {
        load().finally(() => !stopped && schedule());
      }, pollMs * (0.85 + Math.random() * 0.3));
    };
    api
      .get<PublicConfig>("/api/config")
      .then((c) => {
        pollMs = Math.min(MAX_POLL_MS, c.leaderboard.refreshSeconds * 1000);
      })
      .catch(() => {})
      .finally(() => {
        load();
        schedule();
      });
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, []);

  return (
    <div className="panel">
      <div className="panel-header">Leaderboard</div>
      <div className="panel-body">
        <table>
          <thead>
            <tr>
              <th>#</th>
              <th>Name</th>
              <th>Value</th>
              <th>Return</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r) => (
              <tr key={r.accountId}>
                <td>{r.rank}</td>
                <td>{r.displayName}</td>
                <td className="mono">{r.portfolioValue.toFixed(2)}</td>
                <td className={`mono ${r.percentReturn >= 0 ? "up" : "down"}`}>
                  {r.percentReturn.toFixed(2)}%
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  );
}
