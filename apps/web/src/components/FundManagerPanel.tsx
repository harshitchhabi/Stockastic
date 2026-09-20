import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";
import type { Fund } from "@/lib/types";

interface Breakdown {
  investorId: string;
  units: number;
}

/** Round 2, fund_manager role: NAV/units management + investor-in-fund breakdown. */
export function FundManagerPanel() {
  const { account } = useSession();
  const [myFund, setMyFund] = useState<Fund | null>(null);
  const [name, setName] = useState("");
  const [pitch, setPitch] = useState("");
  const [nav, setNav] = useState("");
  const [breakdown, setBreakdown] = useState<Breakdown[]>([]);

  async function loadFund() {
    if (!account) return;
    const funds = await api.get<Fund[]>("/api/funds");
    const mine = funds.find((f) => f.managerAccountId === account.id) ?? null;
    setMyFund(mine);
    if (mine) {
      setNav(String(mine.navPerUnit));
      const b = await api.get<Breakdown[]>(`/api/funds/${mine.id}/breakdown`);
      setBreakdown(b);
    }
  }

  useEffect(() => {
    loadFund();
  }, [account]);

  async function createFund(e: React.FormEvent) {
    e.preventDefault();
    if (!account) return;
    await api.post("/api/funds", { name, pitch, riskProfile: "TBD" });
    setName("");
    setPitch("");
    loadFund();
  }

  async function updateNav(e: React.FormEvent) {
    e.preventDefault();
    if (!myFund) return;
    await api.post(`/api/funds/${myFund.id}/nav`, { navPerUnit: Number(nav) });
    loadFund();
  }

  return (
    <div className="panel">
      <div className="panel-header">Fund Manager Ops</div>
      <div className="panel-body">
        {!myFund && (
          <form onSubmit={createFund} style={{ display: "flex", flexDirection: "column", gap: 6 }}>
            <div style={{ color: "var(--text-dim)", fontSize: 12 }}>No fund yet — create one.</div>
            <input placeholder="Fund name" value={name} onChange={(e) => setName(e.target.value)} required />
            <input placeholder="Pitch" value={pitch} onChange={(e) => setPitch(e.target.value)} />
            <button type="submit">Create fund</button>
          </form>
        )}

        {myFund && (
          <>
            <div style={{ fontWeight: 600, marginBottom: 4 }}>{myFund.name}</div>
            <form onSubmit={updateNav} style={{ display: "flex", gap: 6, marginBottom: 12 }}>
              <input type="number" step="0.01" value={nav} onChange={(e) => setNav(e.target.value)} />
              <button type="submit">Update NAV/unit</button>
            </form>

            <div style={{ color: "var(--text-dim)", fontSize: 11, marginBottom: 4 }}>
              Investor breakdown
            </div>
            <table>
              <thead>
                <tr>
                  <th>Investor</th>
                  <th>Units</th>
                </tr>
              </thead>
              <tbody>
                {breakdown.map((b) => (
                  <tr key={b.investorId}>
                    <td>{b.investorId.slice(0, 8)}…</td>
                    <td className="mono">{b.units}</td>
                  </tr>
                ))}
                {breakdown.length === 0 && (
                  <tr>
                    <td colSpan={2} style={{ color: "var(--text-dim)", textAlign: "center" }}>
                      no investors yet
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </>
        )}
      </div>
    </div>
  );
}
