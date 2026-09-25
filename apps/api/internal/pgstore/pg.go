// Package pgstore keeps the platform's durable record in PostgreSQL.
//
// It replaces the log file behind the same store.Log interface, so everything above it is unchanged: a record is
// confirmed only once it has been committed to the database, and the whole state is rebuilt by replaying the records
// in order when the server starts.
//
// What it guarantees, and how:
//
//   - Atomic and durable: each batch of records is one INSERT statement (one transaction) with synchronous_commit on.
//     The caller is told "saved" only after PostgreSQL has committed it.
//   - Exactly once: every record carries its own unique id and is inserted with ON CONFLICT DO NOTHING. If the
//     connection drops and nobody knows whether a commit happened, the same batch is sent again on a new connection
//     and cannot be stored twice.
//   - One writer: a session-level advisory lock is taken on the connection that does all the writing. A second
//     server refuses to start; if this server ever loses the lock (its connection died and another server took
//     over) it stops writing at once.
//   - Fail-stop: if the database cannot be reached for long enough, the log refuses every write rather than accept
//     something it cannot keep. A restart replays what really was stored.
//   - Order: records are numbered by a sequence, and replay reads them in that order.
//
// Alongside the records it maintains ordinary tables (accounts, trades, wallet history, logins and more) for
// looking things up. Those are derived from the records and can be rebuilt at any time (see projector.go).
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"stockastic/api/internal/ids"
	"stockastic/api/internal/store"
)

// lockKey identifies this application's write lock in the database.
const lockKey int64 = 0x5354_4f43_4b41_5354 // "STOCKAST"

// ErrBroken is returned for every write after the log has stopped itself.
var ErrBroken = errors.New("pgstore: the database log has stopped accepting writes")

// Options configure Open.
type Options struct {
	// DSN is the connection string, for example postgres://user:pass@host:5432/stockastic?sslmode=require.
	DSN string
	// Rules say which records a newer one replaces (see store.Rule); applied once when the log is opened.
	Rules map[string]store.Rule
	Log   *slog.Logger
	// RetryFor is how long a failing database is retried before the log stops itself (default 20 seconds).
	RetryFor time.Duration
	// OnBroken is called once, from the writer, when the log stops itself.
	OnBroken func(error)
}

type req struct {
	rid     string
	kind    string
	at      time.Time
	payload string
	res     chan error
}

// Log is a store.Log backed by PostgreSQL.
type Log struct {
	opt  Options
	cfg  *pgx.ConnConfig
	log  *slog.Logger
	reqs chan *req
	stop chan struct{}
	wg   sync.WaitGroup

	mu     sync.Mutex
	broken error
	closed bool
	dead   chan struct{} // closed once the writer has finished and nothing more will be answered

	conn *pgx.Conn // used only by the writer goroutine after Open

	// Compacted is how many superseded records were removed when the log was opened.
	Compacted int
}

// Open connects, takes the write lock, prepares the tables, tidies the log and starts the writer.
func Open(ctx context.Context, opt Options) (*Log, error) {
	if opt.Log == nil {
		opt.Log = slog.Default()
	}
	if opt.RetryFor == 0 {
		opt.RetryFor = 20 * time.Second
	}
	cfg, err := pgx.ParseConfig(opt.DSN)
	if err != nil {
		return nil, fmt.Errorf("pgstore: bad DATABASE_URL: %w", err)
	}
	cfg.RuntimeParams["application_name"] = "stockastic"
	cfg.RuntimeParams["synchronous_commit"] = "on"
	cfg.RuntimeParams["statement_timeout"] = "15000"
	cfg.RuntimeParams["idle_in_transaction_session_timeout"] = "15000"
	cfg.ConnectTimeout = 10 * time.Second
	cfg.DialFunc = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 15 * time.Second}).DialContext

	l := &Log{opt: opt, cfg: cfg, log: opt.Log, reqs: make(chan *req, 4096), stop: make(chan struct{}), dead: make(chan struct{})}
	if err := l.connect(ctx); err != nil {
		return nil, err
	}
	if err := migrate(ctx, l.conn); err != nil {
		l.conn.Close(context.Background())
		return nil, fmt.Errorf("pgstore: preparing the tables: %w", err)
	}
	if len(opt.Rules) > 0 {
		n, err := compact(ctx, l.conn, opt.Rules)
		if err != nil {
			l.conn.Close(context.Background())
			return nil, fmt.Errorf("pgstore: tidying the log: %w", err)
		}
		l.Compacted = n
	}
	l.wg.Add(1)
	go l.run()
	return l, nil
}

// connect opens the writer's connection and takes the write lock on it.
func (l *Log) connect(ctx context.Context) error {
	c, err := pgx.ConnectConfig(ctx, l.cfg)
	if err != nil {
		return err
	}
	var got bool
	if err := c.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockKey).Scan(&got); err != nil {
		c.Close(context.Background())
		return err
	}
	if !got {
		c.Close(context.Background())
		return store.ErrLocked
	}
	l.conn = c
	return nil
}

// Append stores one record and returns only when the database has committed it.
func (l *Log) Append(kind string, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("store: encode %s: %w", kind, err)
	}
	if err := l.failure(); err != nil {
		return err
	}
	r := &req{rid: ids.New(), kind: kind, at: time.Now().UTC(), payload: string(raw), res: make(chan error, 1)}
	select {
	case l.reqs <- r:
	case <-l.stop:
		return errors.New("store: log closed")
	}
	select {
	case err := <-r.res:
		return err
	case <-l.dead:
		select {
		case err := <-r.res:
			return err
		default:
			return errors.New("store: log closed")
		}
	}
}

