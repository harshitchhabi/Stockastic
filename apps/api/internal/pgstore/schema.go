package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"stockastic/api/internal/store"
)

// schemaVersion is bumped when the tables change. Every step is written so it can be run again safely.
const schemaVersion = 1

const schemaSQL = `
-- ---- the durable record: the only thing the platform's state is rebuilt from ----
CREATE TABLE IF NOT EXISTS events (
  seq     bigserial PRIMARY KEY,
  rid     uuid        NOT NULL UNIQUE,
  kind    text        NOT NULL,
  at      timestamptz NOT NULL,
  payload json        NOT NULL
);
CREATE INDEX IF NOT EXISTS events_kind_idx ON events (kind);

CREATE TABLE IF NOT EXISTS schema_info (key text PRIMARY KEY, value text NOT NULL);

-- ---- tables for looking things up, derived from the events (see projector.go) ----
CREATE TABLE IF NOT EXISTS projector_state (
  id       int PRIMARY KEY CHECK (id = 1),
  last_seq bigint NOT NULL DEFAULT 0,
  epoch    int    NOT NULL DEFAULT 1
);
INSERT INTO projector_state (id) VALUES (1) ON CONFLICT DO NOTHING;

CREATE TABLE IF NOT EXISTS accounts (
  id text PRIMARY KEY, email text NOT NULL, display_name text NOT NULL, role text NOT NULL, is_admin boolean NOT NULL,
  status text NOT NULL, warnings int NOT NULL, locked boolean NOT NULL, session_version int NOT NULL,
  created_at timestamptz, updated_at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS accounts_email_idx ON accounts (lower(email));

CREATE TABLE IF NOT EXISTS trades (
  seq bigint PRIMARY KEY, epoch int NOT NULL, trade_id text NOT NULL, client_trade_id text, account_id text NOT NULL,
  symbol text NOT NULL, side text NOT NULL, qty bigint NOT NULL, price_paise bigint NOT NULL, value_paise bigint NOT NULL,
  stage text, at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS trades_account_idx ON trades (epoch, account_id, at);
CREATE INDEX IF NOT EXISTS trades_symbol_idx ON trades (epoch, symbol, at);

-- every change to a team's cash or shares, in one place
CREATE TABLE IF NOT EXISTS ledger_entries (
  seq bigint NOT NULL, line int NOT NULL DEFAULT 0, epoch int NOT NULL, account_id text NOT NULL, at timestamptz NOT NULL,
  type text NOT NULL, symbol text, shares_delta bigint NOT NULL DEFAULT 0, cash_delta_paise bigint NOT NULL DEFAULT 0, ref text,
  PRIMARY KEY (seq, line)
);
CREATE INDEX IF NOT EXISTS ledger_account_idx ON ledger_entries (epoch, account_id, at);

-- each team's cash and total value, once a minute
CREATE TABLE IF NOT EXISTS wallet_history (
  epoch int NOT NULL, account_id text NOT NULL, at timestamptz NOT NULL, cash_paise bigint NOT NULL, value_paise bigint NOT NULL,
  PRIMARY KEY (epoch, account_id, at)
);

-- sign-ins, sign-outs, connections and other things a person did
CREATE TABLE IF NOT EXISTS activity (
  seq bigint PRIMARY KEY, epoch int NOT NULL, account_id text, type text NOT NULL, ip text, user_agent text, detail text,
  at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS activity_account_idx ON activity (account_id, at);
CREATE INDEX IF NOT EXISTS activity_ip_idx ON activity (ip, at);

CREATE TABLE IF NOT EXISTS price_ticks (
  epoch int NOT NULL, tick int NOT NULL, symbol text NOT NULL, price_paise bigint NOT NULL, at timestamptz NOT NULL,
  PRIMARY KEY (epoch, tick, symbol)
);

CREATE TABLE IF NOT EXISTS funds (
  epoch int NOT NULL, fund_id text NOT NULL, number int NOT NULL, name text, risk text, strategy text, philosophy text,
  members text[] NOT NULL, trader text, PRIMARY KEY (epoch, fund_id)
);
CREATE TABLE IF NOT EXISTS fund_flows (
  seq bigint PRIMARY KEY, epoch int NOT NULL, fund_id text NOT NULL, investor text NOT NULL, kind text NOT NULL, window_no int,
  amount_paise bigint, units double precision, nav double precision, at timestamptz NOT NULL
);
CREATE INDEX IF NOT EXISTS fund_flows_investor_idx ON fund_flows (epoch, investor);

CREATE TABLE IF NOT EXISTS audit (
  seq bigint PRIMARY KEY, epoch int NOT NULL, id text, at timestamptz NOT NULL, actor text, action text, target text, reason text, ok boolean
);
CREATE TABLE IF NOT EXISTS news_releases (
  epoch int NOT NULL, id text NOT NULL, kind text, headline text, created_at timestamptz, fund_manager_at timestamptz, public_at timestamptz,
  PRIMARY KEY (epoch, id)
);
CREATE TABLE IF NOT EXISTS disputes (
  epoch int NOT NULL, id text NOT NULL, account_id text, category text, summary text, raised_at timestamptz, status text, resolution text,
  PRIMARY KEY (epoch, id)
);
CREATE TABLE IF NOT EXISTS freezes (
  epoch int NOT NULL, name text NOT NULL, account_id text NOT NULL, value_paise bigint NOT NULL, peak_paise bigint, trades int, at timestamptz NOT NULL,
  PRIMARY KEY (epoch, name, account_id)
);

-- ready-made answers
CREATE OR REPLACE VIEW v_positions AS
  SELECT epoch, account_id, symbol, sum(shares_delta) AS qty
  FROM ledger_entries WHERE symbol IS NOT NULL GROUP BY epoch, account_id, symbol HAVING sum(shares_delta) <> 0;
CREATE OR REPLACE VIEW v_cash_change AS
  SELECT epoch, account_id, sum(cash_delta_paise) AS cash_change_paise FROM ledger_entries GROUP BY epoch, account_id;
CREATE OR REPLACE VIEW v_team_activity AS
  SELECT a.id, a.display_name, a.email, a.status, a.locked,
         (SELECT max(at) FROM activity x WHERE x.account_id = a.id AND x.type = 'login') AS last_login,
         (SELECT count(*) FROM activity x WHERE x.account_id = a.id AND x.type = 'login') AS logins,
         (SELECT count(DISTINCT ip) FROM activity x WHERE x.account_id = a.id AND x.ip IS NOT NULL) AS addresses
  FROM accounts a;
-- accounts that have signed in from the same address, as a hint (many people can share one venue address)
CREATE OR REPLACE VIEW v_shared_addresses AS
  SELECT ip, count(DISTINCT account_id) AS accounts, array_agg(DISTINCT account_id) AS account_ids
  FROM activity WHERE type = 'login' AND ip IS NOT NULL GROUP BY ip HAVING count(DISTINCT account_id) > 1;
`

