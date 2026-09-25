import { useMemo, useState } from "react";
import { api } from "@/lib/api";
import type { AdminAccount } from "@/lib/adminTypes";
import { ago } from "@/lib/format";
import { ActionButton, Badge, LoadError, useAdmin, useDo, useNow, usePoll } from "./shared";
import { downloadCsv } from "./download";

type RoleFilter = "all" | "investor" | "fund_manager";
type PresenceFilter = "all" | "online" | "offline" | "below";

function lastSeenText(a: AdminAccount, now: number) {
  if (a.online) return a.sockets > 1 ? `Online, ${a.sockets} tabs` : "Online";
  return a.lastSeen ? `Left ${ago(a.lastSeen, now)}` : "Not seen yet";
}

/** Everyone in the event, who is online right now, and quick actions on each team. */
export function Participants() {
  const { data, error, at, reload } = usePoll<AdminAccount[]>("/api/admin/accounts", 4000);
  const run = useDo(reload);
  const now = useNow(1000);
  const [query, setQuery] = useState("");
  const [role, setRole] = useState<RoleFilter>("all");
  const [presence, setPresence] = useState<PresenceFilter>("all");
  const { notify } = useAdmin();
  const settings = usePoll<{ signupOpen: boolean; signupCode: string; allowlistCount: number }>("/api/admin/settings", 5000);
  const [list, setList] = useState("");
  const [code, setCode] = useState<string | null>(null);

  const counts = useMemo(() => {
    const all = data ?? [];
    const online = all.filter((a) => a.online).length;
    return { online, offline: all.length - online };
  }, [data]);

  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data ?? [])
      .filter((a) => role === "all" || a.role === role)
      .filter((a) => presence === "all" || (presence === "below" ? a.belowMandatory : (presence === "online") === a.online))
      .filter((a) => !q || a.displayName.toLowerCase().includes(q) || a.email.toLowerCase().includes(q))
      .sort((a, b) => Number(b.online) - Number(a.online) || b.portfolioValue - a.portfolioValue);
  }, [data, query, role, presence]);

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Participants</h1>
        {data && (
          <span className="dim">
            <span className="up">{counts.online} online</span>, {counts.offline} offline, {data.length} in all
          </span>
        )}
        <span style={{ marginLeft: "auto" }} className="btn-row">
          {settings.data && (
            <ActionButton
              label={settings.data.signupOpen ? "Close registration" : "Open registration"}
              className={settings.data.signupOpen ? "" : "solid"}
              title={settings.data.signupOpen ? "Close registration" : "Open registration"}
              description={settings.data.signupOpen ? "New people can no longer create an account. Existing teams are not affected." : "New people can create an account again."}
              run={async () => {
                await api.post("/api/admin/settings/signup", { open: !settings.data!.signupOpen });
                notify(settings.data!.signupOpen ? "Registration closed" : "Registration opened");
                await settings.reload();
              }}
            />
          )}
          <button onClick={() => void downloadCsv("/api/admin/export/accounts.csv", "teams.csv").catch((e) => notify(String(e.message ?? e), false))}>Export teams</button>
          <ActionButton
            label="Sign everyone out"
            danger
            disabled={!data || data.length === 0}
            title="Sign every team out"
            description="Every team's open pages go back to the sign-in screen and their old logins stop working. Teams can sign in again straight away. Your own login is not affected."
            run={() => run("/api/admin/sign-out-all", {}, "Every team signed out")}
          />
        </span>
      </div>

      {settings.data && (
        <div className="row-field" style={{ maxWidth: 520, marginBottom: 10 }}>
          <input
            type="text"
            autoComplete="off"
            maxLength={40}
            placeholder="Event code needed to register (empty means none)"
            aria-label="Event code"
            value={code ?? settings.data.signupCode}
            onChange={(e) => setCode(e.target.value)}
          />
          <ActionButton
            label="Save code"
            disabled={code === null || code === settings.data.signupCode}
            title="Change the event code"
            description="New sign-ups need this code from now on. Teams that already have accounts are not affected. Leave it empty to remove the requirement."
            run={async () => {
              await api.post("/api/admin/settings/signup-code", { code: code ?? "" });
              notify("Event code saved");
              setCode(null);
              await settings.reload();
            }}
          />
        </div>
      )}

      {settings.data && (
        <details style={{ marginBottom: 10 }}>
          <summary>
            Approved emails for registration:{" "}
            <strong>{settings.data.allowlistCount > 0 ? `${settings.data.allowlistCount} people may register` : "anyone may register"}</strong>
          </summary>
          <div className="stack" style={{ maxWidth: 560, marginTop: 8 }}>
            <p className="dim" style={{ margin: 0 }}>
              Paste the emails of the people who may register, one per line or separated by commas. Only they can create an account, and each mailbox can be used for one account only. Existing teams are not affected. Save an empty list to let anyone register again.
            </p>
            <textarea rows={6} value={list} onChange={(e) => setList(e.target.value)} placeholder="ann@example.com&#10;bob@example.com" />
            <div>
              <ActionButton
                className="solid"
                label="Save the list"
                title="Save the list of approved emails"
                description="From now on only these emails can register. An empty list lets anyone register."
                run={async () => {
                  const emails = list.split(/[\s,;]+/).filter(Boolean);
                  const r = await api.post<{ count: number }>("/api/admin/settings/allowlist", { emails });
                  notify(r.count > 0 ? `${r.count} approved emails saved` : "Anyone may register again");
                  setList("");
                  await settings.reload();
                }}
              />
            </div>
          </div>
        </details>
      )}

      <div className="btn-row">
        <ActionButton
          label="Clear sign-in locks"
          title="Clear every sign-in lock"
          description="Lifts the short locks that follow repeated wrong passwords, for everyone. Use it if a team is stuck out of its account."
          run={async () => {
            const r = await api.post<{ cleared: number }>("/api/admin/security/clear-login-locks", {});
            notify(`${r.cleared} lock${r.cleared === 1 ? "" : "s"} cleared`);
          }}
        />
        {presence === "below" && (
          <ActionButton
            danger
            label="Warn everyone below the share"
            title="Give a formal warning to every investor below the required share in funds"
            description="Teams that already carry a warning are skipped, so pressing it twice does not stack warnings. What follows a second violation is up to you."
            run={() => run("/api/admin/funds/warn-below-share", {}, "Warnings given")}
          />
        )}
      </div>

      <div className="toolbar">
        <div className="tabs inline" role="tablist">
          {(
            [
              ["all", "Everyone"],
              ["online", "Online"],
              ["offline", "Offline"],
              ["below", `Below the required share in funds${data ? ` (${data.filter((a) => a.belowMandatory).length})` : ""}`],
            ] as [PresenceFilter, string][]
          ).map(([id, label]) => (
            <button key={id} role="tab" aria-selected={presence === id} onClick={() => setPresence(id)}>
              {label}
            </button>
          ))}
        </div>
        <div className="tabs inline" role="tablist">
          {(
            [
              ["all", "All roles"],
              ["investor", "Investors"],
              ["fund_manager", "Fund managers"],
            ] as [RoleFilter, string][]
          ).map(([id, label]) => (
            <button key={id} role="tab" aria-selected={role === id} onClick={() => setRole(id)}>
              {label}
            </button>
          ))}
        </div>
        <input type="search" className="search" placeholder="Search name or email" aria-label="Search participants" value={query} onChange={(e) => setQuery(e.target.value)} />
      </div>

      <table className="roomy">
        <thead>
          <tr>
            <th>Team</th>
            <th>Connection</th>
            <th>Standing</th>
            <th></th>
          </tr>
        </thead>
        <tbody>
          {rows.map((a) => (
            <tr key={a.id}>
              <td>
                <span className={`dot-status ${a.online ? "on" : ""}`} />
                <a href={`#/team/${a.id}`} className="rowlink">
                  <strong>{a.displayName}</strong>
                </a>
                <div className="dim" style={{ fontSize: 11, paddingLeft: 17 }}>
                  {a.email} · {a.role === "fund_manager" ? "Fund manager" : "Investor"}
                </div>
              </td>
              <td className={a.online ? "up" : "dim"} style={{ fontFamily: "var(--sans)", textAlign: "left" }}>
                {lastSeenText(a, now)}
              </td>
              <td style={{ fontFamily: "var(--sans)", textAlign: "left" }}>
                {a.locked && <Badge tone="down">Locked</Badge>}{" "}
                {a.status === "active" && !a.locked && <Badge tone="up">Active</Badge>}
                {a.belowMandatory && <Badge tone="flag">{a.fundShare.toFixed(1)}% in funds</Badge>}{" "}
                {a.status === "warned" && <Badge tone="flag">Warned{a.warnings > 1 ? ` ×${a.warnings}` : ""}</Badge>}
                {a.status === "disqualified" && <Badge tone="down">Disqualified</Badge>}
              </td>
              <td>
                <span className="btn-row" style={{ justifyContent: "flex-end", margin: 0 }}>
                  <ActionButton
                    label="Sign out"
                    title={`Sign ${a.displayName} out`}
                    description="Their open pages go back to the sign-in screen and their old logins stop working. They can sign in again straight away."
                    run={() => run(`/api/admin/accounts/${a.id}/sign-out`, {}, `${a.displayName} signed out`)}
                  />
                  {a.locked ? (
                    <ActionButton
                      className="solid"
                      label="Unlock"
                      title={`Unlock ${a.displayName}`}
                      run={() => run(`/api/admin/accounts/${a.id}/unlock`, {}, `${a.displayName} unlocked`)}
                    />
                  ) : (
                    <ActionButton
                      danger
                      label="Lock"
                      title={`Lock ${a.displayName}`}
                      description="Signs them out and stops them signing in at all until you unlock them."
                      run={() => run(`/api/admin/accounts/${a.id}/lock`, {}, `${a.displayName} locked`)}
                    />
                  )}
                  {a.status === "disqualified" && a.locked ? (
                    <ActionButton
                      className="solid"
                      label="Let back in"
                      title={`Let ${a.displayName} back into the event`}
                      run={() => run(`/api/admin/accounts/${a.id}/readmit`, {}, `${a.displayName} is back in`)}
                    />
                  ) : (
                    <ActionButton
                      danger
                      label="Remove"
                      title={`Remove ${a.displayName} from the event`}
                      description="They are signed out at once, cannot sign in, and cannot trade. Their trades so far stand. You can let them back in later."
                      run={() => run(`/api/admin/accounts/${a.id}/eject`, {}, `${a.displayName} removed from the event`)}
                    />
                  )}
                  <a href={`#/team/${a.id}`}>
                    <button>Open</button>
                  </a>
                </span>
              </td>
            </tr>
          ))}
          {data && rows.length === 0 && (
            <tr>
              <td colSpan={4} className="empty">
                no one matches
              </td>
            </tr>
          )}
        </tbody>
      </table>
    </div>
  );
}
