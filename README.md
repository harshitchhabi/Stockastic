# Stockastic

Live financial-market simulation event platform. Governing spec: the organisers' Live Financial Ecosystem rulebook v1.1 (not final, values change). It is not stored in this repository; every rule value lives in `apps/api/internal/rulebook/rulebook.json`.

## Shape of the system

At event time there are exactly **two processes**: the Go binary and Postgres.

```
 browsers ──HTTP/WS──►  Go binary  ──►  Postgres
 (static SPA served by                 (system of record)
  the same binary)
```

The frontend is built once and embedded in the Go binary, so there is no Node server, no second origin and no CORS.
Node exists only on a developer machine to build the frontend.

## Layout

| Path | What |
|---|---|
| `apps/api` | Go backend (`stockastic/api`). **Built and tested:** `rulebook`, `scoring`, `engine`, `ledger`, `eventclock`, `news`, `ratelimit`, `disputes`, `webui`. **Still stubs:** `store` (pgx), `auth`, `httpapi` (Gin), `wsapi`, `funds`, `config` |
| `apps/api/internal/rulebook/rulebook.json` | **Every rulebook value**, embedded in the binary. Values the rulebook marks Recommended / TBF, and gaps we filled with an assumption, are tagged in its `provenance` map |
| `apps/web` | Vite + React + TypeScript single-page app. Builds straight into `apps/api/internal/webui/dist` |
| `loadtest/` | k6 load tests (to be written against the Go API) |

The previous TypeScript backend lives only on the `archive/ts-backend` branch.

## Commands

```bash
# backend
cd apps/api && go vet ./... && go test -race ./...

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