func (l *Log) failure() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return errors.New("store: log closed")
	}
	return l.broken
}

// Healthy reports whether the log is still accepting writes.
func (l *Log) Healthy() error { return l.failure() }

func (l *Log) run() {
	defer l.wg.Done()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()
	for {
		select {
		case r := <-l.reqs:
			batch := []*req{r}
		fill:
			for len(batch) < 256 {
				select {
				case n := <-l.reqs:
					batch = append(batch, n)
				default:
					break fill
				}
			}
			l.flush(batch)
		case <-tick.C:
			l.keepAlive()
		case <-l.stop:
			for {
				select {
				case r := <-l.reqs:
					l.flush([]*req{r})
				default:
					return
				}
			}
		}
	}
}

const insertSQL = `
INSERT INTO events (rid, kind, at, payload)
SELECT rid::uuid, kind, at::timestamptz, payload::json
FROM unnest($1::text[], $2::text[], $3::text[], $4::text[]) WITH ORDINALITY AS t(rid, kind, at, payload, n)
ORDER BY n
ON CONFLICT (rid) DO NOTHING`

func (l *Log) insert(ctx context.Context, batch []*req) error {
	rids, kinds, ats, payloads := make([]string, len(batch)), make([]string, len(batch)), make([]string, len(batch)), make([]string, len(batch))
	for i, r := range batch {
		rids[i], kinds[i], ats[i], payloads[i] = r.rid, r.kind, r.at.Format(time.RFC3339Nano), r.payload
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := l.conn.Exec(cctx, insertSQL, rids, kinds, ats, payloads)
	return err
}

// dataError is true for a problem with the record itself (not the database): retrying it can never help.
func dataError(err error) bool {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return strings.HasPrefix(pe.Code, "22") || strings.HasPrefix(pe.Code, "23")
	}
	return false
}

// flush stores a batch. A record that the database rejects for what it contains is refused on its own, without
// affecting the others; anything else is retried on a fresh connection until it works or the time runs out.
func (l *Log) flush(batch []*req) {
	if err := l.failure(); err != nil {
		for _, r := range batch {
			r.res <- err
		}
		return
	}
	deadline := time.Now().Add(l.opt.RetryFor)
	backoff := 100 * time.Millisecond
	for {
		err := l.insert(context.Background(), batch)
		if err == nil {
			for _, r := range batch {
				r.res <- nil
			}
			return
		}
		if dataError(err) {
			// One bad record must not sink the batch: store them one by one.
			for _, r := range batch {
				e := l.insert(context.Background(), []*req{r})
				r.res <- e
			}
			return
		}
		if time.Now().After(deadline) {
			l.breakWith(fmt.Errorf("%w: %v", ErrBroken, err))
			for _, r := range batch {
				r.res <- l.failure()
			}
			return
		}
		l.log.Warn("database write failed, reconnecting", "err", err)
		time.Sleep(backoff)
		backoff = min(backoff*2, time.Second)
		if e := l.reconnect(deadline); e != nil {
			l.breakWith(e)
			for _, r := range batch {
				r.res <- l.failure()
			}
			return
		}
	}
}

// reconnect replaces the connection and takes the write lock again. If another server holds it now, this one has
// been fenced out and must stop writing for good.
func (l *Log) reconnect(deadline time.Time) error {
	if l.conn != nil {
		l.conn.Close(context.Background())
		l.conn = nil
	}
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := l.connect(ctx)
		cancel()
		if err == nil {
			return nil
		}
		if errors.Is(err, store.ErrLocked) && time.Now().After(deadline) {
			return fmt.Errorf("%w: the write lock was lost to another server", ErrBroken)
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: %v", ErrBroken, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// keepAlive checks the connection while nothing is being written, so a database restart is noticed and repaired
// before the next trade needs it.
func (l *Log) keepAlive() {
	if l.failure() != nil || l.conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := l.conn.Ping(ctx); err != nil {
		l.log.Warn("database connection lost while idle, reconnecting", "err", err)
		if e := l.reconnect(time.Now().Add(l.opt.RetryFor)); e != nil {
			l.breakWith(e)
		}
	}
}

func (l *Log) breakWith(err error) {
	l.mu.Lock()
	first := l.broken == nil
	if first {
		l.broken = err
	}
	l.mu.Unlock()
	if first {
		l.log.Error("the database log stopped itself: no more writes are accepted until the server is restarted", "err", err)
		if l.opt.OnBroken != nil {
			l.opt.OnBroken(err)
		}
	}
}

// Replay calls fn for every stored record in the order they were stored.
func (l *Log) Replay(fn func(kind string, raw json.RawMessage) error) error {
	ctx := context.Background()
	c, err := pgx.ConnectConfig(ctx, l.cfg)
	if err != nil {
		return err
	}
	defer c.Close(ctx)
	rows, err := c.Query(ctx, "SELECT kind, payload::text FROM events ORDER BY seq")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, payload string
		if err := rows.Scan(&kind, &payload); err != nil {
			return err
		}
		if err := fn(kind, json.RawMessage(payload)); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Close finishes what is queued and releases the database.
func (l *Log) Close() error {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return nil
	}
	l.closed = true
	l.mu.Unlock()
	close(l.stop)
	l.wg.Wait()
	close(l.dead)
	if l.conn != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = l.conn.Exec(ctx, "SELECT pg_advisory_unlock($1)", lockKey)
		return l.conn.Close(ctx)
	}
	return nil
}

var _ store.Log = (*Log)(nil)
