import { useState } from "react";
import { useSession } from "@/lib/session";

export function LoginForm() {
  const { login, signup } = useSession();
  const [mode, setMode] = useState<"login" | "signup">("login");
  const [displayName, setDisplayName] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  async function submit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      if (mode === "signup") await signup(displayName, email, password);
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
        <span className="colophon">Live Financial Ecosystem</span>
        <div>
          <h1>Stockastic</h1>
          <p>Five hours. Two hundred and fifty companies. One book to trade them in.</p>
        </div>
        <span className="colophon">Trades are final</span>
      </div>

      <form onSubmit={submit} className="login-form">
        <div className="seg">
          <button type="button" aria-pressed={mode === "login"} onClick={() => setMode("login")}>
            Log in
          </button>
          <button type="button" aria-pressed={mode === "signup"} onClick={() => setMode("signup")}>
            Sign up
          </button>
        </div>
        {mode === "signup" && (
          <input placeholder="Display name" value={displayName} onChange={(e) => setDisplayName(e.target.value)} required />
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
