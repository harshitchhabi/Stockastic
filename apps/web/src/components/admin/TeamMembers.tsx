import { ActionButton, useDo, usePoll } from "./shared";

type Member = { id: string; name: string; email: string; trader: boolean; joinedAt: number; locked?: boolean };
type Members = { joinCode: string; teamSize: number; traderName: string; leaderTrades: boolean; members: Member[] };

/** The people in one team: who trades for it, its join code, and substitutions. */
export function TeamMembers({ id, name }: { id: string; name: string }) {
  const base = `/api/admin/accounts/${encodeURIComponent(id)}`;
  const { data, reload } = usePoll<Members>(`${base}/members`, 10000);
  const run = useDo(reload);
  if (!data || data.teamSize < 2) return null;
  const places = data.teamSize - 1 - data.members.length;

  return (
    <>
      <h2 className="section">Members</h2>
      <p className="dim">
        The team leader signs in with the team's own login. Teammates join with the team code and have their own logins. Only one of them places the
        team's trades and moves its money: <strong>{data.traderName}</strong>.
      </p>
      <div className="meta" style={{ marginBottom: 12 }}>
        <span>
          Team code <span className="mono">{data.joinCode}</span>
        </span>
        <span className="dim">{Math.max(places, 0)} place(s) left</span>
        <ActionButton
          label="New code"
          title={`Give ${name} a new team code?`}
          description="The old code stops working at once. Teammates who already joined are not affected."
          run={() => run(`${base}/team-code`, {}, `${name} has a new team code`)}
        />
      </div>
      <table className="roomy">
        <thead>
          <tr>
            <th style={{ textAlign: "left" }}>Name</th>
            <th style={{ textAlign: "left" }}>Email</th>
            <th>Trades</th>
            <th />
          </tr>
        </thead>
        <tbody>
          <tr>
            <td style={{ textAlign: "left" }}>
              Team leader <span className="label">· the team's own login</span>
            </td>
            <td />
            <td>{data.leaderTrades ? "Yes" : ""}</td>
            <td>
              {!data.leaderTrades && (
                <ActionButton
                  label="Leader trades"
                  title={`Let the team leader trade for ${name} again?`}
                  run={() => run(`${base}/trader`, { memberId: "" }, `The leader trades for ${name} again`)}
                />
              )}
            </td>
          </tr>
          {data.members.map((m) => (
            <tr key={m.id}>
              <td style={{ textAlign: "left" }}>{m.name}</td>
              <td style={{ textAlign: "left" }}>{m.email}</td>
              <td>{m.trader ? "Yes" : ""}</td>
              <td>
                <span className="row-field" style={{ justifyContent: "flex-end" }}>
                  {!m.trader && (
                    <ActionButton
                      label="Make trader"
                      title={`Let ${m.name} trade for ${name}?`}
                      description="Only this person will be able to buy, sell and move the team's money. Use it when the leader is away."
                      run={() => run(`${base}/trader`, { memberId: m.id }, `${m.name} now trades for ${name}`)}
                    />
                  )}
                  <ActionButton
                    label="Sign out"
                    title={`Sign ${m.name} out?`}
                    description="They can sign in again straight away."
                    run={() => run(`${base}/members/${m.id}/sign-out`, {}, `${m.name} signed out`)}
                  />
                  <ActionButton
                    label="Remove"
                    danger
                    title={`Remove ${m.name} from ${name}?`}
                    description="Their login stops working and their place opens for a substitute. The team's portfolio is not affected."
                    run={() => run(`${base}/members/${m.id}/remove`, {}, `${m.name} removed from ${name}`)}
                  />
                </span>
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}
