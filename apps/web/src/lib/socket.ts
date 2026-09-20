/**
 * Plain JSON-over-WebSocket client. Frames are text JSON: {"t": "<type>", "d": <payload>}.
 *
 * Contract with the Go backend (wsapi implements the server side):
 *   client -> server   {"t":"auth","d":{"token":"<jwt>"}}   first frame after open
 *                      {"t":"subscribe:symbol","d":"ACME"}   / "unsubscribe:symbol"
 *                      {"t":"ping"}                          every heartbeatMs
 *   server -> client   {"t":"ready"}                         auth accepted; the socket is now live
 *                      {"t":"pong"}
 *                      {"t":"error","d":{"code":"unauthenticated"}}   then close code 4401
 *                      {"t":"bookUpdate" | "trade" | "fill" | "orderAccepted" | "orderCancelled"
 *                        | "news" | "controlState" | ..., "d": ...}
 *
 * Reliability, because laptops drop wifi during a 5-hour event and ~750 clients share one server:
 *  - A half-open TCP connection never errors, so a watchdog treats "no frame for watchdogMs" as dead
 *    and reconnects; the heartbeat keeps a healthy idle connection producing frames.
 *  - Reconnect uses exponential backoff with FULL JITTER, so a server restart does not make every
 *    client retry in lockstep (thundering herd). Backoff resets after a successful ready.
 *  - Browsers throttle timers in background tabs, so `online` and tab-visible trigger an immediate
 *    retry instead of waiting out a long backoff.
 *  - Symbol subscriptions are remembered and replayed after every reconnect (server-side rooms are
 *    lost with the connection); components only need to refetch REST snapshots on "connect".
 *  - An auth rejection (4401) stops retrying and reports "unauthenticated" so the app can log out
 *    instead of hammering the server with a dead token.
 */
import { getToken } from "./api";

export type WireStatus = "connecting" | "open" | "reconnecting" | "unauthenticated" | "closed";
type Handler = (data: any) => void;

export interface WireOptions {
  url: () => string;
  token: () => string | null;
  WebSocketImpl?: typeof WebSocket;
  random?: () => number;
  now?: () => number;
  heartbeatMs?: number;
  watchdogMs?: number;
  connectTimeoutMs?: number;
  baseDelayMs?: number;
  maxDelayMs?: number;
  /** Subscribe to browser events that should trigger an immediate reconnect. Returns an unsubscribe. */
  hooks?: { onWake?: (cb: () => void) => () => void };
}

const UNAUTHENTICATED_CLOSE = 4401;
const MIN_RETRY_MS = 250;

export class Wire {
  private readonly o: Required<Omit<WireOptions, "hooks" | "WebSocketImpl">> & Pick<WireOptions, "hooks" | "WebSocketImpl">;
  private ws: WebSocket | null = null;
  private handlers = new Map<string, Set<Handler>>();
  private symbols = new Set<string>();
  private statusListeners = new Set<() => void>();
  private _status: WireStatus = "closed";
  private wanted = false;
  private attempt = 0;
  private retryTimer: ReturnType<typeof setTimeout> | null = null;
  private tickTimer: ReturnType<typeof setInterval> | null = null;
  private lastRx = 0;
  private lastPing = 0;
  private openedAt = 0;
  private unwake: (() => void) | null = null;
  private wasReady = false;

  constructor(opts: WireOptions) {
    this.o = {
      url: opts.url,
      token: opts.token,
      random: opts.random ?? Math.random,
      now: opts.now ?? Date.now,
      heartbeatMs: opts.heartbeatMs ?? 15_000,
      watchdogMs: opts.watchdogMs ?? 40_000,
      connectTimeoutMs: opts.connectTimeoutMs ?? 10_000,
      baseDelayMs: opts.baseDelayMs ?? 500,
      maxDelayMs: opts.maxDelayMs ?? 10_000,
      hooks: opts.hooks,
      WebSocketImpl: opts.WebSocketImpl,
    };
  }

  get status(): WireStatus {
    return this._status;
  }
  get connected(): boolean {
    return this._status === "open";
  }

  /** Subscribe to status changes (for useSyncExternalStore). */
  subscribeStatus = (cb: () => void): (() => void) => {
    this.statusListeners.add(cb);
    return () => this.statusListeners.delete(cb);
  };