func migrate(ctx context.Context, c *pgx.Conn) error {
	if _, err := c.Exec(ctx, schemaSQL); err != nil {
		return err
	}
	_, err := c.Exec(ctx, `INSERT INTO schema_info (key, value) VALUES ('version', $1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, fmt.Sprint(schemaVersion))
	return err
}

// compact removes records that a newer one replaces, so the record stays bounded however many times the server
// is restarted. It follows the same rules as the log file's compaction: for a rule keeping the latest one record per
// key, the newest content takes the place of the first record; records of a kind with no rule are never touched.
// It only looks at records the lookup tables have already taken in, so nothing is dropped before it has been copied.
func compact(ctx context.Context, c *pgx.Conn, rules map[string]store.Rule) (int, error) {
	var upTo int64
	if err := c.QueryRow(ctx, "SELECT last_seq FROM projector_state WHERE id = 1").Scan(&upTo); err != nil {
		return 0, err
	}
	if upTo == 0 {
		return 0, nil
	}
	kinds := store.SortedKinds(rules)
	rows, err := c.Query(ctx, "SELECT seq, kind, at, payload::text FROM events WHERE kind = ANY($1) AND seq <= $2 ORDER BY seq", kinds, upTo)
	if err != nil {
		return 0, err
	}
	type rec struct {
		seq int64
		at  string
		raw string
	}
	groups := map[string][]rec{} // kind + "\x00" + key
	for rows.Next() {
		var seq int64
		var kind, raw string
		var at time.Time
		if err := rows.Scan(&seq, &kind, &at, &raw); err != nil {
			rows.Close()
			return 0, err
		}
		key := ""
		if r := rules[kind]; r.Key != nil {
			key = r.Key(json.RawMessage(raw))
		}
		g := kind + "\x00" + key
		groups[g] = append(groups[g], rec{seq, at.UTC().Format(time.RFC3339Nano), raw})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	var drop []int64
	type upd struct {
		seq     int64
		at, raw string
	}
	var updates []upd
	names := make([]string, 0, len(groups))
	for g := range groups {
		names = append(names, g)
	}
	sort.Strings(names)
	for _, g := range names {
		recs := groups[g]
		kind := g[:indexByte(g, 0)]
		keep := rules[kind].Latest
		if keep < 1 {
			keep = 1
		}
		if len(recs) <= keep {
			continue
		}
		if keep == 1 {
			newest := recs[len(recs)-1]
			updates = append(updates, upd{recs[0].seq, newest.at, newest.raw})
			for _, r := range recs[1:] {
				drop = append(drop, r.seq)
			}
			continue
		}
		for _, r := range recs[:len(recs)-keep] {
			drop = append(drop, r.seq)
		}
	}
	if len(drop) == 0 {
		return 0, nil
	}
	tx, err := c.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	for _, u := range updates {
		if _, err := tx.Exec(ctx, "UPDATE events SET at = $2::timestamptz, payload = $3::json WHERE seq = $1", u.seq, u.at, u.raw); err != nil {
			return 0, err
		}
	}
	if _, err := tx.Exec(ctx, "DELETE FROM events WHERE seq = ANY($1)", drop); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	return len(drop), nil
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return len(s)
}
