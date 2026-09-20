import { useState } from "react";
import type { NewsDesk as NewsDeskData } from "@/lib/adminTypes";
import { fmtMinSec } from "@/lib/format";
import { ActionButton, Badge, LoadError, useDo, useNow, usePoll } from "./shared";

/** Publish news and market-regime events. The server handles the two-tier timing; this only shows it. */
export function NewsDesk() {
  const { data, error, at, reload } = usePoll<NewsDeskData>("/api/admin/news", 3000);
  const run = useDo(reload);
  const now = useNow(1000);
  const [kind, setKind] = useState<"news" | "regime">("news");
  const [headline, setHeadline] = useState("");
  const [body, setBody] = useState("");

  const delay = data?.publicDelaySeconds;
  const ready = headline.trim().length >= 3;

  return (
    <div className="page">
      <LoadError error={error} at={at} />
      <div className="page-head">
        <h1>News desk</h1>
      </div>

      <div className="two-col">
        <div className="stack">
          <div className="seg" role="group" aria-label="What to publish">
            <button aria-pressed={kind === "news"} onClick={() => setKind("news")}>
              Market news
            </button>
            <button aria-pressed={kind === "regime"} onClick={() => setKind("regime")}>
              Market regime
            </button>
          </div>
          <label className="field">
            <span className="label">Headline</span>
            <input value={headline} onChange={(e) => setHeadline(e.target.value)} style={{ fontFamily: "var(--serif)", fontSize: 16 }} />
          </label>
          <label className="field">
            <span className="label">Detail (optional)</span>
            <textarea rows={4} value={body} onChange={(e) => setBody(e.target.value)} />
          </label>

          <div className="ticket-summary">
            {kind === "news" ? (
              <>
                <div>
                  <span className="label">Fund managers</span>
                  <span>immediately</span>
                </div>
                <div>
                  <span className="label">Everyone else</span>
                  <span>{delay !== undefined ? `after ${delay} seconds` : "after the public delay"}</span>
                </div>
              </>
            ) : (
              <div>
                <span className="label">Everyone</span>
                <span>immediately, at the same moment</span>
              </div>
            )}
          </div>

          <ActionButton
            className="solid"
            disabled={!ready}
            label="Publish"
            title={kind === "news" ? "Publish this news" : "Announce this market regime"}
            description={<strong>{headline}</strong>}
            run={(reason) =>
              run("/api/admin/news", { reason, kind, headline: headline.trim(), body: body.trim() || undefined }, "Published").then(() => {
                setHeadline("");
                setBody("");
              })
            }
          />
        </div>

        <div>
          <h2 className="section" style={{ marginTop: 0 }}>Published</h2>
          {(data?.items ?? []).map((n) => {
            const pending = n.publicAt === 0 ? null : Math.max(0, n.publicAt - now);
            return (
              <article key={n.id} className="wire-item">
                <div className="label">
                  {n.kind === "regime" ? "Market regime" : "News"} · <span className="mono">{new Date(n.createdAt).toLocaleTimeString()}</span>
                </div>
                <div className="hl">{n.headline}</div>
                {n.body && <div className="dim">{n.body}</div>}
                <div className="rel">
                  {n.kind === "news" && (
                    <>
                      <Badge tone={n.fundManagerAt ? "up" : "flag"}>{n.fundManagerAt ? "Fund managers have it" : "Fund managers waiting"}</Badge>{" "}
                    </>
                  )}
                  {n.publicAt > 0 && n.publicAt <= now ? (
                    <Badge tone="up">Public</Badge>
                  ) : (
                    <Badge tone="flag">Public in {fmtMinSec(pending ?? 0)}</Badge>
                  )}
                </div>
              </article>
            );
          })}
          {data && data.items.length === 0 && <div className="empty">nothing published yet</div>}
        </div>
      </div>
    </div>
  );
}
