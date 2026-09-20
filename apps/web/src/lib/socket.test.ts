import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { Wire, type WireOptions } from "./socket";

class FakeWS {
  static instances: FakeWS[] = [];
  readyState = 0;
  sent: { t: string; d?: unknown }[] = [];
  closedWith: number | undefined;
  onopen: ((e: Event) => void) | null = null;
  onmessage: ((e: MessageEvent) => void) | null = null;
  onclose: ((e: CloseEvent) => void) | null = null;
  onerror: ((e: Event) => void) | null = null;
  constructor(public url: string) {
    FakeWS.instances.push(this);
  }
  send(s: string) {
    this.sent.push(JSON.parse(s));
  }
  close(code?: number) {
    this.closedWith = code;
    this.readyState = 3;
  }
  // ---- test controls ----
  open() {
    this.readyState = 1;
    this.onopen?.({} as Event);
  }
  recv(frame: unknown) {
    this.onmessage?.({ data: typeof frame === "string" ? frame : JSON.stringify(frame) } as MessageEvent);
  }
  serverClose(code = 1006) {
    this.readyState = 3;
    this.onclose?.({ code } as CloseEvent);
  }
}

const last = () => FakeWS.instances[FakeWS.instances.length - 1];
const count = () => FakeWS.instances.length;

let wakeCb: (() => void) | null = null;

function make(over: Partial<WireOptions> = {}) {
  wakeCb = null;
  return new Wire({
    url: () => "ws://test/ws",
    token: () => "tok",
    WebSocketImpl: FakeWS as unknown as typeof WebSocket,
    random: () => 0.999,
    hooks: { onWake: (cb) => ((wakeCb = cb), () => (wakeCb = null)) },
    ...over,
  });
}

/** Bring a wire fully up: open the socket and have the server accept auth. */
function ready(w: Wire) {
  w.start();
  last().open();
  last().recv({ t: "ready" });
}