  on(type: string, fn: Handler): void {
    let set = this.handlers.get(type);
    if (!set) this.handlers.set(type, (set = new Set()));
    set.add(fn);
  }

  off(type: string, fn: Handler): void {
    this.handlers.get(type)?.delete(fn);
  }

  /** Send a frame. Symbol subscriptions are also remembered so they survive reconnects. */
  emit(type: string, data?: unknown): void {
    if (type === "subscribe:symbol" && typeof data === "string") this.symbols.add(data);
    if (type === "unsubscribe:symbol" && typeof data === "string") this.symbols.delete(data);
    if (this._status === "open") this.send(type, data);
    // Not open: nothing to send now. Subscriptions are replayed on the next ready; other frames are
    // deliberately dropped rather than queued, so a stale command can never fire after a reconnect.
  }

  /** Start (or restart) the connection. Idempotent. */
  start(): void {
    if (this.wanted && this.ws) return;
    this.wanted = true;
    this.attempt = 0;
    this.unwake ??= this.o.hooks?.onWake?.(() => this.wake()) ?? null;
    this.startTick();
    this.open();
  }

  /** Stop for good (logout). No further reconnects. */
  stop(): void {
    this.wanted = false;
    this.clearRetry();
    this.stopTick();
    this.unwake?.();
    this.unwake = null;
    this.detach(1000);
    this.setStatus("closed");
  }

  /** Drop the current connection and reconnect now, e.g. right after login with a new token. */
  restart(): void {
    this.detach(1000);
    this.clearRetry();
    this.wanted = true;
    this.attempt = 0;
    this.startTick();
    this.open();
  }

  // ---- internals ------------------------------------------------------------------------------

  private setStatus(s: WireStatus): void {
    if (this._status === s) return;
    this._status = s;
    for (const l of [...this.statusListeners]) l();
  }

  private dispatch(type: string, data?: unknown): void {
    const set = this.handlers.get(type);
    if (!set) return;
    for (const fn of [...set]) {
      try {
        fn(data);
      } catch (err) {
        // One faulty panel must never break delivery to the others.
        console.error(`wire: handler for "${type}" threw`, err);
      }
    }
  }

  private send(type: string, data?: unknown): void {
    const ws = this.ws;
    if (!ws || ws.readyState !== 1 /* OPEN */) return;
    try {
      ws.send(JSON.stringify(data === undefined ? { t: type } : { t: type, d: data }));
    } catch {
      // The socket is broken; the close/watchdog path will reconnect.
    }
  }

  private open(): void {
    if (!this.wanted || this.ws) return;
    if (!this.o.token()) {
      this.setStatus("unauthenticated");
      return;
    }
    const Impl = this.o.WebSocketImpl ?? WebSocket;
    this.setStatus(this.attempt === 0 ? "connecting" : "reconnecting");
    const ws = new Impl(this.o.url());
    this.ws = ws;
    this.wasReady = false;
    const t = this.o.now();
    this.openedAt = t;
    this.lastRx = t;
    this.lastPing = t;

    ws.onopen = () => {
      if (this.ws !== ws) return;
      this.send("auth", { token: this.o.token() });
    };
    ws.onmessage = (ev: MessageEvent) => {
      if (this.ws !== ws) return;
      this.lastRx = this.o.now();
      let frame: { t?: unknown; d?: unknown };
      try {
        frame = JSON.parse(String(ev.data));
      } catch {
        console.warn("wire: ignoring malformed frame");
        return;
      }
      if (typeof frame.t !== "string") return;
      this.onFrame(frame.t, frame.d);
    };
    ws.onclose = (ev: CloseEvent) => {
      if (this.ws !== ws) return;
      this.ws = null;
      const wasOpen = this.wasReady;
      this.wasReady = false;
      if (wasOpen) this.dispatch("disconnect");
      if (ev.code === UNAUTHENTICATED_CLOSE) return this.rejectAuth();
      this.scheduleRetry();
    };
    ws.onerror = () => {
      // Always followed by close; handled there.
    };
  }

