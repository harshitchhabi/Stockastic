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
| `GET /api/admin/systems` | 3 s | uptime, log writable, connected people, trades per minute, trade save time p50 and p99, failed saves, free disk space, price update count and time, recent errors |
| `GET /api/admin/accounts` | 4 s | every account with role, status, warnings, locked, cash, positions, portfolio value (fund units included), and whether it is online now (`online`, `sockets`, `lastSeen`) |
| `GET /api/admin/news` | 3 s | `publicDelaySeconds` and every release with its fund-manager and public delivery times (`0` = not yet) |
| `GET /api/admin/disputes` | 5 s | tickets with category, decide-by time, late flag, status |
| `GET /api/admin/audit` | 5 s | the audit log |
| `GET /api/admin/sim` | 4 s | the price simulation: every scheduled event and bull or bear run with its time, type (REAL, FAKE, DENIAL), whether it has fired, and how many prices it moves |
| `GET /api/admin/qualification` | 5 s | the Phase 1 ranking with what decided each place, the cut, and whether the funds are formed |
| `GET /api/admin/funds` | 5 s | every fund: NAV, return, AUM, investors, largest fall, capital kept, share of investors in profit, checkpoint fees |
| `GET /api/admin/prizes` | 8 s | Prizes 1 to 4 ranked (live until the final freeze, then from the frozen figures) |
| `GET /api/admin/strategy-logs` | 10 s | the rubric and every investor's strategy log entries and judges' scores |
| `GET /api/admin/schedule` | | the schedule as it is now, and the rulebook timeline that can be loaded as a starting point |
| `GET /api/admin/rulebook` | 60 s | version, source, the provenance list (tbf / recommended / assumption) and every value |

The schedule length is the sum of the blocks as they stand now, not a fixed five hours.
The window count in `control.windowOverrides` and `control.windowsOpen` comes from the rulebook timeline,
so a rulebook with five windows shows five switches with no console change.

## Actions (all take `{ reason, ... }`)

