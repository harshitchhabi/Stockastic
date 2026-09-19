"use client";

import { useSession } from "@/lib/session";
import { LoginForm } from "@/components/LoginForm";
import { AdminConsole } from "@/components/AdminConsole";

export default function AdminPage() {
  const { account, loading } = useSession();

  if (loading) return null;
  if (!account) return <LoginForm />;
  if (!account.isAdmin) {
    return (
      <div style={{ padding: 24, color: "var(--text-dim)" }}>
        Admin access only. Signed in as {account.displayName} ({account.role}).
      </div>
    );
  }
  return <AdminConsole />;
}
