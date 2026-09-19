# Stockastic

Live financial-market simulation event platform. Governing spec: `Live_Financial_Ecosystem_Rulebook_v1.1.pdf` (not final — values change).

## Layout

| Path | What |
|---|---|
| `apps/api` | Go backend (scaffold — packages and boundaries only, no implementation yet) |
| `apps/web` | Next.js frontend. Still speaks the deleted TS backend's REST + Socket.IO API; needs porting to the Go API contract |
| `packages/config` | Rulebook constants (timeline, fees, caps, prize weights). Rulebook data only |
| `loadtest/` | k6 load tests (to be written against the Go API) |

The previous TypeScript backend lives only on the `archive/ts-backend` branch (reference: its test cases are the port target).
