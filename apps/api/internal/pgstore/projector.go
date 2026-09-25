package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stockastic/api/internal/store"
)

// Projector keeps ordinary tables (accounts, trades, ledger entries, wallet history, activity, prices, funds, news,
// disputes, audit) up to date from the durable record. It only ever reads the record and writes its own tables:
// nothing the event depends on waits for it, so if it is slow or the database is busy the event is not affected, and it
// catches up afterwards. Everything in its tables can be rebuilt from the record (Rebuild).
type Projector struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	stop chan struct{}
	wg   sync.WaitGroup
}

// NewProjector connects the projector. Call Start to begin.
func NewProjector(ctx context.Context, dsn string, log *slog.Logger) (*Projector, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["application_name"] = "stockastic-projector"
	cfg.ConnConfig.RuntimeParams["statement_timeout"] = "30000"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if log == nil {
		log = slog.Default()
	}
	return &Projector{pool: pool, log: log, stop: make(chan struct{})}, nil
}

// Start runs the projector in the background.
func (p *Projector) Start() {
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		for {
			n, err := p.RunOnce(context.Background())
			wait := 500 * time.Millisecond
			if err != nil {
				p.log.Warn("lookup tables: could not take in new records, will retry", "err", err)
				wait = 3 * time.Second
			} else if n == 500 {
				wait = 0 // a full batch: there is probably more
			}
			select {
			case <-p.stop:
				return
			case <-time.After(wait):
			}
		}
	}()
}

// Stop ends the background work and closes the connections.
func (p *Projector) Stop() {
	close(p.stop)
	p.wg.Wait()
	p.pool.Close()
}

func dataErr(err error) bool { return dataError(err) }

// RunOnce takes in up to 500 new records. It returns how many.
func (p *Projector) RunOnce(ctx context.Context) (int, error) {
	n, err := p.batch(ctx, 500)
	if err != nil && dataErr(err) {
		// A record the tables cannot hold must not stall the rest: go one at a time and skip the bad one.
		return p.batch(ctx, 1)
	}
	return n, err
}

type event struct {
	seq     int64
	kind    string
	at      time.Time
	payload string
}

