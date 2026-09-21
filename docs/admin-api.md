# Organiser API

What the organiser console (`apps/web/src/components/admin/`) calls. Implemented in `apps/api/internal/httpapi`. Types are in `apps/web/src/lib/adminTypes.ts`.

## Rules for every route

- **Admin only.** The server checks the JWT's admin flag on every request. The console hiding a page is a
  convenience, never the gate.
- **Every state-changing call is audited**: who, what, which target, when, and whether it succeeded (failures
  too). The console only asks the organiser to confirm; a `reason` in the body is optional and, if given,
  is kept in the audit entry.
- Timestamps are epoch milliseconds. Durations are milliseconds.
- Errors are `{ "error": "<code>" }` with a matching HTTP status. The console shows the code to the organiser.

## Reads (polled by the console)

| Route | Poll | Returns |
| --- | --- | --- |
| `GET /api/admin/overview` | 2 s | `clock` (status, elapsed, total, block index, time left), the `timeline`, and `control` (freeze, market override, window overrides, and what is actually open) |
| `GET /api/admin/systems` | 3 s | uptime, db reachable, connected people, open orders, orders and trades per minute, order save time p50 and p99, failed saves, stopped symbols, recent errors |
| `GET /api/admin/accounts` | 4 s | every account with role, status, warnings, locked, cash, held-back cash, positions, portfolio value, and whether it is online now (`online`, `sockets`, `lastSeen`) |
| `GET /api/admin/news` | 3 s | `publicDelaySeconds` and every release with its fund-manager and public delivery times (`0` = not yet) |
| `GET /api/admin/disputes` | 5 s | tickets with queue, category, due time, late flag, platform-wide flag, status |
| `GET /api/admin/audit` | 5 s | the audit log |
| `GET /api/admin/rulebook` | 60 s | version, source, the provenance list (tbf / recommended / assumption) and every value |

The schedule length is the sum of the blocks as they stand now, not a fixed five hours.
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
| `POST /api/admin/accounts/{id}/disqualify` | | disqualify: stops trading and cancels every working order (a fund is frozen at its current NAV) |
| `POST /api/admin/grants` | `accountId` (or `"*"` for every team), `symbol`, `qty`, `price` | `Ledger.Grant`: gives shares with no cash movement. This is the only way inventory enters the market |
| `GET /api/admin/accounts/{id}` | | A team's wallet, holdings, working orders, recent trades and history |
| `POST /api/admin/accounts/{id}/cash` | `amount` (rupees, negative to remove) or `setTo` (exact balance) | `Ledger.AdjustCash`. Refused if it would reach into cash held back for working orders |
| `POST /api/admin/accounts/{id}/shares` | `direction: "give" / "take" / "set"`, `symbol`, `qty` (the exact total for `set`), `price` (give only) | `Ledger.Grant` or `Ledger.Revoke`. Taking is refused for shares held back for a working sell |
| `POST /api/admin/accounts/{id}/cancel-orders` | `orderId?` (all orders if omitted) | cancels working orders on the team's behalf |
| `POST /api/admin/accounts/{id}/reinstate` | | lets a disqualified team trade again |
| `POST /api/admin/accounts/{id}/reset-password` | `password` | sets a new password. The password is never written to the audit log |
| `POST /api/admin/accounts/{id}/role` | `role: "investor" / "fund_manager"` | moves a team either way |
| `POST /api/admin/control/symbols/{symbol}` | `paused: bool` | stops or resumes new orders in one company. Cancels still work |
| `POST /api/admin/announce` | `text` | shows a desk notice on every participant's news column, and keeps it |
| `POST /api/admin/clock/block-duration` | `blockId`, `minutes` | `Clock.SetBlockDuration`: makes a block shorter or longer; everything after it moves. A finished block is refused (409) |
| `POST /api/admin/clock/end` | | `Clock.End`: finishes the event now |
| `POST /api/admin/accounts/{id}/sign-out` | | ends every login of a team: its open pages go to the sign-in screen and old tokens stop working |
| `POST /api/admin/accounts/{id}/lock` and `/unlock` | | a locked team cannot log in and has no valid sessions |
| `POST /api/admin/sign-out-all` | | signs every team out (organisers stay in) |
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
