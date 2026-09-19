/**
 * Load test for Stockastic: ~700-750 concurrent websocket clients (matching
 * the event's expected concurrency, see @stockastic/config CONFIG.event) plus
 * a concurrent HTTP order-submission burst, against the real persisted+authed
 * stack (Postgres write-through, JWT auth) — not a mock.
 *
 * Run:
 *   k6 run loadtest/stockastic.js
 *   BASE_URL=http://localhost:4000 WS_URL=ws://localhost:4000 VUS=750 DURATION=5m k6 run loadtest/stockastic.js
 *
 * The server must be reachable at BASE_URL/WS_URL with DATABASE_URL and
 * JWT_SECRET set — this intentionally exercises the same auth/persistence
 * path production uses, per the rule that a load test against a different
 * path proves nothing real.
 *
 * Socket.IO does not speak raw WebSocket framing — it layers Engine.IO
 * packets (type-prefixed text frames: '0' open, '2'/'3' ping/pong, '4'
 * message) and, within message packets, the Socket.IO protocol itself
 * ('40' connect a namespace, '42["event",data]' emit). This script speaks
 * that protocol directly since k6's ws module only gives raw WebSocket.
 */
import http from "k6/http";
import ws from "k6/ws";
import { check, sleep } from "k6";
import { Counter, Trend } from "k6/metrics";
import { randomString } from "https://jslib.k6.io/k6-utils/1.2.0/index.js";

const BASE_URL = __ENV.BASE_URL || "http://localhost:4000";
const WS_URL = __ENV.WS_URL || BASE_URL.replace(/^http/, "ws");
const VUS = Number(__ENV.VUS || 750);
const DURATION = __ENV.DURATION || "3m";
const SYMBOLS = ["ACME", "GLOBEX", "INITECH", "UMBRELLA"];

const wsMessagesReceived = new Counter("ws_messages_received");
const orderSubmitDuration = new Trend("order_submit_duration", true);
const orderSubmitFailures = new Counter("order_submit_failures");
const wsConnectFailures = new Counter("ws_connect_failures");

export const options = {
  scenarios: {
    // One persistent websocket connection per VU for the whole test — this
    // is the ~700-750 concurrent client requirement.
    websocket_clients: {
      executor: "constant-vus",
      vus: VUS,
      duration: DURATION,
      exec: "websocketClient",
    },
    // A separate, smaller pool hammering order submission concurrently
    // against the same running system — the "order-submission burst".
    order_burst: {
      executor: "ramping-vus",
      startVUs: 0,
      stages: [
        { duration: "30s", target: 50 },
        { duration: "1m", target: 150 },
        { duration: "30s", target: 0 },
      ],
      exec: "orderBurstClient",
    },
  },
  thresholds: {
    ws_connect_failures: ["count==0"],
    order_submit_failures: ["count<50"], // some 429 rate-limit rejections are expected and fine
    // Calibrated from an actual run on a single dev-machine Postgres
    // container: switching auth password hashing from bcryptjs (pure JS,
    // blocks the event loop) to native bcrypt (offloads to libuv's
    // threadpool) took p95 from ~17.6s to ~570ms at 150 concurrent
    // order-submitting VUs. 750ms leaves headroom for a real deployment's
    // network hop; tighten this once you have a baseline against the
    // actual event's Postgres instance.
    order_submit_duration: ["p(95)<750"],
  },
};

function signUpOrLogIn(vuTag) {
  const email = `loadtest-${vuTag}@stockastic.test`;
  const password = "loadtest-password-123";

  let res = http.post(
    `${BASE_URL}/api/auth/signup`,
    JSON.stringify({ displayName: `Load VU ${vuTag}`, email, password }),
    { headers: { "Content-Type": "application/json" } }
  );

  if (res.status === 409) {
    // Already exists from a prior run — log in instead.
    res = http.post(
      `${BASE_URL}/api/auth/login`,
      JSON.stringify({ email, password }),
      { headers: { "Content-Type": "application/json" } }
    );
  }

  check(res, { "auth succeeded": (r) => r.status === 200 });
  return res.json("token");
}

/** Scenario 1: hold a real Socket.IO connection open, subscribed to a symbol, for the test duration. */
export function websocketClient() {
  const token = signUpOrLogIn(`ws-${__VU}`);
  const symbol = SYMBOLS[__VU % SYMBOLS.length];

  const res = ws.connect(
    `${WS_URL}/socket.io/?EIO=4&transport=websocket`,
    {},
    function (socket) {
      let namespaceConnected = false;

      socket.on("open", () => {
        // Wait for the Engine.IO 'open' packet before sending anything.
      });

      socket.on("message", (data) => {
        wsMessagesReceived.add(1);

        if (data === "2") {
          socket.send("3"); // engine.io ping -> pong, keeps the connection alive
          return;
        }
        if (data.startsWith("0")) {
          // Engine.IO session opened — now connect the Socket.IO namespace,
          // passing the JWT the same way socket.io-client's `auth` option does.
          socket.send(`40${JSON.stringify({ token })}`);
          return;
        }
        if (data.startsWith("40") && !namespaceConnected) {
          namespaceConnected = true;
          socket.send(`42${JSON.stringify(["subscribe:symbol", symbol])}`);
          return;
        }
        // '42[...]' frames from here on are real app events: bookUpdate,
        // trade, fill, news, controlState — just counted above.
      });

      socket.on("error", () => wsConnectFailures.add(1));

      // Stay connected for the scenario duration; the executor tears the VU
      // iteration down (and the socket with it) when the scenario ends.
      socket.setTimeout(() => socket.close(), 170_000);
    }
  );

  check(res, { "ws handshake succeeded": (r) => r && r.status === 101 });
  if (!res || res.status !== 101) wsConnectFailures.add(1);
}

/** Scenario 2: authenticated order submissions at volume, idempotency key included, against the live matching engine. */
export function orderBurstClient() {
  const token = signUpOrLogIn(`burst-${__VU}`);
  const symbol = SYMBOLS[__VU % SYMBOLS.length];
  const side = Math.random() < 0.5 ? "buy" : "sell";
  const price = (100 + (Math.random() * 10 - 5)).toFixed(2);
  const qty = 1 + Math.floor(Math.random() * 5);

  const res = http.post(
    `${BASE_URL}/api/orders`,
    JSON.stringify({
      clientOrderId: `${__VU}-${__ITER}-${randomString(6)}`,
      symbol,
      side,
      price: Number(price),
      qty,
    }),
    { headers: { "Content-Type": "application/json", Authorization: `Bearer ${token}` } }
  );

  orderSubmitDuration.add(res.timings.duration);
  const ok = check(res, {
    "order accepted or rate-limited": (r) => r.status === 200 || r.status === 429,
  });
  if (!ok) orderSubmitFailures.add(1);

  sleep(0.2 + Math.random() * 0.3);
}
