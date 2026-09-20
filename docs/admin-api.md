# Organiser API

What the organiser console (`apps/web/src/components/admin/`) calls. The Go `httpapi` package must implement
exactly this. Types are in `apps/web/src/lib/adminTypes.ts`.

## Rules for every route

- **Admin only.** The server checks the JWT's admin flag on every request. The console hiding a page is a
  convenience, never the gate.
- **Every state-changing call carries `reason`** (at least 5 characters). A missing or short reason is `400`.
  The server writes one audit entry per call: who, what, which target, why, and whether it succeeded.
  Failures are audited too.
- Timestamps are epoch milliseconds. Durations are milliseconds.
- Errors are `{ "error": "<code>" }` with a matching HTTP status. The console shows the code to the organiser.

## Reads (polled by the console)

| Route | Poll | Returns |
| --- | --- | --- |
| `GET /api/admin/overview` | 2 s | `clock` (status, elapsed, total, block index, time left), the `timeline`, and `control` (freeze, market override, window overrides, and what is actually open) |
| `GET /api/admin/systems` | 3 s | uptime, db reachable, connected people, open orders, orders and trades per minute, order save time p50 and p99, failed saves, stopped symbols, recent errors |
| `GET /api/admin/accounts` | 10 s | every account with role, status (`active` / `warned` / `disqualified`), warnings, cash, portfolio value |
| `GET /api/admin/news` | 3 s | `publicDelaySeconds` and every release with its fund-manager and public delivery times (`0` = not yet) |
| `GET /api/admin/disputes` | 5 s | tickets with queue, category, due time, late flag, platform-wide flag, status |
| `GET /api/admin/audit` | 5 s | the audit log |
| `GET /api/admin/rulebook` | 60 s | version, source, the provenance list (tbf / recommended / assumption) and every value |

The window count in `control.windowOverrides` and `control.windowsOpen` comes from the rulebook timeline,
so a rulebook with five windows shows five switches with no console change.

## Actions (all take `{ reason, ... }`)

| Route | Extra body | Maps to |
| --- | --- | --- |
| `POST /api/admin/clock/start` | | `Clock.Start` |
| `POST /api/admin/clock/pause` | | `Clock.Pause` |
| `POST /api/admin/clock/resume` | `compressBlockId?` | `Clock.Resume` |
| `POST /api/admin/clock/nudge` | `minutes` (±) | `Clock.Nudge` |
| `POST /api/admin/clock/jump` | `blockId` | `Clock.JumpTo` |
| `POST /api/admin/control/freeze` | `frozen: bool` | `Clock.SetFrozen` and the engine freeze |
| `POST /api/admin/control/market` | `override: "open" / "closed" / null` | `Clock.SetMarketOverride` |
| `POST /api/admin/control/windows/{i}` | `override: "open" / "closed" / null` | `Clock.SetWindowOverride` |
| `POST /api/admin/symbols/{symbol}/resume` | | `Engine.Resume` |
| `POST /api/admin/accounts/{id}/promote` | | move a team into the fund manager role |
| `POST /api/admin/accounts/{id}/warn` | | formal warning (rulebook Section 21) |
| `POST /api/admin/accounts/{id}/disqualify` | | disqualify; a fund is frozen at its current NAV |
| `POST /api/admin/news` | `kind: "news" / "regime"`, `headline`, `body?` | `news.Dispatcher.Publish` |
| `POST /api/admin/disputes/{id}/triage` | `platformWide: bool` | `disputes.Tracker.Triage` |
| `POST /api/admin/disputes/{id}/resolve` | | resolves; `reason` is the resolution shown to the team |
| `POST /api/admin/trade-adjustments` | `fillId`, `adjustment` | records a correction; the trade is never changed |

Publishing news for the two feeds and the public delay is entirely the server's job (`news.Dispatcher`); the
console only displays the resulting times.

## Not built yet

- **Funds** admin page (fund list, NAV, AUM, freeze, the 5% compliance status per team). It needs the Stage 2
  `funds` package first.
- **Qualification preview** (the Top 20 line and the mirror pairing). It should come from the `scoring`
  package on the server, not be recomputed in the browser.
