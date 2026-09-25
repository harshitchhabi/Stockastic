import { useEffect, useState } from "react";
import { api } from "@/lib/api";
import { useSession } from "@/lib/session";

export function LoginForm() {
  const { login, signup } = useSession();
  const [mode, setMode] = useState<"login" | "signup">("login");
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [signupOpen, setSignupOpen] = useState(true);
  const [needsCode, setNeedsCode] = useState(false);
  const [listed, setListed] = useState(false);
  const [google, setGoogle] = useState(false);

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
      failed: "Sign-in did not work. Try again.",
    };
    setError(text[reason] ?? "Sign-in did not work. Try again.");
  }, []);
  const [eventCode, setEventCode] = useState("");

  // Registration can be closed by the organisers: then there is only the log in form.
  useEffect(() => {
    api
      .get<{ signupOpen: boolean; signupNeedsCode?: boolean; signupListed?: boolean; googleEnabled?: boolean }>("/api/status")
      .then((s) => {
        setSignupOpen(s.signupOpen);
        setNeedsCode(s.signupNeedsCode === true);
        setListed(s.signupListed === true);
        setGoogle(s.googleEnabled === true);
        if (!s.signupOpen) setMode("login");
      })
      .catch(() => {});
  }, []);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      if (mode === "signup") await signup(displayName, email, password, eventCode);
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
              Sign up
            </button>
          )}
        </div>
        {mode === "signup" && (
          <input placeholder="Display name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} required />
        )}
        {mode === "signup" && listed && <div className="dim">Use the email you registered for this event with the organisers.</div>}
        {mode === "signup" && needsCode && (
          <input placeholder="Event code (from the organisers)" value={eventCode} onChange={(e) => setEventCode(e.target.value)} required autoComplete="off" />
        )}
        <input placeholder="Email" type="email" value={email} onChange={(e) => setEmail(e.target.value)} required />
        <input
          placeholder="Password"
          type="password"
          minLength={mode === "signup" ? 8 : undefined}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
        {error && <div className="down">{error}</div>}
        {google && (
          <>
            {needsCode && mode === "login" && (
              <input placeholder="Event code (only needed the first time)" value={eventCode} onChange={(e) => setEventCode(e.target.value)} autoComplete="off" />
            )}
            <button
              type="button"
              style={{ padding: 11 }}
              onClick={() => {
                window.location.href = "/api/auth/google/start" + (eventCode.trim() ? `?code=${encodeURIComponent(eventCode.trim())}` : "");
              }}
            >
              Continue with Google
            </button>
          </>
        )}
        <button type="submit" className="solid" disabled={submitting} style={{ padding: 11 }}>
          {submitting ? "…" : mode === "signup" ? "Create account" : "Enter the floor"}
        </button>
      </form>
    </div>
  );
}
