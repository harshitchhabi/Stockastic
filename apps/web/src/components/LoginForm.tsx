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
  const [eventCode, setEventCode] = useState("");

  // Registration can be closed by the organisers: then there is only the log in form.
  useEffect(() => {
    api
      .get<{ signupOpen: boolean; signupNeedsCode?: boolean; signupListed?: boolean }>("/api/status")
      .then((s) => {
        setSignupOpen(s.signupOpen);
        setNeedsCode(s.signupNeedsCode === true);
        setListed(s.signupListed === true);
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
        <button type="submit" className="solid" disabled={submitting} style={{ padding: 11 }}>
          {submitting ? "…" : mode === "signup" ? "Create account" : "Enter the floor"}
        </button>
      </form>
    </div>
  );
}
