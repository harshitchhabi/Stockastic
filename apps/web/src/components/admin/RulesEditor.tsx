import { useEffect, useState } from "react";
import { RulesText } from "../RulesText";
import { ActionButton, LoadError, useDo, usePoll } from "./shared";

type RulesAdmin = { text: string; edited: boolean; default: string };

/** Edit the rules players read on their Rules page. This is only the text shown to them: the game itself follows
 * the rulebook file and does not change. */
export function RulesEditor() {
  const { data, error, at, reload } = usePoll<RulesAdmin>("/api/admin/rules", 60000);
  const run = useDo(reload);
  const [draft, setDraft] = useState<string | null>(null);
  useEffect(() => {
    if (data && draft === null) setDraft(data.text);
  }, [data, draft]);

  if (!data || draft === null) return <div className="page"><LoadError error={error} at={at} />{!error && <div className="empty">loading…</div>}</div>;
  const changed = draft !== data.text;

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>Rules shown to players</h1>
        <span className="dim">{data.edited ? "edited by an organiser" : "the default, written from the rulebook"}</span>
      </div>
      <p className="dim">
        This is what players read on their Rules page. Changing it does not change how the game works. A line starting with "# " is a heading, a line
        starting with "- " is a bullet point, and anything else is a paragraph.
      </p>
      <div className="two-col" style={{ gap: 32, alignItems: "start" }}>
        <div className="stack">
          <textarea value={draft} onChange={(e) => setDraft(e.target.value)} rows={34} style={{ width: "100%", fontFamily: "var(--mono)", fontSize: 13 }} />
          <div className="row-field">
            <ActionButton
              label="Publish to players"
              className="solid"
              disabled={!changed}
              title="Publish these rules to every player?"
              description="Players see the new text straight away."
              run={() => run("/api/admin/rules", { text: draft }, "Rules published").then(() => setDraft(null))}
            />
            <button disabled={!changed} onClick={() => setDraft(data.text)}>
              Discard changes
            </button>
            <ActionButton
              label="Back to the default"
              disabled={!data.edited}
              title="Put back the default rules?"
              description="Players see the text written from the rulebook again. Your edited version is replaced."
              run={() => run("/api/admin/rules", { text: "" }, "Default rules restored").then(() => setDraft(null))}
            />
          </div>
        </div>
        <div>
          <div className="label" style={{ marginBottom: 8 }}>
            Preview
          </div>
          <RulesText text={draft} />
        </div>
      </div>
    </div>
  );
}
