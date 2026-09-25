# Data files: how the final workbook becomes the game

The server loads three files. Everything about companies, prices and news is in them, so a new workbook means new
files and no code change.

| File | Variable | What it holds |
|---|---|---|
| `universe.json` | `UNIVERSE_PATH` | The 150 companies: symbol, game name, sector, opening price |
| `scenario.json` | `SCENARIO_PATH` | The news schedule, and where the price table is |
| `prices.json` | (named inside `scenario.json`) | The exact price of every company at every 10 seconds of open-market time |

## The final data is a secret, so it is not in git

`prices.json` and the news schedule are the event's future. They are generated into `apps/api/scenarios/final/`, which is
git-ignored, and so is the workbook. Copy those three files to the server by hand (for example to `/etc/stockastic/`) and
point `UNIVERSE_PATH` and `SCENARIO_PATH` at them. `apps/api/scenarios/mock/` is a small sample that is safe to commit and is
what the tests use.

## Turning the workbook into the files

```bash
python tools/import_final.py Stochasticdata.xlsx apps/api/scenarios/final
```

Needs Python with `openpyxl`. It reads `UNIVERSE` (game names and sectors, never the real names), `PRICES_10S` (the price
table; bar 0 is the opening price) and `RUN_SHEET` (the 40 news items). It refuses to continue if the companies in the two
sheets do not match or a news item is not inside live trading. The other sheets (`IMPACT_MAP`, `RUMOUR_SPIKES`, the two
feeds, `DATA_CHECKS`) describe how the prices were built and are not needed at run time.

## How the game uses them

- **Prices follow the table exactly.** Every 10 seconds of open-market time the next row is applied. Nothing is random.
  The step is counted in open-market time, not clock time, so pausing, closing the market, or editing the schedule cannot
  put prices out of step with the data. The table covers 170 minutes of trading (1,020 steps). If the market stays open
  longer, prices hold their last values.
- **News goes out by itself.** Each of the 40 items has a time in open-market minutes (worked out from the workbook's
  event times). In Phase 1 everyone sees it at once. In Phase 2 fund managers see it first and the public 60 seconds later.
  Bull and bear run announcements go to everyone at once. Real news, rumours and denials look the same to teams;
  only the organiser sees the type.
- **Breaks are safe.** Prices and news run on open-market time, so a closed stretch (for example the break between Phase 1 and
  Phase 2) pauses both and they carry on afterwards. With the rulebook timeline the 40 minute break sits between
  market minute 40 and 41, exactly where the data expects it.
- **The schedule is checked against the data.** Under the Schedule editor, the organiser sees how many minutes of open trading the
  schedule gives against the 170 the data covers, the breaks, news that would never go out, and news that lands in a block of a
  different phase than the data was designed for (which would change the fund managers' head start). Each news item also shows the
  event-clock time it will go out at under the current schedule.
- **The organiser is in charge of the news.** On **News and market events** they can turn automatic release off and send
  each item by hand, hold any item, reword it or change its time, or release it now. This changes only what people read.
  Prices come from the table whatever the organiser does with the news.
- **Single-stock limit.** A buy is refused if it would put more than 25% of the portfolio's value in one company (the data's
  "Max single-stock holding"). It is checked when the order is placed, so a holding that grows past 25% because its price
  rose is not forced out. Sells are always allowed. The value is in `rulebook.json` (`market.maxSingleStockPercent`).

## Things to know

- The rulebook text says about 250 companies. The data has 150, and `rulebook.json` now says 150.
- Prices go slightly outside the Rs 50 to 2,500 range in about 1.5% of cells (the workbook's own check reports this as
  information only). The table is used exactly as given: nothing is clamped.
- The parameters sheet's volatility bands, tiers and impact coefficient describe how the table was generated, so they are not
  used by the server.
- Every 10 seconds the prices that changed go to every browser in one small message (about 150 numbers), so the price
  feed adds almost nothing to the load.

## Other formats the simulation still understands

The price simulation can also run without a table: random movement scaled by volatility with market events and bull and
bear runs (`apps/api/scenarios/mock`), or exact scripted paths for chosen companies. Those are described by the fields in
`internal/sim/scenario.go`. With a `pricesFile` set, the table wins and the other price fields are ignored.
