import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

export function LoginForm() {
  const { login, signup, join } = useSession();
  const [mode, setMode] = useState<"login" | "signup" | "join">("login");
  const [teamCode, setTeamCode] = useState("");
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [signupOpen, setSignupOpen] = useState(true);
  const [needsCode, setNeedsCode] = useState(false);
  const [listed, setListed] = useState(false);
  const [google, setGoogle] = useState(false);
  const [googleOnly, setGoogleOnly] = useState(false);

  // Where "Continue with Google" sent us back with a problem.
  useEffect(() => {
    const h = window.location.hash;
    if (!h.startsWith("#/signin-error=")) return;
    const reason = decodeURIComponent(h.slice("#/signin-error=".length));
    window.history.replaceState(null, "", window.location.pathname + window.location.search);
    const text: Record<string, string> = {
      cancelled: "You cancelled the Google sign-in.",
      expired: "That sign-in took too long or was not started here. Try again.",
      google: "Google could not confirm who you are. Try again.",
      unverified: "Google says that email is not verified.",
      domain: "That email is not from an address allowed for this event.",
      locked: "This account is locked. Ask an organiser.",
      not_on_list: "That email is not on the list of people registered for this event. Ask an organiser.",
      code: "A new account needs the event code. Enter it and try again.",
      closed: "Registration is closed.",
      full: "Registration is full. Ask an organiser.",
      team_code: "That team code is not right. Ask your team leader for it and try again.",
      team_full: "That team already has all its members.",
      failed: "Sign-in did not work. Try again.",
    };
    setError(text[reason] ?? "Sign-in did not work. Try again.");
  }, []);
  const [eventCode, setEventCode] = useState("");

  // Registration can be closed by the organisers: then there is only the log in form.
  useEffect(() => {
    api
      .get<{ signupOpen: boolean; signupNeedsCode?: boolean; signupListed?: boolean; googleEnabled?: boolean; googleOnlySignup?: boolean }>("/api/status")
      .then((s) => {
        setSignupOpen(s.signupOpen);
        setNeedsCode(s.signupNeedsCode === true);
        setListed(s.signupListed === true);
        setGoogle(s.googleEnabled === true);
        setGoogleOnly(s.googleOnlySignup === true);
        if (!s.signupOpen) setMode("login");
      })
      .catch(() => {});
  }, []);

  // Registering or joining with a password is off when only Google may add people; signing in with one never is
  // (the organisers' accounts use passwords).
  const passwordForm = mode === "login" || !googleOnly;

  function goGoogle(extra: Record<string, string>) {
    const q = new URLSearchParams(extra);
    if (eventCode.trim()) q.set("code", eventCode.trim());
    const qs = q.toString();
    window.location.href = "/api/auth/google/start" + (qs ? "?" + qs : "");
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      if (mode === "signup") await signup(displayName, email, password, eventCode);
      else if (mode === "join") await join(teamCode, displayName, email, password, eventCode);
      else await login(email, password);
    } catch (err) {
      setError(err instanceof Error ? err.message : `${mode} failed`);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="login-wrap">
      <div className="login-hero">
        <div>
          <h1>Stockastic</h1>
        </div>
      </div>

      <form onSubmit={submit} className="login-form">
        <div className="seg">
          <button type="button" aria-pressed={mode === "login"} onClick={() => setMode("login")}>
            Log in
          </button>
          {signupOpen && (
            <button type="button" aria-pressed={mode === "signup"} onClick={() => setMode("signup")}>
              Register team
            </button>
          )}
          {signupOpen && (
            <button type="button" aria-pressed={mode === "join"} onClick={() => setMode("join")}>
              Join your team
            </button>
          )}
        </div>
        {mode === "signup" && <div className="dim">One person registers the team and becomes its leader. The others then choose Join your team.</div>}
        {mode === "signup" && (
          <input placeholder="Team name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} required />
        )}
        {mode === "join" && <div className="dim">Ask your team leader for the team code (on their Team page).</div>}
        {mode === "join" && (
          <input placeholder="Team code" value={teamCode} onChange={(e) => setTeamCode(e.target.value)} required autoComplete="off" autoCapitalize="characters" />
        )}
        {mode === "join" && passwordForm && <input placeholder="Your name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} required />}
        {mode !== "login" && listed && <div className="dim">Use the email you registered for this event with the organisers.</div>}
        {mode !== "login" && needsCode && (
          <input placeholder="Event code (from the organisers)" value={eventCode} onChange={(e) => setEventCode(e.target.value)} required autoComplete="off" />
        )}
        {passwordForm && <input placeholder="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />}
        {passwordForm && (
          <input
            placeholder="Password"
            type="password"
            minLength={mode !== "login" ? 8 : undefined}
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
        )}
        {error && <div className="down">{error}</div>}
        {google && mode === "login" && (
          <>
            {needsCode && (
              <input placeholder="Event code (only needed the first time)" value={eventCode} onChange={(e) => setEventCode(e.target.value)} autoComplete="off" />
            )}
            <button type="button" style={{ padding: 11 }} onClick={() => goGoogle({})}>
              Continue with Google
            </button>
            <div className="dim">New here? Choose Register team or Join your team first.</div>
          </>
        )}
        {google && mode === "signup" && (
          <button type="button" style={{ padding: 11 }} disabled={displayName.trim().length < 2 || (needsCode && !eventCode.trim())} onClick={() => goGoogle({ teamName: displayName.trim() })}>
            Register team with Google
          </button>
        )}
        {google && mode === "join" && (
          <button type="button" style={{ padding: 11 }} disabled={!teamCode.trim() || (needsCode && !eventCode.trim())} onClick={() => goGoogle({ team: teamCode.trim() })}>
            Join with Google
          </button>
        )}
        {google && mode !== "login" && !googleOnly && <div className="dim">Or fill in the fields above to use a password instead.</div>}
        {googleOnly && mode !== "login" && <div className="dim">Use your college Google account.</div>}
        {passwordForm && (
          <button type="submit" className="solid" disabled={submitting} style={{ padding: 11 }}>
            {submitting ? "…" : mode === "signup" ? "Register team" : mode === "join" ? "Join team" : "Enter the floor"}
          </button>
        )}
      </form>
    </div>
  );
}
