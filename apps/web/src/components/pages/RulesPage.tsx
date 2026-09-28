import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { getSocket } from "@/lib/socket";
import { RulesText } from "../RulesText";

/** The rules of the event, as the organisers publish them. */
export function RulesPage() {
  const [text, setText] = useState<string | null>(null);
  const load = useCallback(() => {
    api
      .get<{ text: string }>("/api/rules")
      .then((r) => setText(r.text))
      .catch(() => {});
  }, []);
  useEffect(load, [load]);
  useEffect(() => {
    const socket = getSocket();
    socket.on("rules", load); // the organisers changed them
    return () => socket.off("rules", load);
  }, [load]);

  return (
    <div className="page" style={{ maxWidth: 820 }}>
      <div className="page-head">
        <h1>Rules</h1>
      </div>
      {text == null ? <div className="empty">loading…</div> : <RulesText text={text} />}
    </div>
  );
}
