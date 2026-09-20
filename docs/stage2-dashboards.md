# Stage 2 dashboards — Investors and Fund Managers

Source of truth: rulebook v1.1 (not final). Everything marked **[TBF]** or **[ASK]** depends on a value or
decision the organisers have not fixed; those stay in `rulebook.json` and are never hardcoded.

## What every Stage 2 user shares

Both roles keep the trading pages built for Stage 1: **Explore → Company → Holdings → Watchlist**, the live
**wire** on the right, and the status strip. Only the extra pages below differ. The role comes from the
verified JWT, never from the client, and the server enforces every rule the UI merely displays.

Standings are hidden from teams (`leaderboard.visibleToParticipants = false`); the organiser console shows
them. If the final rulebook publishes them (Section 15 describes public boards), flip that one value and a
**Standings** page appears for everyone, refreshed every `leaderboard.refreshSeconds` (5 minutes).

## Individual Investor (~640–690 people, teams of 3, one shared account)

The question this dashboard answers: *"Do I beat the fund managers myself, or give them part of my capital?"*

| Page | What it shows | Rulebook |
| --- | --- | --- |
| **Funds** | The 10 funds side by side. Each card: name, philosophy, risk profile, strategy, NAV/unit, % return, AUM, risk rating. Never a fund's holdings. | §8, §15 |
| **Fund page** | NAV chart (sampled every `navSampleSeconds`), the five published profile fields, the management team, **Allocate / Redeem** form. | §8, §10 |
| **Window banner** (every page) | Which allocation window is open, a countdown to its **hard close**, or "next window opens in…". After Window 3 closes: "fund positions locked". | §10 |
| **Allocate form** | Live validation before submit: minimum `min(₹5,000, 5% of wallet)`, maximum 60% of wallet in one fund, units you would receive = amount ÷ NAV, fund cap remaining, and a clear refusal for your own team's fund. Disabled outside an open window. | §9, §10 [TBF numbers] |
| **Holdings** (extended) | Stocks **and** fund units in one net-worth view, plus a wallet mix bar: cash / stocks / funds, always totalling 100%. | §10 |
| **5% minimum meter** | Your fund allocation vs the mandatory 5%, the next checkpoint (close of each window), and any warning already issued. | §9 |
| **Strategy log** | Submit the 2–3 short strategy notes at the fixed checkpoints; shows deadlines and what was submitted. | §16 Prize 3 |
| **Trade limit meter** (ticket) | Your account's 2 trades per minute: trades used, seconds until one frees up. The whole team shares it. | §11, §14 |
| **Disputes** | Raise a dispute (window `raiseWithinMinutes`), see the 3 expedited slots left this phase, and its status. | §22 |

## Fund Manager (60 people, 10 funds of 6, one shared fund account)

The question this dashboard answers: *"How is my fund doing, and what do I do with the news I got first?"*

| Page | What it shows | Rulebook |
| --- | --- | --- |
| **Fund desk** | NAV/unit, return since launch, AUM, units outstanding, high-water mark, cap used vs cap allowed, investors in (count), net flow per window. NAV is **computed by the server** from the fund's holdings. | §9, §10, §12 |
| **The wire · early** | News at T = 0 with a live countdown on each item: "public in 41 s". This is the institutional advantage, shown honestly as timing only. | §11 |
| **Trading** | Explore / Company / Holdings exactly as investors have, but acting for the fund account. The 2-trades-per-minute meter is large: all 6 members share one cap. | §11 |
| **Fees** | Management fee accrued (time-weighted AUM), provisional performance fee above the high-water mark, both labelled **provisional until final settlement** (clawback). Kept apart from investor-facing NAV, which is never reduced by fees. Short-team proration (headcount ÷ 6) applied and shown. | §12 |
| **Fund profile** | Edit the five published fields (name, philosophy, risk profile, strategy) until Phase 2 starts; read-only after. Fictional names only. | §8 |
| **Team** | The members, optional internal roles (equity analyst, macro analyst, risk, IR/PM), headcount and the proration factor. | §6, §17 |
| **Windows** | Timeline of Windows 0–3 with hard closes, and the note that unused cap is forfeited at close. | §10 |
| **Disputes** | Same as investors. | §22 |

## Issues found in the current code

1. **`FundManagerPanel` lets a manager type in the NAV.** The rulebook says NAV moves automatically with the
   fund's portfolio (§10). That control must go; Stage 2 shows NAV read-only.
2. **`FundBrowser` sends units, not an amount.** The rulebook allocates capital and issues units at the
   current NAV. The form should take an amount and show the units.
3. **The fund-manager breakdown lists each investor's units.** Section 15 only makes aggregate figures public.
   **[ASK]** whether managers may see who invested; until answered, show counts and totals only.

## Needs an answer from the organisers

- **[ASK]** Liquidity: who supplies prices and market-making — still blocking Phase 1 trading.
- **[ASK]** Should teams ever see the standings (Section 15 says yes; current setting says no)?
- **[TBF]** Fee percentages and basis, minimum/maximum investment, window timings, cap size.
- **[ASK]** The company universe: symbols, names, sectors and opening prices (the Explore filters and
  day-change colouring are built for them and need no code change).

## Build order for Stage 2

1. Server: `funds` package (units, NAV, windows, caps, self-investment ban, 5% checks), then its API.
2. Investor: Funds → Fund page → allocate form → wallet mix and 5% meter.
3. Fund manager: Fund desk → early wire countdown → Fees → Profile → Team.
4. Shared: window banner, trade-limit meter, Disputes, Strategy log.
5. Run the full-event rehearsal with real fund accounts, then the load test above 450 users.
