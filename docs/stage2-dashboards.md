# Phase 2: funds, investors and fund managers

Everything here is built and covered by tests (`internal/funds`, `internal/scoring`,
`internal/httpapi/funds_test.go`). The rule values are in `rulebook.json`, never in code.

Standings are hidden from teams (`leaderboard.visibleToParticipants = false`); the organiser console shows them.
Flip that one value and a Standings page appears for everyone, refreshed every `leaderboard.refreshSeconds`.

## How Phase 2 starts (organiser)

1. The Phase 1 freeze takes a snapshot: every team's value at the freeze prices, its highest value, and its trade count.
2. **Funds and prizes** shows the ranking, what decided each place, and where the cut falls. If the last qualifying place
   is decided by the coin toss, the page says so, so it can be supervised.
3. **Form the funds** ranks the teams, takes the top 20, pairs rank 1 with rank 20, 2 with 19 and so on, creates ten
   funds and makes those 20 teams fund managers. Each fund is one shared account.
4. Managers name their fund and publish its philosophy, risk profile and strategy on the **Fund desk**.

## Individual investor

| Page | What it shows |
|---|---|
| **Funds** | The ten funds: name, philosophy, risk, strategy, managers, NAV, return, AUM, and the investor's own position. The allocation window state, the mandatory 5% meter, and Invest, Withdraw and All buttons that work only while a window is open |
| **Holdings** | Stocks, fund units at the current NAV, and the team's trades |
| **Strategy log** (on Funds) | 2 to 3 sentences at a checkpoint, for Prize 3 |

Rules the server enforces: minimum investment is the lower of 5,000 or 5% of the portfolio; at most 60% of the portfolio in
one fund; money moves only in an open window; the equal-share cap (the mandatory pool is split equally across the funds, a
full fund waits until every fund has reached the same level, then all rise); leaving a fund cannot drop the team below the
mandatory 5%.

## Fund manager

| Page | What it shows |
|---|---|
| **Fund desk** | NAV, return, AUM, cash, investors, largest fall, capital kept, holdings, the profile form, and the fees at each checkpoint |
| **Trading** | The same Explore and Company pages as investors, acting for the fund. The whole fund shares the two-trades-a-minute allowance |
| **The wire, early** | News 60 seconds before the public |

NAV is computed by the server from what the fund holds; nobody types it in. Fees are computed at each window close and at
the final close (management fee on average AUM for the period, performance fee on new profit above the high-water mark)
and are never taken from investors.

## Prizes

Shown live on **Funds and prizes**, and from the frozen figures once the final freeze has happened.

| Prize | How it is decided |
|---|---|
| 1 Best fund | Score of 100 x (0.35 return + 0.25 risk management + 0.25 investor profitability + 0.15 capital kept), each scaled across the funds. Size is not an input |
| 2 Best investor | Highest final value: cash, shares, and fund units at the final NAV |
| 3 Creative investor | Judges score each criterion for investors who wrote logs at two or more checkpoints. Weights and scale are equal and out of 10 until the organisers publish the rubric |
| 4 Best risk manager | Return over largest fall, drawdown control, and diversification across holdings including fund units |

## Still to decide with the organisers

- Whether managers may see who invested in their fund (today they see counts and totals only).
- The Prize 3 rubric scale and weights, and the exact fee percentages.
- The wording used for the early and public wire.
