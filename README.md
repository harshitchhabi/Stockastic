# Stockastic

Live financial-market simulation event platform. Governing spec: `Live_Financial_Ecosystem_Rulebook_v1.1.pdf` (not final — values change).

## Layout

| Path | What |
|---|---|
| `packages/config` | **`rulebook.json`** — single source of truth for every rulebook value, plus its JSON Schema. The Go backend loads the JSON directly; web types are generated from the schema. Values the rulebook marks Recommended / TBF, and gaps we filled with an assumption, are tagged in its `provenance` map |
| `apps/api` | Go backend (`stockastic/api`). Built so far: `rulebook`, `scoring`, `engine`, `ledger`, `eventclock`, `news`, `ratelimit`, `disputes`. Still stubs: `store` (pgx), `auth`, `httpapi` (Gin), `wsapi`, `funds`, `obs`, `config` |
| `apps/web` | Next.js frontend. Still speaks the deleted TS backend's REST + Socket.IO API; to be ported to plain JSON-over-WS once the Go contract exists |
| `loadtest/` | k6 load tests (to be written against the Go API) |

The previous TypeScript backend lives only on the `archive/ts-backend` branch.

## Commands

```bash
# rulebook
npm run check -w @stockastic/config     # validate rulebook.json against the schema + cross-field rules
npm run gen   -w @stockastic/config     # regenerate TS types from the schema (commit the result)

# backend
cd apps/api && go vet ./... && go test -race ./...
```

Changing a rulebook value = edit `rulebook.json`, run `check`. Adding a field = edit the schema, the Go struct in
`internal/rulebook`, and run `gen`; the Go loader decodes strictly, so a key missing from the struct fails loudly.

## Reliability design (engine)

- One goroutine per symbol, buffered command channel; symbols run in parallel, orders within a symbol strictly serial.
- **Write-before-ack**: a match is planned read-only, committed to the journal, and only then applied. A failed commit changes nothing.
- Idempotent on `(account, clientOrderId)`, including concurrent duplicates and post-restart retries.
- A panic halts only that symbol until `Resume` rebuilds it from durable state.
- Prices are integer paise end to end (`internal/money`); no floating point where value moves.
