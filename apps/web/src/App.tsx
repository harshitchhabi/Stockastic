import { useSession } from "@/lib/session";
import { LoginForm } from "@/components/LoginForm";
import { DashboardShell } from "@/components/DashboardShell";
import { AdminConsole } from "@/components/AdminConsole";

/** Two routes, no router library: `/admin` is the organiser console, everything else the terminal. */
export default function App() {
  const { account, loading } = useSession();
  if (loading) return null;
  if (!account) return <LoginForm />;

  if (window.location.pathname.startsWith("/admin")) {
    if (!account.isAdmin) {
      return (
        <div style={{ padding: 24, color: "var(--text-dim)" }}>
          Admin access only. Signed in as {account.displayName} ({account.role}).
        </div>
      );
    }
    return <AdminConsole />;
  }
  return <DashboardShell />;
}