  private onFrame(type: string, data: unknown): void {
    switch (type) {
      case "pong":
        return;
      case "ready":
        this.attempt = 0;
        this.wasReady = true;
        this.setStatus("open");
        for (const s of this.symbols) this.send("subscribe:symbol", s);
        this.dispatch("connect");
        return;
      case "error":
        if ((data as { code?: string } | undefined)?.code === "unauthenticated") this.rejectAuth();
        else this.dispatch("error", data);
        return;
      default:
        this.dispatch(type, data);
    }
  }

  private rejectAuth(): void {
    this.wanted = false;
    this.clearRetry();
    this.stopTick();
    this.detach(1000);
    this.setStatus("unauthenticated");
    this.dispatch("unauthenticated");
  }

  /** Detach and close the current socket without triggering the reconnect path. */
  private detach(code: number): void {
    const ws = this.ws;
    if (!ws) return;
    this.ws = null;
    ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
    const wasOpen = this.wasReady;
    this.wasReady = false;
    try {
      ws.close(code);
    } catch {
      /* already closed */
    }
    if (wasOpen) this.dispatch("disconnect");
  }

  private scheduleRetry(): void {
    if (!this.wanted) return;
    this.setStatus("reconnecting");
    const ceiling = Math.min(this.o.maxDelayMs, this.o.baseDelayMs * 2 ** this.attempt);
    const delay = Math.max(MIN_RETRY_MS, Math.floor(this.o.random() * ceiling));
    this.attempt++;
    this.clearRetry();
    this.retryTimer = setTimeout(() => {
      this.retryTimer = null;
      this.open();
    }, delay);
  }

  private clearRetry(): void {
    if (this.retryTimer) clearTimeout(this.retryTimer);
    this.retryTimer = null;
  }

  private wake(): void {
    if (!this.wanted) return;
    if (this._status === "open") return;
    this.clearRetry();
    this.attempt = 0;
    this.detach(1000);
    this.open();
  }

  private startTick(): void {
    if (this.tickTimer) return;
    this.tickTimer = setInterval(() => this.tick(), 1000);
  }

  private stopTick(): void {
    if (this.tickTimer) clearInterval(this.tickTimer);
    this.tickTimer = null;
  }

  private tick(): void {
    const ws = this.ws;
    if (!ws) return;
    const now = this.o.now();
    if (this._status === "open") {
      if (now - this.lastPing >= this.o.heartbeatMs) {
        this.lastPing = now;
        this.send("ping");
      }
      if (now - this.lastRx > this.o.watchdogMs) this.kill();
    } else if (now - this.openedAt > this.o.connectTimeoutMs) {
      this.kill(); // connecting (or awaiting ready) for too long
    }
  }

  /** Force-close a connection we no longer trust and go through the normal reconnect path. */
  private kill(): void {
    const ws = this.ws;
    if (!ws) return;
    this.ws = null;
    ws.onopen = ws.onmessage = ws.onclose = ws.onerror = null;
    const wasOpen = this.wasReady;
    this.wasReady = false;
    try {
      ws.close(4000);
    } catch {
      /* ignore */
    }
    if (wasOpen) this.dispatch("disconnect");
    this.scheduleRetry();
  }
}

function defaultUrl(): string {
  const override = import.meta.env.VITE_WS_URL as string | undefined;
  if (override) return override;
  const scheme = location.protocol === "https:" ? "wss" : "ws";
  return `${scheme}://${location.host}/ws`;
}

let wire: Wire | null = null;

/** The single shared connection for the tab. */
export function getSocket(): Wire {
  if (!wire) {
    wire = new Wire({
      url: defaultUrl,
      token: getToken,
      hooks: {
        onWake: (cb) => {
          const onVisible = () => document.visibilityState === "visible" && cb();
          window.addEventListener("online", cb);
          document.addEventListener("visibilitychange", onVisible);
          return () => {
            window.removeEventListener("online", cb);
            document.removeEventListener("visibilitychange", onVisible);
          };
        },
      },
    });
    wire.start();
  }
  return wire;
}

/** Reconnect with the current token, e.g. right after login/signup. */
export function reconnectSocket(): void {
  getSocket().restart();
}

/** Close for good (logout). */
export function closeSocket(): void {
  wire?.stop();
  wire = null;
}
