# Stockastic

Live financial-market simulation event platform. Governing spec: the organisers' Live Financial Ecosystem rulebook v1.1 (not final, values change). It is not stored in this repository; every rule value lives in `apps/api/internal/rulebook/rulebook.json`.

## Shape of the system

At event time there is **one process**: the Go binary. It serves the web app, the REST API and the WebSocket feed, and
writes everything durable to an fsync'd log file (`DATA_DIR/stockastic.wal`).

```
 browsers ──HTTP/WS──►  Go binary  ──►  durable log
 (static SPA served by                 (rebuilds all state on start)
  the same binary)
```

The log sits behind a small `store.Log` interface, so a Postgres implementation can replace the file without touching
anything else. Postgres is **not built yet**; until it is, the log file is the system of record.

The frontend is built once and embedded in the Go binary, so there is no Node server, no second origin and no CORS.
Node exists only on a developer machine to build the frontend.

## Layout

| Path | What |
|---|---|
| `apps/api` | Go backend (`stockastic/api`). **Built and tested:** `app` (wiring), `httpapi` (Gin), `wsapi`, `auth`, `store` (durable log), `config`, `market`, `universe`, `dto`, `rulebook`, `scoring`, `engine`, `ledger`, `eventclock`, `news`, `ratelimit`, `disputes`, `webui`. **Not built yet:** `funds` (Stage 2), a Postgres `store.Log` |
| `apps/api/internal/rulebook/rulebook.json` | **Every rulebook value**, embedded in the binary. Values the rulebook marks Recommended / TBF, and gaps we filled with an assumption, are tagged in its `provenance` map |
| `apps/web` | Vite + React + TypeScript single-page app. Builds straight into `apps/api/internal/webui/dist` |
| `docs/` | `deployment.md` (hosting, sizing, measured load results), `admin-api.md` (organiser routes), `stage2-dashboards.md` (Stage 2 plan) |
| `deploy/` | Ready-to-use server files: systemd unit, Caddy config, backup script, kernel settings, env template |
| `apps/api/cmd/loadsim` | Load simulator: hundreds of teams sign up, log in together, trade over live sockets, then stampede one company |

The previous TypeScript backend lives only on the `archive/ts-backend` branch.

## Running it

```bash
# 1. settings (once). Create apps/api/.env.local (git-ignored) with at least:
#      JWT_SECRET=<32+ random characters>
#      ADMIN_EMAIL=<organiser login>
#      ADMIN_PASSWORD=<12+ characters>
# 2. build the web app, then run the server (it serves the web app too)
cd apps/web && npm install && npm run build
cd ../api && go run ./cmd/api          # http://127.0.0.1:8080

# development: run the server as above, and in another terminal
cd apps/web && npm run dev             # http://localhost:3000, proxies /api and /ws to :8080
```

Settings are environment variables (a `.env.local` next to where you run the server is read too):

| Variable | Default | Meaning |
|---|---|---|
| `JWT_SECRET` | required | 32+ characters; signs login tokens |
| `ADMIN_EMAIL`, `ADMIN_PASSWORD` | none | Creates or updates the organiser login at start (password 12+ characters) |
| `ADDR` | `127.0.0.1:8080` | Listen address. Use `0.0.0.0:8080` to serve other machines |
| `DATA_DIR` | `./data` | Where the durable log lives. **Back this up.** |
| `ALLOW_SIGNUP` | `true` | Let teams register themselves |
| `ALLOWED_ORIGINS` | localhost:3000 | Extra browser origins allowed to open the WebSocket |
| `RULEBOOK_PATH` | embedded | An edited copy of `rulebook.json` |
| `UNIVERSE_PATH` | placeholder list | The real company list (see `internal/universe`) |
| `AUTOSTART` | `false` | Start the event clock on boot. Development only |
| `WEB_DIR` | embedded | Serve the built web app from a folder instead |

**Nobody can trade until shares exist.** Teams start with cash only and short selling is not allowed. As an organiser,
open **Shares** in the console and give teams some (or open a team from **Participants** to give it shares or change its cash). Then start the event in the **Control room** (start, then jump to
"Phase 1, live trading").

## Commands

```bash
# backend
cd apps/api && go vet ./... && go test -race ./...

# load test against a running server (use a throwaway DATA_DIR; it creates accounts and orders)
cd apps/api && go run ./cmd/loadsim -url http://127.0.0.1:8080 -admin-email <email> -admin-password <password> -users 300

# frontend
cd apps/web && npm install
npm run dev          # Vite on :3000, proxying /api and /ws to the Go API on :8080
npm run typecheck && npm test
npm run build        # -> apps/api/internal/webui/dist, then `go build` embeds it
```

## Rulebook values

Edit `apps/api/internal/rulebook/rulebook.json`. The loader decodes strictly (an unknown key is an error) and validates
cross-field rules (timeline sums to 300 minutes, windows 0–3 in order, prize weights sum to 1, provenance paths resolve);
the process **refuses to start** on a bad rulebook. To change a value without rebuilding, point `RULEBOOK_PATH` at an edited
copy; a wrong path is a startup error, never a silent fallback to the embedded rules.

Participants only ever see `Rulebook.Public()` (served by `/api/config`). A reflection test forces every new rulebook field
to be consciously public or organiser-only.

## Reliability design

**Durability** — every accepted order is on disk before the team is told it was accepted. Start-up rebuilds accounts,
holdings, cash, the order books, working orders, the event clock, news and disputes from the log. A hard kill mid-session
loses nothing that was acknowledged; a half-written final record is discarded on the next start. A retry of an order sent
before the crash returns the original result instead of trading twice.

**One server only** — the durable log is locked while a server uses it, so a second server started by mistake refuses to run
instead of corrupting it. Orders that finish at the same moment share one disk sync.

**Slow clients** — each WebSocket has a bounded send queue and broadcasts never wait, so one stuck browser cannot slow
anyone else; it is disconnected and reconnects.

**Security** — passwords are bcrypt-hashed (with a cap on concurrent hashing), tokens are HS256 and name only the account
(role, promotion and disqualification are read live), organiser routes are checked on the server, logins are rate limited
per email, the WebSocket rejects foreign browser origins, and every organiser action needs a written reason and is audited.

**Engine** — one goroutine per symbol on a buffered channel; symbols run in parallel, orders within a symbol are serial.
- *Write-before-ack*: a match is planned read-only, committed to the journal, and only then applied. A failed commit changes nothing.
- Idempotent on `(account, clientOrderId)`, including concurrent duplicates and post-restart retries.
- A panic halts only that symbol until `Resume` rebuilds it from durable state.
- Money is integer paise end to end (`internal/money`).

**Frontend connection** (`apps/web/src/lib/socket.ts`) — plain JSON-over-WebSocket with a watchdog for silently dead connections,
full-jitter exponential backoff (no thundering herd after a server restart), immediate retry on `online`/tab-visible,
automatic subscription replay, and a stop on auth rejection. Order submissions are retried with the same idempotency key.
Each panel is wrapped in an error boundary so one crash cannot blank the terminal.

**Static assets** (`internal/webui`) — read, hashed and gzip-compressed once at startup; served from memory. No per-request
file access, content-hash ETags, immutable caching for hashed assets, and a mistyped `/api/...` path gets a JSON 404 rather than HTML.
