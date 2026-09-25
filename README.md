# Stockastic

Live financial-market simulation event platform. Governing spec: the organisers' final Live Financial Ecosystem rulebook. It is not stored in this repository; every rule value lives in `apps/api/internal/rulebook/rulebook.json`.

The market is a **price simulation**, not an order book. Teams buy or sell a number of shares at the company's current simulated price (two trades a minute, trades are final). Prices come from a scenario file that the organisers supply.

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
| `apps/api` | Go backend (`stockastic/api`): `app` (wiring), `httpapi` (Gin), `wsapi`, `auth`, `store` (durable log), `config`, `market` (current prices), `sim` (price simulation), `trading` (buy and sell at the current price), `ledger`, `funds` (Phase 2 funds, units, NAV, checkpoints), `scoring` (ranking, pairing, caps, prizes), `universe`, `dto`, `rulebook`, `eventclock`, `news`, `ratelimit`, `disputes`, `webui` |
| `apps/api/internal/rulebook/rulebook.json` | **Every rulebook value**, embedded in the binary. Values the rulebook marks Recommended / TBF, and gaps we filled with an assumption, are tagged in its `provenance` map |
| `apps/web` | Vite + React + TypeScript single-page app. Builds straight into `apps/api/internal/webui/dist` |
| `docs/` | `deployment.md` (hosting, sizing, measured load results), `admin-api.md` (organiser routes), `data-files.md` (how the final data becomes the game), `security.md` (protection against abuse and how it was tested), `owner-checklist.md` (what only you can do), `stage2-dashboards.md` (Phase 2 screens) |
| `deploy/` | Ready-to-use server files: systemd unit, Caddy config, backup script, kernel settings, env template |
| `tools/import_final.py` | Turns the organisers' final workbook into the files the server loads (see `docs/data-files.md`) |
| `apps/api/scenarios/mock` | A small sample scenario that is safe to commit; the tests use it |
| `apps/api/scenarios/final` | The final companies, exact prices and news. **Git-ignored** because it holds the future prices |
| `apps/api/cmd/abusesim` | Attack simulator: floods, guessing, idle and flooding sockets, slow connections, huge bodies, while honest teams trade |
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
| `UNIVERSE_PATH` | placeholder list | The company list and opening prices (see `docs/data-files.md`) |
| `SCENARIO_PATH` | plain random walk | The price simulation: volatility, price limits, scheduled events, bull and bear runs |
| `DISK_MIN_FREE_MB` | `200` | New trades are refused below this much free disk space |
| `SIGNUP_CODE`, `MAX_ACCOUNTS`, `MAX_SOCKETS`, `MAX_SOCKETS_PER_ACCOUNT`, `TRUSTED_PROXIES` | see `docs/security.md` | Protection against abuse |
| `AUTOSTART` | `false` | Start the event clock on boot. Development only |
| `WEB_DIR` | embedded | Serve the built web app from a folder instead |

Run with the final data (generate it first with `python tools/import_final.py`; use `scenarios/mock` for the sample):

```bash
cd apps/api && UNIVERSE_PATH=scenarios/final/universe.json SCENARIO_PATH=scenarios/final/scenario.json go run ./cmd/api
```

**Nobody can sell until they hold shares.** Teams start with cash only and short selling is not allowed. Buying works from the start. To give teams shares, open **Shares** in the console (or open a team from **Participants**). Then start the event in the **Control room**: start, then jump to "Phase 1 live trading". After the Phase 1 freeze, open **Funds and prizes** and form the funds.

## Commands

```bash
# backend
cd apps/api && go vet ./... && go test -race ./...

# load test against a running server (use a throwaway DATA_DIR; it creates accounts and trades)
cd apps/api && go run ./cmd/loadsim -url http://127.0.0.1:8080 -admin-email <email> -admin-password <password> -users 300

# frontend
cd apps/web && npm install
npm run dev          # Vite on :3000, proxying /api and /ws to the Go API on :8080
npm run typecheck && npm test
npm run build        # -> apps/api/internal/webui/dist, then `go build` embeds it
```

## Rulebook values

Edit `apps/api/internal/rulebook/rulebook.json`. The loader decodes strictly (an unknown key is an error) and validates
cross-field rules (allocation windows numbered in order, prize weights sum to 1, provenance paths resolve);
the process **refuses to start** on a bad rulebook. To change a value without rebuilding, point `RULEBOOK_PATH` at an edited
copy; a wrong path is a startup error, never a silent fallback to the embedded rules.

Participants only ever see `Rulebook.Public()` (served by `/api/config`). A reflection test forces every new rulebook field
to be consciously public or organiser-only.

## Reliability design

**Durability** - every accepted trade, fund allocation and organiser change is on disk before the person is told it happened. Start-up rebuilds accounts, holdings, cash, prices, the simulation's position, funds and units, the event clock, news and disputes from the log. A hard kill mid-session loses nothing that was acknowledged; a half-written final record is discarded on the next start. A retry of a trade sent before the crash returns the original result instead of trading twice.

**The log cannot grow without bound** - at every start the server tidies the log: superseded copies of accounts, clock state, news and old price records are dropped, while trades, grants, cash changes, fund events, snapshots and the audit log are always kept in full. Restarting any number of times leaves the file the same size (tested). New trades are refused when free disk space falls below `DISK_MIN_FREE_MB`, and `/readyz` and the Systems page say so. `deploy/stockastic.service` stops restarting after 20 starts in 5 minutes, and `deploy/journald-stockastic.conf` caps the system log.

**One server only** - the durable log is locked while a server uses it, so a second server started by mistake refuses to run instead of corrupting it. Writes that finish at the same moment share one disk sync.

**Slow clients** - each WebSocket has a bounded send queue and broadcasts never wait, so one stuck browser cannot slow anyone else; it is disconnected and reconnects.

**Security** - passwords are bcrypt-hashed (with a cap on concurrent hashing), tokens are HS256 and carry a per-account session version (signing a team out cancels its tokens), role, promotion and disqualification are read live, organiser routes are checked on the server, logins are rate limited per email, the WebSocket rejects foreign browser origins, and every organiser action asks for confirmation and is audited (who, what, when). Prices come only from the server: a trade names a company and a number of shares, never a price, so a participant cannot alter what they pay. Use HTTPS in production (`deploy/Caddyfile`).

**Trading** - one lock per account. Cash and shares can never go negative, money is integer paise end to end (`internal/money`), and a trade is written to the log before it is applied (write before acknowledge). Trades are idempotent on `(account, clientTradeId)`, including concurrent duplicates and post-restart retries. An optional `expectedPrice` makes the server refuse a trade if the price moved since the person saw it.

**Price simulation** (`internal/sim`) - deterministic from a seed, so a restarted server continues exactly where it was. Prices change once per tick while the market is open: random movement scaled by each company's volatility, market events that move a sector or one company over a few minutes, and bull or bear runs. In Phase 2 the price reaction to news waits for the public release, so fund managers, who see the news 60 seconds earlier, have that window to act.

**Frontend connection** (`apps/web/src/lib/socket.ts`) — plain JSON-over-WebSocket with a watchdog for silently dead connections,
full-jitter exponential backoff (no thundering herd after a server restart), immediate retry on `online`/tab-visible,
automatic subscription replay, and a stop on auth rejection. Trades are retried with the same idempotency key.
Each panel is wrapped in an error boundary so one crash cannot blank the terminal.

**Static assets** (`internal/webui`) — read, hashed and gzip-compressed once at startup; served from memory. No per-request
file access, content-hash ETags, immutable caching for hashed assets, and a mistyped `/api/...` path gets a JSON 404 rather than HTML.
