"use client";

import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

interface AdminState {
  tradingFrozen: boolean;
  windowOverrides: Record<string, "open" | "closed" | undefined>;
  windowsEffective: Record<string, boolean>;
}

interface AdminAccount {
  id: string;
  displayName: string;
  email: string;
  role: string;
  isAdmin: boolean;
  cashBalance: number;
}

const WINDOWS = ["tradingRound1", "fundAllocationWindow", "tradingRound2"] as const;

export function AdminConsole() {
  const { account, logout } = useSession();
  const [state, setState] = useState<AdminState | null>(null);
  const [accounts, setAccounts] = useState<AdminAccount[]>([]);
  const [headline, setHeadline] = useState("");
  const [body, setBody] = useState("");
  const [fillId, setFillId] = useState("");
  const [reason, setReason] = useState("");
  const [adjustmentNote, setAdjustmentNote] = useState("");
  const [message, setMessage] = useState<string | null>(null);

  const loadState = () => api.get<AdminState>("/api/admin/state").then(setState);
  const loadAccounts = () => api.get<AdminAccount[]>("/api/admin/accounts").then(setAccounts);

  useEffect(() => {
    loadState();
    loadAccounts();
  }, []);

  async function toggleFreeze() {
    if (state?.tradingFrozen) await api.post("/api/admin/unfreeze");
    else await api.post("/api/admin/freeze");
    loadState();
  }

  async function setWindow(name: string, action: "open" | "close" | "reset") {
    await api.post(`/api/admin/windows/${name}/${action}`);
    loadState();
  }

  async function publishNews(e: React.FormEvent) {
    e.preventDefault();
    await api.post("/api/admin/news", { headline, body: body || undefined });
    setHeadline("");
    setBody("");
    setMessage("News dispatched to fund-manager queue now, public queue after the configured delay.");
  }

  async function promote(id: string) {
    await api.post(`/api/admin/accounts/${id}/promote`);
    loadAccounts();
  }

  async function submitAdjustment(e: React.FormEvent) {
    e.preventDefault();
    await api.post("/api/admin/trade-adjustments", {
      fillId,
      reason,
      adjustment: { note: adjustmentNote },
    });
    setMessage(`Correction recorded against fill ${fillId} (fill itself is unchanged).`);
    setFillId("");
    setReason("");
    setAdjustmentNote("");
  }

  return (
    <div style={{ padding: 16, display: "flex", flexDirection: "column", gap: 16, maxWidth: 900 }}>
      <div style={{ display: "flex", alignItems: "center", gap: 12 }}>
        <h2 style={{ margin: 0 }}>Admin Console</h2>
        <span style={{ color: "var(--text-dim)" }}>{account?.displayName}</span>
        <a href="/" style={{ marginLeft: "auto" }}>
          Back to terminal
        </a>
        <button onClick={logout}>Sign out</button>
      </div>

      {message && (
        <div className="panel" style={{ padding: 8, color: "var(--text-dim)" }}>
          {message}
        </div>
      )}

      <div className="panel" style={{ padding: 12 }}>
        <div className="panel-header" style={{ padding: 0, border: "none" }}>
          Force-freeze trading
        </div>
        <p style={{ color: "var(--text-dim)", fontSize: 12 }}>
          Rejects order submissions inside the matching engine itself the instant it&apos;s triggered —
          including anything already queued — not just a UI flag.
        </p>
        <button
          onClick={toggleFreeze}
          style={{
            background: state?.tradingFrozen ? "var(--green)" : "var(--red)",
            color: "#000",
            border: "none",
            borderRadius: 4,
            padding: "8px 16px",
            fontWeight: 700,
          }}
        >
          {state?.tradingFrozen ? "Unfreeze trading" : "FREEZE TRADING"}
        </button>
      </div>

      <div className="panel" style={{ padding: 12 }}>
        <div className="panel-header" style={{ padding: 0, border: "none" }}>
          Windows
        </div>
        <table>
          <thead>
            <tr>
              <th>Window</th>
              <th>Override</th>
              <th>Effective</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {WINDOWS.map((name) => (
              <tr key={name}>
                <td>{name}</td>
                <td className="mono">{state?.windowOverrides[name] ?? "none (config)"}</td>
                <td className={state?.windowsEffective[name] ? "up" : "down"}>
                  {state?.windowsEffective[name] ? "open" : "closed"}
                </td>
                <td style={{ display: "flex", gap: 4 }}>
                  <button onClick={() => setWindow(name, "open")}>Open</button>
                  <button onClick={() => setWindow(name, "close")}>Close</button>
                  <button onClick={() => setWindow(name, "reset")}>Reset</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="panel" style={{ padding: 12 }}>
        <div className="panel-header" style={{ padding: 0, border: "none" }}>
          Trigger news release
        </div>
        <form onSubmit={publishNews} style={{ display: "flex", flexDirection: "column", gap: 6 }}>
          <input placeholder="Headline" value={headline} onChange={(e) => setHeadline(e.target.value)} required />
          <input placeholder="Body (optional)" value={body} onChange={(e) => setBody(e.target.value)} />
          <button type="submit" style={{ alignSelf: "flex-start" }}>
            Dispatch
          </button>
        </form>
      </div>

      <div className="panel" style={{ padding: 12 }}>
        <div className="panel-header" style={{ padding: 0, border: "none" }}>
          Accounts
        </div>
        <table>
          <thead>
            <tr>
              <th>Name</th>
              <th>Email</th>
              <th>Role</th>
              <th>Cash</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {accounts.map((a) => (
              <tr key={a.id}>
                <td>{a.displayName}</td>
                <td>{a.email}</td>
                <td>{a.role}</td>
                <td className="mono">{a.cashBalance.toFixed(2)}</td>
                <td>
                  {a.role === "investor" && <button onClick={() => promote(a.id)}>Promote</button>}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      <div className="panel" style={{ padding: 12 }}>
        <div className="panel-header" style={{ padding: 0, border: "none" }}>
          Trade correction (dispute resolution)
        </div>
        <p style={{ color: "var(--text-dim)", fontSize: 12 }}>
          Records a correction against a fill via trade_adjustments — the fill itself stays immutable
          (trades are final; there is no client-facing reversal).
        </p>
        <form onSubmit={submitAdjustment} style={{ display: "flex", flexDirection: "column", gap: 6 }}>
          <input placeholder="Fill id (e.g. ACME-fill-1)" value={fillId} onChange={(e) => setFillId(e.target.value)} required />
          <input placeholder="Reason" value={reason} onChange={(e) => setReason(e.target.value)} required />
          <input
            placeholder="Adjustment note"
            value={adjustmentNote}
            onChange={(e) => setAdjustmentNote(e.target.value)}
          />
          <button type="submit" style={{ alignSelf: "flex-start" }}>
            Record correction
          </button>
        </form>
      </div>
    </div>
  );
}
