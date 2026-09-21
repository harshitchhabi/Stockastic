# Data files: how the master data becomes the game

The server needs two files. Everything about prices and news lives in them, so when the organisers send the real
data, only these files change. No code changes.

| File | Variable | What it holds |
|---|---|---|
| `universe.json` | `UNIVERSE_PATH` | The companies: symbol, name, sector, opening price |
| `scenario.json` | `SCENARIO_PATH` | How prices move: volatility, price limits, scheduled market events, bull and bear runs |

`apps/api/scenarios/mock/` holds mock versions made from `Stockastic_Master.xlsx` (150 companies, 62 market events,
3 bull or bear runs). They are the test data until the real workbook arrives.

## Turning the workbook into the two files

```bash
python tools/import_master.py Stockastic_Master.xlsx apps/api/scenarios/mock
```

Needs Python with `openpyxl`. Run it again whenever the workbook changes. A test (`sim.TestMockScenarioMatchesMockUniverse`)
loads the generated files, so a bad import is caught before the event.

The importer reads these sheets:

| Sheet | Used for |
|---|---|
| `UNIVERSE` | ticker, name, sector, `P_sim` (the opening price) and `vol_band` (how much each company moves) |
| `RUN_SHEET` | one row per news item: time (`T+HH:MM:SS`, since the event started), type, category, headline |
| `IMPACT_MAP` | for each `REAL` news item, which sectors or single companies it moves and by how much |
| `PARAMETERS` | price floor, price cap, price rounding, price impact coefficient |
| `FUND_FEED`, `PARTICIPANT_FEED`, `SECTOR_ALLOCATION` | not read: they repeat what the sheets above already say |

### How each item behaves

- **REAL**: the headline is released, and the prices in its impact rows move.
- **FAKE**: the headline is released and nothing moves (a rumour). Teams are not told it is fake.
- **DENIAL**: the headline is released and nothing moves.
- **REGIME**: a bull or bear run starts and lasts to the end of that trading block. It nudges most companies, not all
  of them and not equally.
- The release time in the sheet is when **fund managers** see it. The public sees it 60 seconds later in Phase 2, and the
  price reaction happens at that public time, so fund managers have the 60 seconds to act first. In Phase 1 there is no delay.

### Assumptions in the importer (edit the constants at the top of `tools/import_master.py`)

These are the places where the workbook does not say exactly what to do, so a choice was made.

| Constant | Value | Meaning |
|---|---|---|
| `VOL_BAND_TO_TICK_PCT` | 0.1 | A `vol_band` of 0.02 becomes a 0.2% typical move per price update |
| `TICK_SECONDS` | 60 | One price update a minute while the market is open |
| `RAMP_MINUTES` | 3 | An event's price move is spread over 3 minutes |
| `REGIME_DRIFT` | 0.03 / 0.05 / 0.10 % | Push per price update for a MINOR / MODERATE / MAJOR run |
| `REGIME_BREADTH` | 0.7 | A run drives 70% of companies |
| Impact | `impact x coefficient` | An impact of 0.02 with a coefficient of 0.7 moves prices 1.4% |

## The file formats

`universe.json` is a list:

```json
[{ "symbol": "SMZK", "name": "Saruti Muzuki", "sector": "Automobile & Auto Components", "openPrice": 1788.85 }]
```

`scenario.json`:

```json
{
  "seed": 20260921,
  "tickSeconds": 60,
  "volatility": { "defaultPct": 0.3, "bySector": { "Information Technology": 0.4 }, "bySymbol": { "SMZK": 0.2 } },
  "priceFloor": 50, "priceCap": 2500, "roundTo": 0.05,
  "events": [
    { "id": "e03", "atMinute": 27.83, "headline": "…", "type": "REAL", "category": "GLOBAL",
      "impacts": [ { "sector": "Oil, Gas & Energy", "shockPct": 4.2, "rampMinutes": 3 },
                   { "symbol": "INGA", "shockPct": -4.9, "rampMinutes": 3 } ] }
  ],
  "regimes": [
    { "id": "e11", "kind": "bull", "atMinute": 43.5, "durationMinutes": 16.5, "driftPctPerTick": 0.05, "breadth": 0.7, "headline": "…" }
  ],
  "paths": { "SMZK": [[0, 1788.85], [30, 1800.0], [60, 1750.0]] }
}
```

- `paths` is optional. A company with a path follows exactly those `[minutes since the event started, price]` points
  (straight lines between them) and ignores volatility and events. Use it if the organisers want specific price stories.
- Volatility is looked up by company, then by sector, then the default.
- `seed` makes the random movement repeatable: the same seed gives the same prices, and a restarted server carries on
  exactly where it was.
- Every company, sector and event name is checked at start-up. A typo is an error, not a silent gap.

If the organisers provide exact price tables instead of rules for how prices change, use `paths` for every company, or
tell the developer: the price simulation is one file (`internal/sim/engine.go`) behind a small interface, so a different
way of producing prices replaces it without touching trading, funds or the screens.

## Things the mock data cannot tell us

- The real company count (the rulebook says about 250; the mock has 150).
- Whether prices should be exact scripted paths or rules with randomness.
- The scale of `vol_band` (per update, per block, or per event).
