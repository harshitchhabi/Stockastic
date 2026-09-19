# Stockastic

Desktop live trading terminal. Monorepo (npm workspaces):

- `packages/config` — the ONE file every fee/threshold/window/team-count value lives in (`src/index.ts`). Everything else reads through it; nothing hardcodes a rulebook number.
- `packages/matching-engine` — the core limit order book + exchange. No dependency on config, no UI dependency — pure price-time-priority matching, unit-tested in isolation (`npm test`). Public surface is deliberately narrow: `submitOrder` / `cancelOrder` / `getDepth` / `getOrder` / `freeze` / `unfreeze` / `restoreOrder` (hydration) / `seedIdempotency` (hydration), plus event subscriptions (`fill`, `orderAccepted`, `orderCancelled`, `bookUpdate`). Fees, exposure caps, and window timings never enter this package.
- `packages/settlement` — pure, unit-tested settlement math: final portfolio value (cash + holdings at freeze price + fund units at final NAV) and high-water-mark performance fee checkpoints. The formula shape is fixed even though the fee % itself is still `null`.
- `apps/server` — Fastify REST + Socket.io, JWT auth, Postgres write-through persistence, an admin control surface, and Phase 2 fund endpoints.
- `apps/web` — Next.js (App Router) dashboard shell. One component tree; `account.role` (`investor` | `fund_manager`) gates which panels render — never a separate app. A separate `/admin` route hosts the organizer control surface.

## Why Node for the matching engine, not Go

Per-symbol order processing must be strictly sequential. Node's single-threaded event loop gives that for free via a per-symbol promise queue (see `Exchange.enqueue` in `packages/matching-engine/src/engine.ts`) — no locks, no races. At ~700-750 concurrent WS clients over a 5-10 hour event, this is nowhere near where Go's concurrency model would pay for itself, and staying single-language (TS across engine/backend/frontend) matters more given the rulebook is still in flux.

## Running locally

```bash
npm install

# Postgres (persistence is no longer optional — see below)
docker run -d --name stockastic-pg -e POSTGRES_PASSWORD=stockastic -e POSTGRES_DB=stockastic -p 5432:5432 postgres:16-alpine

export DATABASE_URL="postgres://postgres:stockastic@localhost:5432/stockastic"
export JWT_SECRET="some-long-random-string"        # required for real deployments
export ADMIN_EMAIL="admin@example.com"             # provisions the one admin account at boot
export ADMIN_PASSWORD="a-real-password"
export POSTGRES_POOL_SIZE=50                        # see "Load testing" below for why

npm test                # matching-engine + settlement unit tests
npm run dev:server      # Fastify + Socket.io on :4000 — applies schema.sql, then hydrates from Postgres
npm run dev:web         # Next.js on :3000 (separate terminal)
```

Open http://localhost:3000, sign up (real password auth — bcrypt + JWT, 24h token, sent as `Authorization: Bearer`), and trade. Open http://localhost:4000... er, http://localhost:3000/admin and log in with `ADMIN_EMAIL`/`ADMIN_PASSWORD` for the organizer control surface.

## Persistence

Every order, fill, and account/holdings mutation is written through to Postgres in the same request that accepted it — the client isn't told "success" until the write-through resolves (or fails loudly; see below). At startup, `db/hydrate.ts` rebuilds the entire in-memory matching-engine state from Postgres before the server accepts any request: every symbol's resting orders are replayed in their original price-time (`engine_seq`) order, account cash/holdings are restored as-is, and the idempotency cache is reseeded so a client retrying a pre-crash `clientOrderId` after reconnecting still dedupes instead of double-trading. This was verified by actually killing the server process mid-session and restarting it against the same Postgres container — resting orders, balances, and the idempotency dedupe all survived.

