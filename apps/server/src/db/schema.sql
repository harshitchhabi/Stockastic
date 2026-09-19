-- Stockastic schema (rulebook v1.1 model). Every statement is IF NOT EXISTS so it is safe to run each boot.
-- Numeric columns carry no scale/CHECK tied to unfinalised rulebook values.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

-- One row per platform account. 'team' = a login for a team of up to 3 (Sec 3);
-- 'fund' = the shared trading account of a merged Fund Management Team (Sec 11), no login.
CREATE TABLE IF NOT EXISTS accounts (
  id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  display_name    TEXT NOT NULL,
  email           TEXT UNIQUE NOT NULL,
  password_hash   TEXT NOT NULL,
  kind            TEXT NOT NULL DEFAULT 'team' CHECK (kind IN ('team', 'fund')),
  role            TEXT NOT NULL DEFAULT 'investor' CHECK (role IN ('investor', 'fund_manager')),
  is_admin        BOOLEAN NOT NULL DEFAULT false,
  cash_balance    NUMERIC NOT NULL,
  members         JSONB NOT NULL DEFAULT '[]'::jsonb,
  -- For role=fund_manager team logins and the fund's own account: the fund they belong to.
  fund_id         UUID,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS symbols (
  symbol          TEXT PRIMARY KEY,
  display_name    TEXT NOT NULL,
  is_active       BOOLEAN NOT NULL DEFAULT true
);

CREATE TABLE IF NOT EXISTS orders (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  engine_order_id   TEXT NOT NULL UNIQUE,
  client_order_id   TEXT NOT NULL,
  account_id        UUID NOT NULL REFERENCES accounts(id),
  symbol            TEXT NOT NULL REFERENCES symbols(symbol),
  side              TEXT NOT NULL CHECK (side IN ('buy', 'sell')),
  price             NUMERIC NOT NULL,
  qty               NUMERIC NOT NULL,
  remaining_qty     NUMERIC NOT NULL,
  status            TEXT NOT NULL,
  engine_seq        BIGINT NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (account_id, client_order_id)
);

CREATE TABLE IF NOT EXISTS fills (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  engine_fill_id      TEXT NOT NULL UNIQUE,
  symbol              TEXT NOT NULL REFERENCES symbols(symbol),
  price               NUMERIC NOT NULL,
  qty                 NUMERIC NOT NULL,
  taker_order_id      TEXT NOT NULL,
  maker_order_id      TEXT NOT NULL,
  taker_account_id    UUID NOT NULL REFERENCES accounts(id),
  maker_account_id    UUID NOT NULL REFERENCES accounts(id),
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sec 14/22: trades are final; the only correction path is an explicit admin adjustment.
CREATE TABLE IF NOT EXISTS trade_adjustments (
  id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  fill_id           UUID NOT NULL REFERENCES fills(id),
  admin_account_id  UUID NOT NULL REFERENCES accounts(id),
  reason            TEXT NOT NULL,
  adjustment_json   JSONB NOT NULL,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS holdings (
  account_id  UUID NOT NULL REFERENCES accounts(id),
  symbol      TEXT NOT NULL REFERENCES symbols(symbol),
  qty         NUMERIC NOT NULL DEFAULT 0,
  avg_price   NUMERIC NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, symbol)
);

-- Sec 8: a unitised fund run by a merged Fund Management Team.
CREATE TABLE IF NOT EXISTS funds (
  id                  UUID PRIMARY KEY,
  fund_number         INT NOT NULL UNIQUE,
  name                TEXT NOT NULL,
  philosophy          TEXT NOT NULL DEFAULT '',
  risk_profile        TEXT NOT NULL DEFAULT '',
  strategy            TEXT NOT NULL DEFAULT '',
  details_pending     BOOLEAN NOT NULL DEFAULT true,
  trading_account_id  UUID NOT NULL,
  team_account_ids    JSONB NOT NULL DEFAULT '[]'::jsonb,
  members             JSONB NOT NULL DEFAULT '[]'::jsonb,
  headcount           INT NOT NULL,
  units_outstanding   NUMERIC NOT NULL DEFAULT 0,
  status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disqualified')),
  frozen_nav          NUMERIC,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS fund_positions (
  fund_id       UUID NOT NULL REFERENCES funds(id),
  investor_id   UUID NOT NULL REFERENCES accounts(id),
  units         NUMERIC NOT NULL DEFAULT 0,
  -- Σ allocated − Σ redeemed, for Prize 1 "investor profitability".
  net_invested  NUMERIC NOT NULL DEFAULT 0,
  PRIMARY KEY (fund_id, investor_id)
);

CREATE TABLE IF NOT EXISTS fund_flows (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  fund_id       UUID NOT NULL REFERENCES funds(id),
  investor_id   UUID NOT NULL REFERENCES accounts(id),
  kind          TEXT NOT NULL CHECK (kind IN ('allocate', 'redeem', 'auto_redeem')),
  amount        NUMERIC NOT NULL,
  units         NUMERIC NOT NULL,
  nav           NUMERIC NOT NULL,
  window_index  INT,
  -- Sec 16: allocations below the minimum-investment floor never count toward AUM/retention scoring.
  counts_toward_scoring BOOLEAN NOT NULL DEFAULT true,
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sec 12/15: NAV and AUM sampled on the leaderboard refresh interval.
CREATE TABLE IF NOT EXISTS nav_samples (
  fund_id   UUID NOT NULL REFERENCES funds(id),
  t         BIGINT NOT NULL,
  nav       NUMERIC NOT NULL,
  aum       NUMERIC NOT NULL,
  units     NUMERIC NOT NULL,
  PRIMARY KEY (fund_id, t)
);

-- Running per-stage stats: Phase-1 tie-break inputs (peak, transaction count) and Phase-2 drawdown.
CREATE TABLE IF NOT EXISTS account_stats (
  account_id            UUID NOT NULL REFERENCES accounts(id),
  stage                 TEXT NOT NULL,
  peak_value            NUMERIC NOT NULL DEFAULT 0,
  max_drawdown          NUMERIC NOT NULL DEFAULT 0,
  start_value           NUMERIC,
  last_value            NUMERIC NOT NULL DEFAULT 0,
  executed_transactions INT NOT NULL DEFAULT 0,
  PRIMARY KEY (account_id, stage)
);

-- Sec 21 warnings, Sec 9 5%-minimum warnings, self-investment attempts, etc.
CREATE TABLE IF NOT EXISTS compliance_events (
  id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  account_id  UUID REFERENCES accounts(id),
  kind        TEXT NOT NULL,
  details     JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sec 22: news release timing disputes resolve against these per-feed release timestamps.
CREATE TABLE IF NOT EXISTS news_items (
  id                  UUID PRIMARY KEY,
  headline            TEXT NOT NULL,
  body                TEXT,
  kind                TEXT NOT NULL DEFAULT 'news' CHECK (kind IN ('news', 'regime')),
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  fund_manager_at     TIMESTAMPTZ,
  public_at           TIMESTAMPTZ
);

-- Live, human-operable overrides: force-freeze + manual window/market overrides.
CREATE TABLE IF NOT EXISTS control_state (
  id                BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
  trading_frozen    BOOLEAN NOT NULL DEFAULT false,
  window_overrides  JSONB NOT NULL DEFAULT '{}'::jsonb,
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Event-level singletons (event clock, freeze-price snapshots, Phase-1 results, allocation pool, ...).
CREATE TABLE IF NOT EXISTS kv (
  key         TEXT PRIMARY KEY,
  value       JSONB NOT NULL,
  updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
