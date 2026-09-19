"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { Fund } from "@/lib/types";

interface PublicConfig {
  windowsOpen: { fundAllocationWindow: boolean };
}

/** Round 2, investor role. Field set on Fund is a stub — pitch/NAV/risk-profile shape is TBD. */
export function FundBrowser() {
  const { account } = useSession();
  const [funds, setFunds] = useState<Fund[]>([]);
  const [windowOpen, setWindowOpen] = useState(false);
  const [units, setUnits] = useState<Record<string, string>>({});
  const [message, setMessage] = useState<string | null>(null);

  useEffect(() => {
    api.get<Fund[]>("/api/funds").then(setFunds);
    api.get<PublicConfig>("/api/config").then((c) => setWindowOpen(c.windowsOpen.fundAllocationWindow));
  }, []);

  async function act(fundId: string, action: "allocate" | "redeem") {
    if (!account) return;
    setMessage(null);
    try {
      await api.post(`/api/funds/${fundId}/${action}`, {
        units: Number(units[fundId] ?? 0),
      });
      setMessage(`${action} submitted`);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : `${action} failed`);
    }
  }

  return (
    <div className="panel">
      <div className="panel-header">
        Fund Browser
        {!windowOpen && (
          <span style={{ color: "var(--text-dim)", fontWeight: 400, fontSize: 11 }}>
            allocation window closed
          </span>
        )}
      </div>
      <div className="panel-body">
        {message && <div style={{ marginBottom: 8, color: "var(--text-dim)" }}>{message}</div>}
        {funds.map((f) => (
          <div key={f.id} style={{ borderBottom: "1px solid var(--panel-border)", padding: "8px 0" }}>
            <div style={{ fontWeight: 600 }}>{f.name}</div>
            <div style={{ color: "var(--text-dim)", fontSize: 12 }}>{f.pitch || "no pitch yet"}</div>
            <div style={{ fontSize: 11, color: "var(--text-dim)" }}>
              NAV/unit {f.navPerUnit.toFixed(2)} · risk: {f.riskProfile} · units {f.totalUnits}
            </div>
            <div style={{ display: "flex", gap: 4, marginTop: 4 }}>
              <input
                type="number"
                min="0"
                placeholder="units"
                style={{ width: 80 }}
                value={units[f.id] ?? ""}
                onChange={(e) => setUnits((prev) => ({ ...prev, [f.id]: e.target.value }))}
                disabled={!windowOpen}
              />
              <button onClick={() => act(f.id, "allocate")} disabled={!windowOpen}>
                Allocate
              </button>
              <button onClick={() => act(f.id, "redeem")} disabled={!windowOpen}>
                Redeem
              </button>
            </div>
          </div>
        ))}
        {funds.length === 0 && <div style={{ color: "var(--text-dim)" }}>no funds yet</div>}
      </div>
    </div>
  );
}
