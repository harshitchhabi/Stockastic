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
    <div style={{ display: "flex", height: "100vh", alignItems: "center", justifyContent: "center" }}>
      <form
        onSubmit={submit}
        className="panel"
        style={{ width: 340, padding: 24, gap: 12, display: "flex", flexDirection: "column" }}
      >
        <h2 style={{ margin: 0 }}>Stockastic</h2>

        <div style={{ display: "flex", gap: 4 }}>
          <button
            type="button"
            onClick={() => setMode("login")}
            style={{
              flex: 1,
              background: mode === "login" ? "var(--accent)" : "transparent",
              color: mode === "login" ? "#fff" : "var(--text)",
              border: "1px solid var(--panel-border)",
              borderRadius: 4,
              padding: 6,
            }}
          >
            Log in
          </button>
          <button
            type="button"
            onClick={() => setMode("signup")}
            style={{
              flex: 1,
              background: mode === "signup" ? "var(--accent)" : "transparent",
              color: mode === "signup" ? "#fff" : "var(--text)",
              border: "1px solid var(--panel-border)",
              borderRadius: 4,
              padding: 6,
            }}
          >
            Sign up
          </button>
        </div>

        {mode === "signup" && (
          <input
            placeholder="Display name"
            value={displayName}
            onChange={(e) => setDisplayName(e.target.value)}
            required
          />
        )}
        <input
          placeholder="Email"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
          required
        />
        <input
          placeholder="Password"
          type="password"
          minLength={mode === "signup" ? 8 : undefined}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          required
        />
        {error && <div style={{ color: "var(--red)", fontSize: 12 }}>{error}</div>}
        <button
          type="submit"
          disabled={submitting}
          style={{ background: "var(--accent)", color: "#fff", border: "none", borderRadius: 4, padding: 8 }}
        >
          {submitting ? "…" : mode === "signup" ? "Create account" : "Enter terminal"}
        </button>
      </form>
    </div>
  );
}