func (p *Projector) batch(ctx context.Context, limit int) (int, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var last int64
	var epoch int
	if err := tx.QueryRow(ctx, "SELECT last_seq, epoch FROM projector_state WHERE id = 1 FOR UPDATE").Scan(&last, &epoch); err != nil {
		return 0, err
	}
	rows, err := tx.Query(ctx, "SELECT seq, kind, at, payload::text FROM events WHERE seq > $1 ORDER BY seq LIMIT $2", last, limit)
	if err != nil {
		return 0, err
	}
	var evs []event
	for rows.Next() {
		var e event
		if err := rows.Scan(&e.seq, &e.kind, &e.at, &e.payload); err != nil {
			rows.Close()
			return 0, err
		}
		evs = append(evs, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, e := range evs {
		if limit == 1 {
			// one record on its own: a failure is logged and the record skipped
			if _, err := tx.Exec(ctx, "SAVEPOINT rec"); err != nil {
				return 0, err
			}
			if err := apply(ctx, tx, &epoch, e); err != nil {
				if !dataErr(err) {
					return 0, err
				}
				p.log.Warn("lookup tables: skipped a record they cannot hold", "seq", e.seq, "kind", e.kind, "err", err)
				if _, err := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT rec"); err != nil {
					return 0, err
				}
			}
			last = e.seq
			continue
		}
		if err := apply(ctx, tx, &epoch, e); err != nil {
			return 0, err
		}
		last = e.seq
	}
	if len(evs) > 0 {
		if _, err := tx.Exec(ctx, "UPDATE projector_state SET last_seq = $1, epoch = $2 WHERE id = 1", last, epoch); err != nil {
			return 0, err
		}
	}
	return len(evs), tx.Commit(ctx)
}

// Rebuild empties every lookup table and takes in the whole record again. It exists to prove the tables are derived
// data (and to repair them if anyone ever edits them by hand); the platform does not depend on it.
func (p *Projector) Rebuild(ctx context.Context) error {
	if _, err := p.pool.Exec(ctx, `TRUNCATE accounts, trades, ledger_entries, wallet_history, activity, price_ticks, funds, fund_flows, audit, news_releases, disputes, freezes;
		UPDATE projector_state SET last_seq = 0, epoch = 1 WHERE id = 1`); err != nil {
		return err
	}
	for {
		n, err := p.RunOnce(ctx)
		if err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
	}
}

// ---- turning one record into rows ----

// clean removes NUL characters, which text columns cannot hold.
func clean(s string) string { return strings.ReplaceAll(s, "\x00", "") }

func nz(t time.Time) any {
	if t.IsZero() || t.Year() < 1970 {
		return nil
	}
	return t
}

func ms(v int64) time.Time { return time.UnixMilli(v).UTC() }

func apply(ctx context.Context, tx pgx.Tx, epoch *int, e event) error {
	raw := json.RawMessage(e.payload)
	switch e.kind {
	case store.KindReset:
		*epoch++
	case "user":
		var u struct {
			ID, Email, DisplayName, Role, Status string
			IsAdmin, Locked                      bool
			Warnings, SessionVersion             int
			CreatedAt                            time.Time
		}
		if err := json.Unmarshal(raw, &u); err != nil {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO accounts (id, email, display_name, role, is_admin, status, warnings, locked, session_version, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (id) DO UPDATE SET email=EXCLUDED.email, display_name=EXCLUDED.display_name, role=EXCLUDED.role, is_admin=EXCLUDED.is_admin,
			  status=EXCLUDED.status, warnings=EXCLUDED.warnings, locked=EXCLUDED.locked, session_version=EXCLUDED.session_version, updated_at=EXCLUDED.updated_at`,
			u.ID, clean(u.Email), clean(u.DisplayName), u.Role, u.IsAdmin, u.Status, u.Warnings, u.Locked, u.SessionVersion, nz(u.CreatedAt), e.at)
		return err
	case store.KindTrade:
		var t struct {
			ID, ClientTradeID, AccountID, Symbol, Stage string
			Side                                        int
			Qty, Price                                  int64
			At                                          time.Time
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil
		}
		side, sign := "buy", int64(1)
		if t.Side == 2 {
			side, sign = "sell", -1
		}
		value := t.Qty * t.Price
		if _, err := tx.Exec(ctx, `INSERT INTO trades (seq, epoch, trade_id, client_trade_id, account_id, symbol, side, qty, price_paise, value_paise, stage, at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) ON CONFLICT DO NOTHING`,
			e.seq, *epoch, t.ID, clean(t.ClientTradeID), t.AccountID, t.Symbol, side, t.Qty, t.Price, value, t.Stage, t.At); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO ledger_entries (seq, line, epoch, account_id, at, type, symbol, shares_delta, cash_delta_paise, ref)
			VALUES ($1,0,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`,
			e.seq, *epoch, t.AccountID, t.At, "trade_"+side, t.Symbol, sign*t.Qty, -sign*value, t.ID)
		return err
	case store.KindGrant:
		var g struct {
			AccountID, Symbol string
			Qty               int64
		}
		if err := json.Unmarshal(raw, &g); err != nil {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO ledger_entries (seq, line, epoch, account_id, at, type, symbol, shares_delta, cash_delta_paise)
			VALUES ($1,0,$2,$3,$4,'grant',$5,$6,0) ON CONFLICT DO NOTHING`, e.seq, *epoch, g.AccountID, e.at, g.Symbol, g.Qty)
		return err
	case store.KindCash:
		var c struct {
			AccountID string
			Delta     int64
		}
		if err := json.Unmarshal(raw, &c); err != nil {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO ledger_entries (seq, line, epoch, account_id, at, type, cash_delta_paise)
			VALUES ($1,0,$2,$3,$4,'cash_adjust',$5) ON CONFLICT DO NOTHING`, e.seq, *epoch, c.AccountID, e.at, c.Delta)
		return err
	case store.KindFund:
		return applyFund(ctx, tx, *epoch, e, raw)
	case store.KindPrices:
		var s struct {
			Tick   int
			Prices map[string]int64
			At     time.Time
		}
		if err := json.Unmarshal(raw, &s); err != nil || s.Tick == 0 || len(s.Prices) == 0 {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO price_ticks (epoch, tick, symbol, price_paise, at)
			SELECT $1, $2, key, value::bigint, $3 FROM jsonb_each_text($4::jsonb) ON CONFLICT DO NOTHING`, *epoch, s.Tick, nzTime(s.At, e.at), e.payloadPrices(raw))
		return err
	case store.KindAudit:
		var a struct {
			ID, Actor, Action, Target, Reason string
			At                                time.Time
			OK                                bool
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO audit (seq, epoch, id, at, actor, action, target, reason, ok)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT DO NOTHING`, e.seq, *epoch, a.ID, a.At, clean(a.Actor), clean(a.Action), clean(a.Target), clean(a.Reason), a.OK)
		return err
	case store.KindNews:
		var r struct {
			Item struct {
				ID, Kind, Headline string
				CreatedAt          time.Time
			}
			FundManagerAt, PublicAt time.Time
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO news_releases (epoch, id, kind, headline, created_at, fund_manager_at, public_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)
			ON CONFLICT (epoch, id) DO UPDATE SET fund_manager_at = EXCLUDED.fund_manager_at, public_at = EXCLUDED.public_at`,
			*epoch, r.Item.ID, r.Item.Kind, clean(r.Item.Headline), nz(r.Item.CreatedAt), nz(r.FundManagerAt), nz(r.PublicAt))
		return err
	case store.KindTicket:
		var t struct {
			Ticket struct {
				ID, Account, Category, Summary string
				RaisedAt                       time.Time
			}
			Status, Resolution string
		}
		if err := json.Unmarshal(raw, &t); err != nil {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO disputes (epoch, id, account_id, category, summary, raised_at, status, resolution)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (epoch, id) DO UPDATE SET status = EXCLUDED.status, resolution = EXCLUDED.resolution`,
			*epoch, t.Ticket.ID, t.Ticket.Account, t.Ticket.Category, clean(t.Ticket.Summary), nz(t.Ticket.RaisedAt), t.Status, clean(t.Resolution))
		return err
	case store.KindSnapshot:
		var s struct {
			Name   string
			At     time.Time
			Values map[string]int64
			Peaks  map[string]int64
			Trades map[string]int
		}
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil
		}
		for acct, v := range s.Values {
			if _, err := tx.Exec(ctx, `INSERT INTO freezes (epoch, name, account_id, value_paise, peak_paise, trades, at)
				VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, *epoch, s.Name, acct, v, s.Peaks[acct], s.Trades[acct], s.At); err != nil {
				return err
			}
		}
	case store.KindActivity:
		var a store.Activity
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil
		}
		_, err := tx.Exec(ctx, `INSERT INTO activity (seq, epoch, account_id, type, ip, user_agent, detail, at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING`, e.seq, *epoch, a.Account, clean(a.Type), clean(a.IP), clean(a.UserAgent), clean(a.Detail), ms(a.At))
		return err
	case store.KindWallets:
		var w store.Wallets
		if err := json.Unmarshal(raw, &w); err != nil {
			return nil
		}
		at := ms(w.At)
		for acct, cv := range w.W {
			if _, err := tx.Exec(ctx, `INSERT INTO wallet_history (epoch, account_id, at, cash_paise, value_paise)
				VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, *epoch, acct, at, cv[0], cv[1]); err != nil {
				return err
			}
		}
	}
	return nil
}

func nzTime(t, fallback time.Time) time.Time {
	if t.IsZero() {
		return fallback
	}
	return t
}

// payloadPrices returns just the price map of a simulation record as JSON.
func (e event) payloadPrices(raw json.RawMessage) string {
	var s struct{ Prices json.RawMessage }
	_ = json.Unmarshal(raw, &s)
	return string(s.Prices)
}

func applyFund(ctx context.Context, tx pgx.Tx, epoch int, e event, raw json.RawMessage) error {
	var f struct {
		Op    string
		At    int64
		Funds []struct {
			ID      string
			Number  int
			Members [2]string
			Trader  string
		}
		FundID   string
		Profile  *struct{ Name, Philosophy, Risk, Strategy string }
		Window   int
		Investor string
		Amount   int64
		Units    float64
		NAV      float64
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil
	}
	switch f.Op {
	case "formed":
		for _, fd := range f.Funds {
			trader := fd.Trader
			if trader == "" {
				trader = fd.Members[0]
			}
			if _, err := tx.Exec(ctx, `INSERT INTO funds (epoch, fund_id, number, members, trader) VALUES ($1,$2,$3,$4,$5)
				ON CONFLICT (epoch, fund_id) DO NOTHING`, epoch, fd.ID, fd.Number, []string{fd.Members[0], fd.Members[1]}, trader); err != nil {
				return err
			}
		}
	case "profile":
		if f.Profile == nil {
			return nil
		}
		_, err := tx.Exec(ctx, `UPDATE funds SET name=$3, philosophy=$4, risk=$5, strategy=$6 WHERE epoch=$1 AND fund_id=$2`,
			epoch, f.FundID, clean(f.Profile.Name), clean(f.Profile.Philosophy), f.Profile.Risk, clean(f.Profile.Strategy))
		return err
	case "trader":
		_, err := tx.Exec(ctx, `UPDATE funds SET trader=$3 WHERE epoch=$1 AND fund_id=$2`, epoch, f.FundID, f.Investor)
		return err
	case "dissolve":
		_, err := tx.Exec(ctx, `DELETE FROM funds WHERE epoch=$1`, epoch)
		return err
	case "alloc", "redeem":
		sign, typ := int64(-1), "fund_invest"
		if f.Op == "redeem" {
			sign, typ = 1, "fund_redeem"
		}
		at := ms(f.At)
		if _, err := tx.Exec(ctx, `INSERT INTO fund_flows (seq, epoch, fund_id, investor, kind, window_no, amount_paise, units, nav, at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING`, e.seq, epoch, f.FundID, f.Investor, f.Op, f.Window, f.Amount, f.Units, f.NAV, at); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO ledger_entries (seq, line, epoch, account_id, at, type, cash_delta_paise, ref)
			VALUES ($1,0,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`, e.seq, epoch, f.Investor, at, typ, sign*f.Amount, f.FundID)
		return err
	}
	return nil
}

var _ = errors.New
