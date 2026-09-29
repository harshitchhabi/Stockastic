import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

export function LoginForm() {
  const { login, signup, join } = useSession();
  const [mode, setMode] = useState<"login" | "signup" | "join">("login");
  const [teamCode, setTeamCode] = useState("");
  const [yourName, setYourName] = useState("");
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

  // Google is the way in for players. Someone new chooses on the next screen to create a team or join one.
  function goGoogle() {
    const q = new URLSearchParams();
    if (eventCode.trim()) q.set("code", eventCode.trim());
    const qs = q.toString();
    window.location.href = "/api/auth/google/start" + (qs ? "?" + qs : "");
  }

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      if (mode === "signup") await signup(displayName, email, password, eventCode, yourName);
      else if (mode === "join") await join(teamCode, displayName, email, password, eventCode);
      else await login(email, password);
    } catch (err) {
      setError(err instanceof Error ? err.message : `${mode} failed`);
    } finally {
      setSubmitting(false);
    }
  }

  const googleButton = google && (
    <>
      <button type="button" className={googleOnly ? "solid" : undefined} style={{ padding: 11 }} onClick={() => goGoogle()}>
        Continue with Google
      </button>
      <div className="dim">New here? After Google, you create your team or join your team's.</div>
    </>
  );

  return (
    <div className="login-wrap">
      <div className="login-hero">
        <div>
          <h1>Stockastic</h1>
        </div>
      </div>

      {googleOnly ? (
        // Only Google can add people: one button for players, and a small email form for the organisers.
        <form onSubmit={submit} className="login-form">
          {googleButton}
          {error && <div className="down">{error}</div>}
          <details style={{ marginTop: 18 }}>
            <summary className="dim">Organisers</summary>
            <div className="stack" style={{ marginTop: 8 }}>
              <input placeholder="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
              <input placeholder="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} required />
              <button type="submit" disabled={submitting} style={{ padding: 9 }}>
                {submitting ? "…" : "Sign in"}
              </button>
            </div>
          </details>
        </form>
      ) : (
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
            {/* teammates can join an existing team even once registration is closed */}
            <button type="button" aria-pressed={mode === "join"} onClick={() => setMode("join")}>
              Join your team
            </button>
          </div>
          {mode === "login" && googleButton}
          {mode === "signup" && <div className="dim">One person registers the team and becomes its leader. The others then choose Join your team.</div>}
          {mode === "signup" && <input placeholder="Team name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} required />}
          {mode === "signup" && <input placeholder="Your name (team leader)" value={yourName} onChange={(e) => setYourName(e.target.value)} required minLength={2} />}
          {mode === "join" && <div className="dim">Ask your team leader for the team code (on their Team page).</div>}
          {mode === "join" && (
            <input placeholder="Team code" value={teamCode} onChange={(e) => setTeamCode(e.target.value)} required autoComplete="off" autoCapitalize="characters" />
          )}
          {mode === "join" && <input placeholder="Your name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} required />}
          {mode !== "login" && listed && <div className="dim">Use the email you registered for this event with the organisers.</div>}
          {mode !== "login" && needsCode && (
            <input placeholder="Event code (from the organisers)" value={eventCode} onChange={(e) => setEventCode(e.target.value)} required autoComplete="off" />
          )}
          <input placeholder="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
          <input placeholder="Password" type="password" minLength={mode !== "login" ? 8 : undefined} value={password} onChange={(e) => setPassword(e.target.value)} required />
          {error && <div className="down">{error}</div>}
          <button type="submit" className="solid" disabled={submitting} style={{ padding: 11 }}>
            {submitting ? "…" : mode === "signup" ? "Register team" : mode === "join" ? "Join team" : "Enter the floor"}
          </button>
        </form>
      )}
    </div>
  );
}
