package pgstore

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"stockastic/api/internal/store"
)

// History answers questions about the past from the lookup tables.
type History struct{ p *Projector }

// History returns the reader for this projector's tables.
func (p *Projector) History() *History { return &History{p} }

var _ store.History = (*History)(nil)

func (h *History) epoch(ctx context.Context) (int, error) {
	var e int
	err := h.p.pool.QueryRow(ctx, "SELECT epoch FROM projector_state WHERE id = 1").Scan(&e)
	return e, err
}

func (h *History) WalletHistory(ctx context.Context, account string, max int) ([]store.WalletPoint, error) {
	if max < 2 {
		max = 2
	}
	e, err := h.epoch(ctx)
	if err != nil {
		return nil, err
	}
	// Evenly thin the series to at most max points, always keeping the first and last.
	rows, err := h.p.pool.Query(ctx, `
		WITH w AS (SELECT at, cash_paise, value_paise, row_number() OVER (ORDER BY at) AS n, count(*) OVER () AS total
		           FROM wallet_history WHERE epoch = $1 AND account_id = $2)
		SELECT at, cash_paise, value_paise FROM w
		WHERE total <= $3 OR n = 1 OR n = total OR (n - 1) % GREATEST(total / $3, 1) = 0 ORDER BY at`, e, account, max)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.WalletPoint{}
	for rows.Next() {
		var at time.Time
		var p store.WalletPoint
		if err := rows.Scan(&at, &p.Cash, &p.Value); err != nil {
			return nil, err
		}
		p.At = at.UnixMilli()
		out = append(out, p)
	}
	return out, rows.Err()
}

func (h *History) Activity(ctx context.Context, account string, limit int) ([]store.ActivityRow, error) {
	rows, err := h.p.pool.Query(ctx, `SELECT at, type, coalesce(ip,''), coalesce(user_agent,''), coalesce(detail,'')
		FROM activity WHERE account_id = $1 ORDER BY at DESC, seq DESC LIMIT $2`, account, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.ActivityRow{}
	for rows.Next() {
		var at time.Time
		var r store.ActivityRow
		if err := rows.Scan(&at, &r.Type, &r.IP, &r.UserAgent, &r.Detail); err != nil {
			return nil, err
		}
		r.At = at.UnixMilli()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (h *History) Ledger(ctx context.Context, account string, limit int) ([]store.LedgerRow, error) {
	e, err := h.epoch(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := h.p.pool.Query(ctx, `SELECT at, type, coalesce(symbol,''), shares_delta, cash_delta_paise
		FROM ledger_entries WHERE epoch = $1 AND account_id = $2 ORDER BY at DESC, seq DESC LIMIT $3`, e, account, clampLimit(limit))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.LedgerRow{}
	for rows.Next() {
		var at time.Time
		var r store.LedgerRow
		if err := rows.Scan(&at, &r.Type, &r.Symbol, &r.SharesDelta, &r.CashDelta); err != nil {
			return nil, err
		}
		r.At = at.UnixMilli()
		out = append(out, r)
	}
	return out, rows.Err()
}

func (h *History) SharedAddresses(ctx context.Context) ([]store.SharedAddress, error) {
	rows, err := h.p.pool.Query(ctx, `SELECT ip, account_ids FROM v_shared_addresses ORDER BY accounts DESC, ip LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []store.SharedAddress{}
	for rows.Next() {
		var s store.SharedAddress
		if err := rows.Scan(&s.IP, &s.Accounts); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (h *History) Behind(ctx context.Context) (int64, error) {
	var n int64
	err := h.p.pool.QueryRow(ctx, `SELECT coalesce((SELECT max(seq) FROM events), 0) - last_seq FROM projector_state WHERE id = 1`).Scan(&n)
	if err == pgx.ErrNoRows {
		return 0, nil
	}
	return n, err
}

func clampLimit(n int) int {
	if n < 1 {
		return 100
	}
	if n > 1000 {
		return 1000
	}
	return n
}
