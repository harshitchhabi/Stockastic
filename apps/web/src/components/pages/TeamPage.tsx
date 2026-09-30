import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { useSession } from "@/lib/session";

type Member = { id: string; name: string; email?: string; trader: boolean; joinedAt: number; online: boolean };
type TeamView = {
  leaderOnline: boolean;
  teamName: string;
  leaderName: string;
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
  const [myName, setMyName] = useState("");

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
    const t = setInterval(load, 10_000); // who is online
    return () => {
      socket.off("portfolio", load);
      clearInterval(t);
    };
  }, [load]);

  async function run(f: () => Promise<unknown>, ok: string) {
    if (busy) return; // one at a time: a second click while the first is on its way does nothing
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
            <th style={{ textAlign: "left" }}>Online</th>
            {team.isLeader && <th style={{ textAlign: "left" }}>Email</th>}
            <th>Trades</th>
            {team.isLeader && <th />}
          </tr>
        </thead>
        <tbody>
          <tr>
            <td style={{ textAlign: "left" }}>
              {team.leaderName || (team.isLeader ? "You" : "Team leader")} <span className="label">· team leader</span>
            </td>
            <td style={{ textAlign: "left" }}>
              <Online on={team.leaderOnline} />
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
              <td style={{ textAlign: "left" }}>
                <Online on={m.online} />
              </td>
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
      {team.isLeader && !team.leaderName && (
        <form
          className="row-field"
          style={{ marginTop: 16, maxWidth: 420 }}
          onSubmit={(e) => {
            e.preventDefault();
            void run(() => api.post("/api/team/leader-name", { name: myName }), "Saved.");
          }}
        >
          <input placeholder="Your name, shown to your teammates" value={myName} onChange={(e) => setMyName(e.target.value)} minLength={2} maxLength={40} required />
          <button type="submit" disabled={busy}>
            Save
          </button>
        </form>
      )}
    </div>
  );
}

function Online({ on }: { on: boolean }) {
  return (
    <span className={on ? "up" : "dim"}>
      <span className={`dot-status ${on ? "on" : ""}`} />
      {on ? "Online" : "Offline"}
    </span>
  );
}
