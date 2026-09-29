import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

/** Right after Google, for someone new: create a team (and lead it), or join a team with its code. */
export function Onboard() {
  const { onboarding, onboard, cancelOnboarding } = useSession();
  const [teamName, setTeamName] = useState("");
  const [teamCode, setTeamCode] = useState("");
  const [eventCode, setEventCode] = useState("");
  const [needsCode, setNeedsCode] = useState(false);
  const [busy, setBusy] = useState<"create" | "join" | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    api
      .get<{ signupNeedsCode?: boolean }>("/api/status")
      .then((s) => setNeedsCode(s.signupNeedsCode === true))
      .catch(() => {});
  }, []);

  if (!onboarding) return null;

  async function go(action: "create" | "join") {
    setError(null);
    setBusy(action);
    try {
      await onboard(action, action === "create" ? teamName.trim() : teamCode.trim(), eventCode.trim() || undefined);
    } catch (err) {
      setError(err instanceof Error ? err.message : "That did not work. Try again.");
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="login-wrap">
      <div className="login-hero">
        <div>
          <h1>Stockastic</h1>
        </div>
      </div>
      <div className="login-form">
        <h2 style={{ margin: 0 }}>Welcome{onboarding.name ? `, ${onboarding.name}` : ""}</h2>
        <div className="dim">Signed in with Google as {onboarding.email}.</div>
        {needsCode && (
          <input placeholder="Event code (from the organisers)" value={eventCode} onChange={(e) => setEventCode(e.target.value)} autoComplete="off" />
        )}

        <h3 style={{ margin: "14px 0 0" }}>Create a team</h3>
        <div className="dim">You become the team leader. Your teammates join with the code on your Team page.</div>
        <input placeholder="Team name" value={teamName} onChange={(e) => setTeamName(e.target.value)} maxLength={40} />
        <button className="solid" style={{ padding: 11 }} disabled={busy !== null || teamName.trim().length < 2 || (needsCode && !eventCode.trim())} onClick={() => go("create")}>
          {busy === "create" ? "…" : "Create team"}
        </button>

        <h3 style={{ margin: "14px 0 0" }}>Join a team</h3>
        <div className="dim">Ask your team leader for the team code.</div>
        <input placeholder="Team code" value={teamCode} onChange={(e) => setTeamCode(e.target.value)} autoComplete="off" autoCapitalize="characters" />
        <button style={{ padding: 11 }} disabled={busy !== null || !teamCode.trim() || (needsCode && !eventCode.trim())} onClick={() => go("join")}>
          {busy === "join" ? "…" : "Join team"}
        </button>

        {error && <div className="down">{error}</div>}
        <button className="ghost" onClick={cancelOnboarding}>
          Back to sign in
        </button>
      </div>
    </div>
  );
}
