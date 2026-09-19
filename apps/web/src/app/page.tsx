"use client";

import { useSession } from "@/lib/session";
import { LoginForm } from "@/components/LoginForm";
import { DashboardShell } from "@/components/DashboardShell";

export default function Home() {
  const { account, loading } = useSession();

  if (loading) return null;
  if (!account) return <LoginForm />;
  return <DashboardShell />;
}