If a persistence write fails after a match already happened, the trade still stands (it's real and cannot be undone — no client-side reversal exists) and the response includes `persisted: false` plus a logged error, so an operator knows a reconciliation pass may be needed, rather than silently drifting out of sync.

## Auth

Real password auth (bcrypt-hashed, never returned to the client) + JWT (24h TTL — a 5-10 hour event with flaky wifi shouldn't force re-logins). The same JWT authenticates both REST (`Authorization: Bearer`) and the Socket.IO handshake (`auth: { token }`) — a socket's account identity and room membership (`account:<id>`, `role:fund_manager`) come from the verified token at connection time, never from a client-supplied id. There is no self-service admin signup; exactly one admin account is provisioned from `ADMIN_EMAIL`/`ADMIN_PASSWORD` at boot.

## Admin / organizer control surface

`/admin` (gated on `account.isAdmin`) and `/api/admin/*` (gated on the same server-side, via `requireAdmin`):
- **Force-freeze** — `Exchange.freeze()` rejects order submissions inside the matching engine itself, checked at the moment each per-symbol queued task actually runs, so it also catches anything already queued but not yet processed — not just a UI flag. Broadcast to every connected client instantly over the socket (`controlState` event) so a frozen banner appears without a refresh.
- **Window overrides** — an admin can force any of the three trading/allocation windows open or closed regardless of `CONFIG.windows`' configured timestamps, or reset back to following the config. Reconciled in `state/controlState.ts`.
- **Trigger news** — fires into both dispatch queues exactly like a normal publish, just restricted to admins.
- **Trade adjustments** — records a correction against a fill in `trade_adjustments`; the fill itself is never mutated (trades are final, no client-facing reversal — corrections are additive and explicit).
- **Account lookup + promotion** — promotion (`investor` → `fund_manager`) is an admin action that flips the role flag on the existing account; never a new account, never a redeploy, never a migration.

## Reconnection

The frontend re-fetches REST snapshots (order book depth, portfolio, pending orders) on every Socket.IO `connect` event — including automatic reconnects after a dropped wifi — rather than trusting whatever state a panel held before the drop. The idempotency key `(accountId, clientOrderId)` is what makes a reconnect-and-retry safe: this was tested directly, including a real process-restart scenario (see Persistence above) and a concurrent-double-submit race in `packages/matching-engine/test/engine.test.ts`.

## Rate limiting

Fixed-window limiter (`CONFIG.rateLimits`, default 20 orders / 10s per account) in `state/rateLimiter.ts`, enforced before an order ever reaches the matching engine. In-memory only — losing counters on a restart is an acceptable reset, unlike ledger state.

## Settlement

`packages/settlement` computes: final portfolio value (cash + holdings at freeze price + fund units at final NAV — throws rather than silently valuing a position at 0 if a freeze price/final NAV is missing) and high-water-mark performance fee checkpoints (a fee only on NAV growth past the prior peak; computes correctly to zero everywhere while `fees.performanceFeePercent` is still `null`, without changing shape once it's set). Not yet wired into a "run settlement" endpoint — the formulas are ready, the trigger/checkpoint schedule isn't, since the rulebook's checkpoint cadence isn't final either.

## Load testing

`loadtest/stockastic.js` is a k6 script simulating ~700-750 concurrent Socket.IO clients (matching `CONFIG.event.expectedConcurrentClients`) plus a concurrent HTTP order-submission burst, against the real authed + persisted stack — each virtual user signs up/logs in for a real JWT and a real account, same as production. Socket.IO layers its own protocol over WebSocket (Engine.IO framing, ping/pong, a `40{"token":...}` connect handshake), which the script speaks directly since k6's `ws` module only gives raw WebSocket frames.

```bash
k6 run loadtest/stockastic.js
BASE_URL=http://localhost:4000 WS_URL=ws://localhost:4000 VUS=750 DURATION=5m k6 run loadtest/stockastic.js
```

Actually running this (not just writing it) surfaced four real bugs, in order found:

1. **Frontend Content-Type bug.** `api.post(path)` with no body still sent `Content-Type: application/json`, and Fastify 400s trying to JSON-parse an empty body — silently breaking every no-body admin action (freeze, unfreeze, promote, window toggles) from the UI. Fixed in `apps/web/src/lib/api.ts`: only set that header when a body is actually sent.
2. **bcryptjs blocks the event loop.** At 150 concurrent order-submitting VUs, p95 order latency was ~17.6s — not a matching-engine or DB problem (both are fast), but pure-JS bcrypt hashing/comparing synchronously on Node's single thread during signup/login, starving everything else including order processing. Switched to native `bcrypt` (offloads to libuv's threadpool): p95 dropped to ~570ms, a ~30x improvement, at the same concurrency.
3. **`pg.Pool` default of 10 connections** became the next bottleneck once (2) was fixed and concurrency was pushed higher. Raised via `POSTGRES_POOL_SIZE` (default now 50) and parallelized the independent write-through queries per order (account/holdings/order/fill writes that don't depend on each other no longer serialize).
4. **A genuine Postgres race**, found at ~450 combined concurrent VUs: two concurrent HTTP requests carrying the same idempotency key both resolve through the in-memory `Exchange` (one `deduped:false`, one `deduped:true`, same order object) and both attempted to persist it concurrently. The `orders` table has two overlapping unique constraints describing the same row (`engine_order_id`, and `(account_id, client_order_id)`); Postgres's `ON CONFLICT` only suppresses the error for whichever one is named, so retargeting it just moved the race to the other constraint. Fixed properly in `apps/server/src/persistence.ts` by serializing writes per order id — the same per-key-queue pattern the matching engine already uses per symbol — rather than picking a conflict target and hoping.
5. **N+1 hydration query.** Rebuilding the idempotency cache at startup did one `loadFillsForTakerOrder` query per persisted order. At ~25k accumulated test orders, startup took over a minute. Replaced with `loadAllFillsByTakerOrder`, a single query grouped in memory — startup with 807 accounts / 32k orders now takes ~3s. This matters because a crash mid-event needs the server back up in seconds, not minutes, and startup time should scale with trade volume as little as possible.

After all four fixes: **zero duplicate-key errors, zero failed order submissions, zero WS connect failures** across repeated runs at 300 WS clients + a 150-VU order-submission ramp (450 combined concurrent VUs) against the real Postgres-persisted, JWT-authed stack, with p95 order-submission latency around 1s on a single shared dev laptop also running the browser, Next.js, and Docker.

**What wasn't validated**: a full sustained 700-750 concurrent-WS run. Pushing toward that on this same laptop (client and server sharing one machine's OS networking limits) produced auth failures and near-instant WS disconnects that look like test-harness contention (ephemeral ports, OS socket limits) rather than a server-side limit — the server logs showed no errors during that run. Before the actual event, re-run `loadtest/stockastic.js` at the full 750 VUs from separate hardware than the server, and re-check `order_submit_duration`'s threshold (currently 750ms, calibrated at 150 VUs) against whatever Postgres instance actually backs the event — the right `POSTGRES_POOL_SIZE` scales with concurrent in-flight requests, not total client count.

## What's stubbed on purpose

Per the brief, these are explicitly unfinalized in the rulebook and intentionally not built past a structural placeholder:
- Fee formulas (`CONFIG.fees.*` are `null`) — but the high-water-mark fee *shape* is implemented in `packages/settlement`, computing to zero until the % is set
- Exposure caps (`CONFIG.accounts.exposureCapPerAccount` is `null`)
- Window open/close timestamps (`CONFIG.windows.*` are `null` — everything reads `controlState.isWindowOpenNow()`, which layers a live admin override on top of the config schedule; nothing hardcodes a schedule)
- Promoted-team count (`CONFIG.promotion.promotedTeamCount`) — a config value, never baked into UI copy or logic
- Leaderboard scoring rubric / tie-break logic (`CONFIG.scoring.tieBreakStrategy = "TBD"`) — leaderboard only computes rank + portfolio value + % return
- News dispatch lead time (`CONFIG.news.fundManagerLeadTimeMs`, rumored ~60s) — read live from config on every publish, not captured at startup
- Settlement checkpoint cadence (`CONFIG.settlement.highWaterMarkCheckpointHours`) — the fee math is ready; nothing calls it on a schedule yet

## Non-negotiables already enforced

- **Idempotency**: `Exchange.submitOrder` dedupes on `(accountId, clientOrderId)`; a resubmit returns the original result (`deduped: true`) instead of matching twice — verified across a same-process race, a reconnect-and-retry, and a full process restart.
- **Finality**: no cancel-after-fill, no client-side reversal endpoint. Corrections only via `trade_adjustments` (admin-only, via `/api/admin/trade-adjustments`, never a user-facing route).
- **Fund allocate/redeem** is disabled (423 Locked, `fund_allocation_window_closed`), never hidden, whenever the fund allocation window isn't open (config schedule or live admin override).
- **Force-freeze** actually rejects in-flight order submissions inside the matching engine the instant it's triggered, not just a UI flag — verified with a test that queues a submission in the same tick as `freeze()` and confirms it still gets rejected.
