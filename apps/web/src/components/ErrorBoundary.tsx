import { Component, type ErrorInfo, type ReactNode } from "react";

/**
 * Contains a render error to one panel, so a bug in (say) the chart cannot blank the order ticket
 * mid-event. The panel shows a retry button; everything else keeps running.
 */
export class ErrorBoundary extends Component<{ name: string; children: ReactNode }, { failed: boolean }> {
  state = { failed: false };

  static getDerivedStateFromError() {
    return { failed: true };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(`panel "${this.props.name}" crashed`, error, info.componentStack);
  }

  render() {
    if (!this.state.failed) return this.props.children;
    return (
      <div className="panel" style={{ padding: 12, gap: 8 }}>
        <div className="panel-header" style={{ padding: 0, border: "none" }}>
          {this.props.name}
        </div>
        <div style={{ color: "var(--red)" }}>This panel hit an error. The rest of the terminal is unaffected.</div>
        <button onClick={() => this.setState({ failed: false })} style={{ alignSelf: "flex-start" }}>
          Retry
        </button>
      </div>
    );
  }
}