beforeEach(() => {
  FakeWS.instances = [];
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe("handshake", () => {
  it("sends auth first and is not connected until the server says ready", () => {
    const w = make();
    const onConnect = vi.fn();
    w.on("connect", onConnect);
    w.start();
    expect(w.status).toBe("connecting");
    last().open();
    expect(last().sent[0]).toEqual({ t: "auth", d: { token: "tok" } });
    expect(w.connected).toBe(false); // TCP open is not enough: the server has not accepted the token yet
    expect(onConnect).not.toHaveBeenCalled();
    last().recv({ t: "ready" });
    expect(w.status).toBe("open");
    expect(onConnect).toHaveBeenCalledTimes(1);
  });

  it("does not even try to connect without a token", () => {
    const w = make({ token: () => null });
    w.start();
    expect(count()).toBe(0);
    expect(w.status).toBe("unauthenticated");
  });

  it("notifies status listeners on every change", () => {
    const w = make();
    const seen: string[] = [];
    w.subscribeStatus(() => seen.push(w.status));
    ready(w);
    last().serverClose();
    expect(seen).toEqual(["connecting", "open", "reconnecting"]);
  });
});

describe("subscriptions survive reconnects", () => {
  it("replays remembered symbols on the next ready and forgets unsubscribed ones", () => {
    const w = make();
    ready(w);
    w.emit("subscribe:symbol", "ACME");
    w.emit("subscribe:symbol", "GLOBEX");
    w.emit("unsubscribe:symbol", "GLOBEX");
    last().serverClose();
    vi.advanceTimersByTime(600);
    last().open();
    last().recv({ t: "ready" });
    const subs = last().sent.filter((f) => f.t === "subscribe:symbol").map((f) => f.d);
    expect(subs).toEqual(["ACME"]);
    expect(last().sent[0].t).toBe("auth"); // auth always precedes anything else
  });

  it("remembers a subscribe issued while offline and sends it once ready", () => {
    const w = make();
    w.emit("subscribe:symbol", "ACME"); // before start
    ready(w);
    expect(last().sent.some((f) => f.t === "subscribe:symbol" && f.d === "ACME")).toBe(true);
  });

  it("drops other frames while not open rather than queueing them for later", () => {
    const w = make();
    w.emit("place-order", { x: 1 });
    ready(w);
    expect(last().sent.some((f) => f.t === "place-order")).toBe(false);
  });
});

describe("reconnect backoff", () => {
  it("grows exponentially, is capped, and resets after a successful ready", () => {
    const w = make(); // random() = 0.999 -> delay ~ ceiling
    w.start();
    // Failures that never reach ready: ceilings 500,1000,2000,4000,8000,10000,10000
    const want = [499, 999, 1998, 3996, 7992, 9990, 9990];
    for (const d of want) {
      const before = count();
      last().serverClose();
      vi.advanceTimersByTime(d - 1);
      expect(count()).toBe(before);
      vi.advanceTimersByTime(1);
      expect(count()).toBe(before + 1);
    }
    // A success resets the ladder.
    last().open();
    last().recv({ t: "ready" });
    const before = count();
    last().serverClose();
    vi.advanceTimersByTime(498);
    expect(count()).toBe(before);
    vi.advanceTimersByTime(1);
    expect(count()).toBe(before + 1);
  });

  it("uses full jitter so clients do not retry in lockstep, but never spins", () => {
    const delayFor = (rand: number) => {
      FakeWS.instances = [];
      const w = make({ random: () => rand });
      w.start();
      last().serverClose();
      for (let ms = 1; ms <= 600; ms++) {
        vi.advanceTimersByTime(1);
        if (count() === 2) return ms;
      }
      return Infinity;
    };
    expect(delayFor(0)).toBe(250); // floor: never a busy loop
    expect(delayFor(0.6)).toBe(300);
    expect(delayFor(0.999)).toBe(499);
  });

  it("stops the retry loop at once when the app stops the wire", () => {
    const w = make();
    w.start();
    last().serverClose();
    w.stop();
    vi.advanceTimersByTime(60_000);
    expect(count()).toBe(1);
  });
});

describe("dead connections", () => {
  it("watchdog: an open connection that goes silent is dropped and re-established", () => {
    const w = make();
    const onDisconnect = vi.fn();
    w.on("disconnect", onDisconnect);
    ready(w);
    const silent = last();
    vi.advanceTimersByTime(39_000);
    expect(count()).toBe(1); // still within the watchdog window
    vi.advanceTimersByTime(3_000); // > 40s of silence: killed, then a retry opens a new socket
    expect(onDisconnect).toHaveBeenCalledTimes(1);
    expect(silent.closedWith).toBe(4000);
    expect(count()).toBe(2);
  });

  it("heartbeats while open, and any frame from the server keeps the connection alive", () => {
    const w = make();
    ready(w);
    for (let i = 0; i < 12; i++) {
      vi.advanceTimersByTime(10_000);
      last().recv({ t: "pong" }); // the server is answering: 120s pass without a reconnect
    }
    expect(count()).toBe(1);
    expect(w.connected).toBe(true);
    expect(last().sent.filter((f) => f.t === "ping").length).toBeGreaterThanOrEqual(7);
  });

  it("abandons a connect attempt that never opens", () => {
    const w = make();
    w.start();
    vi.advanceTimersByTime(11_000);
    expect(last().closedWith).toBe(4000);
    vi.advanceTimersByTime(1_000);
    expect(count()).toBe(2);
    expect(w.status).toBe("reconnecting");
  });

  it("abandons a socket that opens but is never told ready", () => {
    const w = make();
    w.start();
    last().open(); // auth sent, server never answers
    vi.advanceTimersByTime(11_000);
    expect(last().closedWith).toBe(4000);
  });
});

describe("auth rejection", () => {
  it("close code 4401 stops retrying and reports unauthenticated", () => {
    const w = make();
    const onUnauth = vi.fn();
    w.on("unauthenticated", onUnauth);
    ready(w);
    last().serverClose(4401);
    vi.advanceTimersByTime(120_000);
    expect(count()).toBe(1);
    expect(w.status).toBe("unauthenticated");
    expect(onUnauth).toHaveBeenCalledTimes(1);
  });

  it("an unauthenticated error frame does the same", () => {
    const w = make();
    const onUnauth = vi.fn();
    w.on("unauthenticated", onUnauth);
    w.start();
    last().open();
    last().recv({ t: "error", d: { code: "unauthenticated" } });
    vi.advanceTimersByTime(120_000);
    expect(count()).toBe(1);
    expect(onUnauth).toHaveBeenCalledTimes(1);
    expect(last().closedWith).toBe(1000);
  });

  it("other error frames are just delivered to listeners", () => {
    const w = make();
    const onErr = vi.fn();
    w.on("error", onErr);
    ready(w);
    last().recv({ t: "error", d: { code: "rate_limited" } });
    expect(onErr).toHaveBeenCalledWith({ code: "rate_limited" });
    expect(w.connected).toBe(true);
  });
});

describe("wake events", () => {
  it("online/visible reconnects immediately instead of waiting out a long backoff", () => {
    const w = make();
    w.start();
    for (let i = 0; i < 5; i++) {
      last().serverClose();
      vi.advanceTimersByTime(20_000);
    }
    last().serverClose(); // now backing off for several seconds
    const before = count();
    wakeCb?.();
    expect(count()).toBe(before + 1); // no waiting
  });

  it("is a no-op while the connection is healthy", () => {
    const w = make();
    ready(w);
    wakeCb?.();
    expect(count()).toBe(1);
    expect(w.connected).toBe(true);
  });
});

describe("delivery", () => {
  it("routes server frames to listeners of that type", () => {
    const w = make();
    ready(w);
    const onTrade = vi.fn();
    const onBook = vi.fn();
    w.on("trade", onTrade);
    w.on("bookUpdate", onBook);
    last().recv({ t: "trade", d: { symbol: "ACME", price: 100 } });
    expect(onTrade).toHaveBeenCalledWith({ symbol: "ACME", price: 100 });
    expect(onBook).not.toHaveBeenCalled();
    w.off("trade", onTrade);
    last().recv({ t: "trade", d: {} });
    expect(onTrade).toHaveBeenCalledTimes(1);
  });

  it("a throwing listener cannot stop the others or the connection", () => {
    const w = make();
    ready(w);
    vi.spyOn(console, "error").mockImplementation(() => {});
    const good = vi.fn();
    w.on("trade", () => {
      throw new Error("bad panel");
    });
    w.on("trade", good);
    last().recv({ t: "trade", d: 1 });
    expect(good).toHaveBeenCalledWith(1);
    expect(w.connected).toBe(true);
  });

  it("ignores malformed frames", () => {
    const w = make();
    ready(w);
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const onAny = vi.fn();
    w.on("trade", onAny);
    expect(() => last().recv("{not json")).not.toThrow();
    expect(() => last().recv({ noType: true })).not.toThrow();
    expect(() => last().recv({ t: 42 })).not.toThrow();
    expect(onAny).not.toHaveBeenCalled();
    expect(w.connected).toBe(true);
  });
});

describe("lifecycle", () => {
  it("stop() closes for good and leaves no timers behind", () => {
    const w = make();
    const onDisconnect = vi.fn();
    w.on("disconnect", onDisconnect);
    ready(w);
    w.stop();
    expect(w.status).toBe("closed");
    expect(onDisconnect).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
    expect(wakeCb).toBeNull(); // the wake hooks were released too
    vi.advanceTimersByTime(300_000);
    expect(count()).toBe(1);
  });

  it("restart() swaps in a fresh handshake and ignores anything the old socket says afterwards", () => {
    const w = make();
    ready(w);
    const old = last();
    const onTrade = vi.fn();
    w.on("trade", onTrade);
    w.restart();
    expect(count()).toBe(2);
    expect(old.closedWith).toBe(1000);
    old.recv({ t: "trade", d: 1 }); // stale socket
    old.serverClose(1006); // must not trigger a second reconnect
    vi.advanceTimersByTime(5_000);
    expect(onTrade).not.toHaveBeenCalled();
    expect(count()).toBe(2);
  });

  it("start() twice does not open two connections", () => {
    const w = make();
    w.start();
    w.start();
    expect(count()).toBe(1);
  });
});
