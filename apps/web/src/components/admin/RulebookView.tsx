import type { RulebookStatus } from "@/lib/adminTypes";
import { Badge, LoadError, usePoll } from "./shared";

const LABEL = { tbf: "To be finalised", recommended: "Recommended", assumption: "Assumption" } as const;
const TONE = { tbf: "down", recommended: "flag", assumption: "flag" } as const;

/** The rules the platform is running on right now, and which of them are still not final. Read-only. */
export function RulebookView() {
  const { data, error, at } = usePoll<RulebookStatus>("/api/admin/rulebook", 60000);
  const count = (s: keyof typeof LABEL) => data?.provenance.filter((p) => p.status === s).length ?? 0;
  const order = { tbf: 0, assumption: 1, recommended: 2 } as const;

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Rulebook</h1>
        {data && <span className="dim">version {data.version} · from {data.source}</span>}
      </div>

      {data && (
        <>
          <div className="figures">
            <div className="figure">
              <div className="label">To be finalised</div>
              <div className={`v ${count("tbf") ? "down" : ""}`}>{count("tbf")}</div>
            </div>
            <div className="figure">
              <div className="label">Assumptions</div>
              <div className="v">{count("assumption")}</div>
            </div>
            <div className="figure">
              <div className="label">Recommended values</div>
              <div className="v">{count("recommended")}</div>
            </div>
          </div>

          <table className="roomy">
            <thead>
              <tr>
                <th>Setting</th>
                <th>Status</th>
                <th>Rulebook section</th>
                <th>Note</th>
              </tr>
            </thead>
            <tbody>
              {[...data.provenance]
                .sort((a, b) => order[a.status] - order[b.status] || a.path.localeCompare(b.path))
                .map((p) => (
                  <tr key={p.path}>
                    <td className="mono" style={{ fontFamily: "var(--mono)" }}>{p.path}</td>
                    <td style={{ fontFamily: "var(--sans)" }}>
                      <Badge tone={TONE[p.status]}>{LABEL[p.status]}</Badge>
                    </td>
                    <td>{p.section}</td>
                    <td className="dim" style={{ textAlign: "left", fontFamily: "var(--sans)", maxWidth: 420 }}>{p.note ?? ""}</td>
                  </tr>
                ))}
            </tbody>
          </table>

          <h2 className="section">Every value</h2>
          <details>
            <summary className="dim">Show the full rulebook</summary>
            <pre className="raw">{JSON.stringify(data.values, null, 2)}</pre>
          </details>
        </>
      )}
    </div>
  );
}
