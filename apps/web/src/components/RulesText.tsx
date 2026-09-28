/**
 * Shows the rules text. The format is kept simple on purpose: a line starting "# " is a heading, a line starting
 * "- " is a bullet point, anything else is a paragraph. It is rendered as plain text, never as HTML.
 */
export function RulesText({ text }: { text: string }) {
  const blocks: React.ReactNode[] = [];
  let bullets: string[] = [];
  const flush = () => {
    if (bullets.length) {
      blocks.push(
        <ul key={`u${blocks.length}`} className="rules-list">
          {bullets.map((b, i) => (
            <li key={i}>{b}</li>
          ))}
        </ul>
      );
      bullets = [];
    }
  };
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line.startsWith("- ")) {
      bullets.push(line.slice(2));
      continue;
    }
    flush();
    if (!line) continue;
    if (line.startsWith("# ")) {
      blocks.push(
        <h2 key={`h${blocks.length}`} className="section">
          {line.slice(2)}
        </h2>
      );
    } else {
      blocks.push(<p key={`p${blocks.length}`}>{line}</p>);
    }
  }
  flush();
  return <div className="rules-text">{blocks}</div>;
}
