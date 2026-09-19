"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import type { LeaderboardRow } from "@/lib/types";

// Fields are a stub (rank + portfolio value + % return only) — the Prize 3/4
// scoring rubric and tie-break logic are explicitly unfinalized in the
// rulebook, so nothing downstream of that is built yet.
const POLL_INTERVAL_MS = 5000;

export function Leaderboard() {
  const [rows, setRows] = useState<LeaderboardRow[]>([]);

  useEffect(() => {
    const load = () => api.get<LeaderboardRow[]>("/api/leaderboard").then(setRows);
    load();
    const interval = setInterval(load, POLL_INTERVAL_MS);
    return () => clearInterval(interval);
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