| Route | Extra body | Maps to |
| --- | --- | --- |
| `POST /api/admin/clock/start` | `blockId?` | `Clock.StartAt`: starts the event at the chosen block (the first one if omitted). Blocks before it count as done |
| `POST /api/admin/clock/pause` | | `Clock.Pause` |
| `POST /api/admin/clock/resume` | `compressBlockId?` | `Clock.Resume` |
| `POST /api/admin/clock/nudge` | `minutes` (±) | `Clock.Nudge` |
| `POST /api/admin/clock/jump` | `blockId` | `Clock.JumpTo` |
| `POST /api/admin/control/freeze` | `frozen: bool` | `Clock.SetFrozen`: every trade is refused while frozen |
| `POST /api/admin/control/market` | `override: "open" / "closed" / null` | `Clock.SetMarketOverride` |
| `POST /api/admin/control/windows/{i}` | `override: "open" / "closed" / null` | `Clock.SetWindowOverride` |
| `PUT /api/admin/schedule` | `blocks: [{ id, label, minutes, stage, marketOpen, allocationWindow, freezeSnapshot }]` | replaces the whole schedule, before or during the event. The clock keeps its time. The rulebook timeline is only a template: nothing in the code depends on any block name |
| `POST /api/admin/schedule/template` | | loads the rulebook timeline as the schedule |
| `POST /api/admin/event/reset` | | resets the whole event: cash and shares back to the start, opening prices, clock before the start, and funds, trades, news, disputes and snapshots erased. Accounts, passwords and the schedule are kept; warnings and roles are cleared. Survives a restart |
| `POST /api/admin/snapshots/{phase1\|final}` | | freezes every team's value now (also done by a schedule block with `freezeSnapshot`). Once each |
| `POST /api/admin/funds/dissolve` | | takes the funds apart so they can be formed again. Refused once anyone has invested |
| `POST /api/admin/funds/{id}/trader` | `accountId` | chooses which of the fund's two teams places its trades |
| `POST /api/admin/sim/{id}/fire` | | releases a scheduled market event or run now; it will not fire again |
| `POST /api/admin/qualification/run` | `pairs?: [[traderId, otherId], ...]` | forms the funds. With no pairs it ranks Phase 1 from the freeze snapshot and pairs the top teams first with last. With pairs, the organiser chooses who is merged with whom (up to the rulebook's fund count); the first team of each pair places the fund's trades |
| `POST /api/admin/funds/{id}/disqualify` | | removes a fund from Prize 1 |
| `POST /api/admin/strategy-logs/{account}/score` | `scores: { criterion: number }` | a judge's Prize 3 scores |
| `POST /api/admin/accounts/{id}/promote` | | move a team into the fund manager role |
| `POST /api/admin/accounts/{id}/warn` | | formal warning (rulebook Section 21) |
| `POST /api/admin/accounts/{id}/disqualify` | | disqualify: stops all trading. Trades already made stand |
| `POST /api/admin/grants` | `accountId` (or `"*"` for every team), `symbol`, `qty`, `price` | `Ledger.Grant`: gives shares with no cash movement. This is the only way inventory enters the market |
| `GET /api/admin/accounts/{id}` | | A team's wallet, holdings, recent trades and history |
| `POST /api/admin/accounts/{id}/cash` | `amount` (rupees, negative to remove) or `setTo` (exact balance) | `Ledger.AdjustCash`. Refused if it would take cash below zero |
| `POST /api/admin/accounts/{id}/shares` | `direction: "give" / "take" / "set"`, `symbol`, `qty` (the exact total for `set`), `price` (give only) | `Ledger.Grant` or `Ledger.Revoke`. Taking more than the team holds is refused |
| `POST /api/admin/accounts/{id}/reinstate` | | lets a disqualified team trade again |
| `POST /api/admin/accounts/{id}/reset-password` | `password` | sets a new password. The password is never written to the audit log |
| `POST /api/admin/accounts/{id}/role` | `role: "investor" / "fund_manager"` | moves a team either way |
| `POST /api/admin/control/symbols/{symbol}` | `paused: bool` | stops or resumes trading in one company |
| `POST /api/admin/announce` | `text` | shows a desk notice on every participant's news column, and keeps it |
| `POST /api/admin/clock/block-duration` | `blockId`, `minutes` | `Clock.SetBlockDuration`: makes a block shorter or longer; everything after it moves. A finished block is refused (409) |
| `POST /api/admin/clock/end` | | `Clock.End`: finishes the event now |
| `POST /api/admin/accounts/{id}/sign-out` | | ends every login of a team: its open pages go to the sign-in screen and old tokens stop working |
| `POST /api/admin/accounts/{id}/lock` and `/unlock` | | a locked team cannot log in and has no valid sessions |
| `POST /api/admin/sign-out-all` | | signs every team out (organisers stay in) |
| `POST /api/admin/news` | `kind: "news" / "regime"`, `headline`, `body?` | `news.Dispatcher.Publish` |
| `POST /api/admin/disputes/{id}/resolve` | | resolves; `reason` is the resolution shown to the team |
| `POST /api/admin/trade-adjustments` | `fillId`, `adjustment` | records a correction; the trade is never changed |

Publishing news for the two feeds and the public delay is entirely the server's job (`news.Dispatcher`); the
console only displays the resulting times.

## Phase 2 routes for participants

| Route | Who | What |
| --- | --- | --- |
| `GET /api/funds` | anyone signed in | the funds with profile, NAV, return, AUM, investors, the caller's own position, and how much each can take now under the equal-share cap; also whether an allocation window is open |
| `POST /api/funds/{id}/allocate` | investors | `amount` in rupees. Units = amount / NAV at that moment. Refused unless a window is open, and for: below the minimum (lower of 5,000 or 5% of the portfolio), above 60% of the portfolio in one fund, not enough cash, or the fund is at its share for the window |
| `POST /api/funds/{id}/redeem` | investors | `amount` or `all: true`. If the fund is short of cash it sells a slice of every holding at current prices. Refused if it would leave less than the mandatory share (5%) in funds |
| `GET /api/funds/mine`, `PUT /api/funds/mine/profile` | fund managers | the fund's cash, holdings, checkpoints and fees; publish the name, philosophy, risk profile and strategy |
| `POST /api/strategy-log` | investors | 2 to 3 sentences at a checkpoint (Prize 3) |
| `POST /api/trades` | anyone | a fund manager's trade uses the fund's cash and holdings, and the whole fund shares one two-trades-a-minute allowance. Only the fund's trader may place trades: the fund's other team gets `not_the_trader` (403) and sees the fund read-only |

## Choices made where the rulebook is silent

These are recorded so they can be changed in one place if the organisers decide otherwise.

- Capital retention (Prize 1) is the share of units ever issued that are still invested, so NAV moves do not change it.
- A fund's investor profitability counts investors whose current units plus what they took out exceed what they put in.
- The equal-share cap is counted per allocation window on money put in (withdrawals do not reduce it), and applies whatever the size of the investor.
- The 5% mandatory share is enforced when leaving a fund. It is reported, not penalised, when a team is below it: warnings and disqualification stay with the organisers (Section 21).
- Fees are computed at each window close and at the final close, per period, and are never deducted from investors.
