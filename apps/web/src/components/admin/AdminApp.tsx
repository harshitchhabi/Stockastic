import { useEffect, useState } from "react";
import { useSession } from "@/lib/session";
import { ErrorBoundary } from "../ErrorBoundary";
import { AdminProvider, useAdmin } from "./shared";
import { ControlRoom } from "./ControlRoom";
import { Systems } from "./Systems";
import { Participants } from "./Participants";
import { NewsDesk } from "./NewsDesk";
import { Disputes } from "./Disputes";
import { AdminStandings } from "./AdminStandings";
import { AuditLog } from "./AuditLog";
import { RulebookView } from "./RulebookView";

const PAGES = [
  { id: "control", label: "Control room", view: ControlRoom },
  { id: "systems", label: "Systems", view: Systems },
  { id: "participants", label: "Participants", view: Participants },
  { id: "news", label: "News desk", view: NewsDesk },
  { id: "disputes", label: "Disputes", view: Disputes },
  { id: "standings", label: "Standings", view: AdminStandings },
  { id: "audit", label: "Audit log", view: AuditLog },
  { id: "rulebook", label: "Rulebook", view: RulebookView },
] as const;

type PageId = (typeof PAGES)[number]["id"];

const fromHash = (): PageId => {
  const id = window.location.hash.replace(/^#\/?/, "");
  return PAGES.find((p) => p.id === id)?.id ?? "control";
};

export function AdminApp() {
  return (
    <AdminProvider>
      <Frame />
    </AdminProvider>
  );
}

function Frame() {
  const { account, logout } = useSession();
  const { notice } = useAdmin();
  const [page, setPage] = useState<PageId>(fromHash);

  useEffect(() => {
    const on = () => setPage(fromHash());
    window.addEventListener("hashchange", on);
    return () => window.removeEventListener("hashchange", on);
  }, []);

  const View = PAGES.find((p) => p.id === page)!.view;

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%" }}>
      <header className="masthead">
        <div className="brand">Stockastic</div>
        <span className="chip organiser">Organiser console</span>
        <span style={{ marginLeft: "auto" }} className="dim">
          {account?.displayName}
        </span>
        <a href="/">Back to the terminal</a>
        <button onClick={logout}>Sign out</button>
      </header>

      <div className="admin-body">
        <nav className="admin-nav" aria-label="Organiser pages">
          {PAGES.map((p) => (
            <a key={p.id} href={`#/${p.id}`} aria-current={p.id === page ? "page" : undefined}>
              {p.label}
            </a>
          ))}
        </nav>
        <main className="main">
          <ErrorBoundary name={page}>
            <View />
          </ErrorBoundary>
        </main>
      </div>

      <footer className="statusbar" aria-live="polite">
        {notice ? (
          <span className={notice.ok ? "" : "down"}>
            {new Date(notice.at).toLocaleTimeString()} · {notice.text}
          </span>
        ) : (
          <span>Every action here is recorded with your name and reason</span>
        )}
      </footer>
    </div>
  );
}
