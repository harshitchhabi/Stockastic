import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";

type Member = { id: string; name: string; email?: string; trader: boolean; joinedAt: number };
type TeamView = {
  teamName: string;
  isLeader: boolean;
  youTrade: boolean;
  leaderTrades: boolean;
  traderName: string;
  joinCode?: string;
  teamSize: number;
  members: Member[];
};

/**
 * The team: who is in it, who trades for it, and (for the leader) the code teammates use to join. Everyone in a
 * team sees the same portfolio; only one person places the team's trades.
 */
export function TeamPage() {
  const { account, refresh } = useSession();
  const [team, setTeam] = useState<TeamView | null>(null);
  const [message, setMessage] = useState<{ ok: boolean; text: string } | null>(null);
  const [busy, setBusy] = useState(false);

  const load = useCallback(() => {
    api
      .get<TeamView>("/api/team")
      .then(setTeam)
      .catch(() => {});
  }, []);
  useEffect(load, [load]);
  useEffect(() => {
    const socket = getSocket();
    socket.on("portfolio", load); // someone joined, or who trades changed
    return () => socket.off("portfolio", load);
  }, [load]);

  async function run(f: () => Promise<unknown>, ok: string) {
    setBusy(true);
    setMessage(null);
    try {
      await f();
      setMessage({ ok: true, text: ok });
      load();
      void refresh();
    } catch (err) {
      setMessage({ ok: false, text: err instanceof Error ? err.message : "That did not work." });
    } finally {
      setBusy(false);
    }
  }

  if (!team || !account) return <div className="page empty">loading…</div>;
  const places = team.teamSize - 1 - team.members.length;

  return (
    <div className="page">
      <div className="page-head">
        <h1>{team.teamName}</h1>
        <span className="chip">{team.youTrade ? "You trade for the team" : `${team.traderName} trades for the team`}</span>
      </div>
      <p className="dim">
        Everyone in the team sees the same portfolio, prices and news on their own screen. Only one person places the team's trades and moves its money
        in and out of funds.
      </p>

      {team.isLeader && team.joinCode && (
        <>
          <h2 className="section">Invite your teammates</h2>
          <p>
            Give them this team code. They choose <strong>Join your team</strong> on the sign-in page and enter it with their own name, email and password.
          </p>
          <div className="figures">
            <div className="figure">
              <div className="label">Team code</div>
              <div className="v mono" style={{ letterSpacing: "0.15em" }}>
                {team.joinCode}
              </div>
            </div>
            <div className="figure">
              <div className="label">Places left</div>
              <div className="v">{Math.max(places, 0)}</div>
            </div>
          </div>
          <button disabled={busy} onClick={() => run(() => api.post("/api/team/code", {}), "New code made. The old one no longer works.")}>
            Make a new code
          </button>
        </>
      )}

      <h2 className="section">Team</h2>
      <table className="roomy">
        <thead>
          <tr>
            <th style={{ textAlign: "left" }}>Name</th>
            {team.isLeader && <th style={{ textAlign: "left" }}>Email</th>}
            <th>Trades</th>
            {team.isLeader && <th />}
          </tr>
        </thead>
        <tbody>
          <tr>
            <td style={{ textAlign: "left" }}>
              {team.teamName} <span className="label">· team leader</span>
            </td>
            {team.isLeader && <td style={{ textAlign: "left" }}>{account.email}</td>}
            <td>{team.leaderTrades ? "Yes" : ""}</td>
            {team.isLeader && (
              <td>
                {!team.leaderTrades && (
                  <button disabled={busy} onClick={() => run(() => api.post("/api/team/trader", { memberId: "" }), "You trade for the team again.")}>
                    Trade myself
                  </button>
                )}
              </td>
            )}
          </tr>
          {team.members.map((m) => (
            <tr key={m.id}>
              <td style={{ textAlign: "left" }}>{m.name}</td>
              {team.isLeader && <td style={{ textAlign: "left" }}>{m.email}</td>}
              <td>{m.trader ? "Yes" : ""}</td>
              {team.isLeader && (
                <td>
                  {!m.trader && (
                    <button disabled={busy} onClick={() => run(() => api.post("/api/team/trader", { memberId: m.id }), `${m.name} now trades for the team.`)}>
                      Let {m.name} trade
                    </button>
                  )}
                </td>
              )}
            </tr>
          ))}
        </tbody>
      </table>
      {message && <div className={message.ok ? "up" : "down"}>{message.text}</div>}
      {!team.isLeader && <p className="dim">Only the team leader can change who trades. An organiser can help if the leader is away.</p>}
    </div>
  );
}
