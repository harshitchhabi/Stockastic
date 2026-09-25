package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"stockastic/api/internal/store"
)

// These tests need a real PostgreSQL. Point TEST_DATABASE_URL at an EMPTY database made for testing (every test wipes
// its public schema), for example postgres://user@127.0.0.1:5433/stockastic_test. Without it they are skipped.
func testDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	return dsn
}

func open(t *testing.T, dsn string, mod ...func(*Options)) *Log {
	t.Helper()
	o := Options{DSN: dsn}
	for _, m := range mod {
		m(&o)
	}
	l, err := Open(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l
}

func replayAll(t *testing.T, l *Log) (kinds []string, raws []json.RawMessage) {
	t.Helper()
	if err := l.Replay(func(k string, r json.RawMessage) error {
		kinds, raws = append(kinds, k), append(raws, r)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestRecordsComeBackInOrderAndIntact(t *testing.T) {
	dsn := testDSN(t)
	l := open(t, dsn)
	weird := "quotes \" back\\slash tab\t newline\n unicode ₹ \U0001f600 and a NUL \x00 too"
	for i := 0; i < 5; i++ {
		if err := l.Append("trade", map[string]any{"n": i, "note": weird}); err != nil {
			t.Fatal(err)
		}
	}
	kinds, raws := replayAll(t, l)
	if len(kinds) != 5 {
		t.Fatalf("%d records", len(kinds))
	}
	for i, r := range raws {
		var m struct {
			N    int
			Note string
		}
		if err := json.Unmarshal(r, &m); err != nil || m.N != i || m.Note != weird {
			t.Fatalf("record %d came back as %s (%v)", i, r, err)
		}
	}
}

func TestManyWritersAtOnceAreAllStoredInEachOnesOrder(t *testing.T) {
	dsn := testDSN(t)
	l := open(t, dsn)
	const writers, each = 200, 10
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				if err := l.Append("trade", map[string]int{"w": w, "i": i}); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	_, raws := replayAll(t, l)
	if len(raws) != writers*each {
		t.Fatalf("%d records stored, want %d", len(raws), writers*each)
	}
	last := map[int]int{}
	for _, r := range raws {
		var m struct{ W, I int }
		_ = json.Unmarshal(r, &m)
		if prev, ok := last[m.W]; ok && m.I != prev+1 {
			t.Fatalf("writer %d: record %d followed %d", m.W, m.I, prev)
		}
		last[m.W] = m.I
	}
}

func TestOnlyOneServerCanWrite(t *testing.T) {
	dsn := testDSN(t)
	l := open(t, dsn)
	if _, err := Open(context.Background(), Options{DSN: dsn}); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("a second server opening the same database got %v, want ErrLocked", err)
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	l2, err := Open(context.Background(), Options{DSN: dsn})
	if err != nil {
		t.Fatalf("opening after the first closed: %v", err)
	}
	l2.Close()
}

// killBackend ends the database session the writer is using, as a network failure or a database restart would.
func killBackend(t *testing.T, dsn string) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	if _, err := c.Exec(ctx, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE application_name = 'stockastic' AND pid <> pg_backend_pid() AND datname = current_database()"); err != nil {
		t.Fatal(err)
	}
}

// The core promise: if the connection dies while records are being written, every record the caller was told was
// saved is stored exactly once, and nothing is stored twice.
func TestConnectionsDyingMidWriteLoseAndDuplicateNothing(t *testing.T) {
	dsn := testDSN(t)
	l := open(t, dsn, func(o *Options) { o.RetryFor = 15 * time.Second })
	stop := make(chan struct{})
	var acked atomic.Int64
	var mu sync.Mutex
	ackedIDs := map[int]bool{}
	var wg sync.WaitGroup
	for w := 0; w < 20; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				id := w*1_000_000 + i
				if err := l.Append("trade", map[string]int{"id": id}); err != nil {
					t.Errorf("an append failed instead of being retried: %v", err)
					return
				}
				mu.Lock()
				ackedIDs[id] = true
				mu.Unlock()
				acked.Add(1)
			}
		}(w)
	}
	for k := 0; k < 6; k++ {
		time.Sleep(250 * time.Millisecond)
		killBackend(t, dsn)
	}
	time.Sleep(500 * time.Millisecond)
	close(stop)
	wg.Wait()
	if acked.Load() < 100 {
		t.Fatalf("only %d records were written: the test did not exercise the writer", acked.Load())
	}
	_, raws := replayAll(t, l)
	seen := map[int]int{}
	for _, r := range raws {
		var m struct{ ID int }
		_ = json.Unmarshal(r, &m)
		seen[m.ID]++
	}
	for id := range ackedIDs {
		if seen[id] != 1 {
			t.Fatalf("record %d was acknowledged but is stored %d times", id, seen[id])
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Fatalf("record %d is stored %d times", id, n)
		}
	}
	t.Logf("%d records acknowledged across 6 killed connections; every one stored exactly once", acked.Load())
}

// If another server takes over the write lock, this one must stop writing at once and never write again.
func TestALostWriteLockStopsTheWriter(t *testing.T) {
	dsn := testDSN(t)
	var broken atomic.Bool
	l := open(t, dsn, func(o *Options) { o.RetryFor = 2 * time.Second; o.OnBroken = func(error) { broken.Store(true) } })
	if err := l.Append("trade", map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	other, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close(ctx)
	killBackend(t, dsn)
	// The "other server" grabs the lock before the writer can reconnect.
	got := false
	for i := 0; i < 400 && !got; i++ {
		_ = other.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", lockKey).Scan(&got)
		time.Sleep(5 * time.Millisecond)
	}
	if !got {
		t.Fatal("could not take the lock for the test")
	}
	if err := l.Append("trade", map[string]int{"n": 2}); err == nil {
		t.Fatal("a write was accepted after the write lock was lost")
	}
	if !broken.Load() || l.Healthy() == nil {
		t.Fatal("the log did not report that it had stopped")
	}
	if err := l.Append("trade", map[string]int{"n": 3}); !errors.Is(err, ErrBroken) {
		t.Fatalf("later writes: %v", err)
	}
	var n int
	if err := other.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d records are stored, want only the first (err %v)", n, err)
	}
}

func TestTidyingKeepsTheNewestAndOnlyWhatWasCopied(t *testing.T) {
	dsn := testDSN(t)
	rules := map[string]store.Rule{
		"user":   {Latest: 1, Key: func(raw json.RawMessage) string { var m struct{ ID string }; _ = json.Unmarshal(raw, &m); return m.ID }},
		"prices": {Latest: 2},
	}
	l := open(t, dsn)
	for i := 1; i <= 3; i++ {
		_ = l.Append("user", map[string]any{"ID": "a", "v": i})
		_ = l.Append("user", map[string]any{"ID": "b", "v": i})
		_ = l.Append("prices", map[string]any{"tick": i})
		_ = l.Append("trade", map[string]any{"n": i})
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
	// Nothing has been copied to the lookup tables yet, so nothing may be dropped.
	l = open(t, dsn, func(o *Options) { o.Rules = rules })
	if l.Compacted != 0 {
		t.Fatalf("dropped %d records that had not been copied", l.Compacted)
	}
	l.Close()
	// Once everything is copied, superseded records go, newest content in the first record's place.
	ctx := context.Background()
	c, _ := pgx.Connect(ctx, dsn)
	if _, err := c.Exec(ctx, "UPDATE projector_state SET last_seq = (SELECT max(seq) FROM events)"); err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)
	l = open(t, dsn, func(o *Options) { o.Rules = rules })
	if l.Compacted != 5 { // user a: 2, user b: 2, prices: 1
		t.Fatalf("dropped %d, want 5", l.Compacted)
	}
	kinds, raws := replayAll(t, l)
	want := []string{"user", "user", "trade", "prices", "trade", "prices", "trade"}
	if fmt.Sprint(kinds) != fmt.Sprint(want) {
		t.Fatalf("kinds after tidying = %v, want %v", kinds, want)
	}
	var first struct {
		ID string
		V  int
	}
	_ = json.Unmarshal(raws[0], &first)
	if first.ID != "a" || first.V != 3 {
		t.Fatalf("account a is %+v, want its newest content (v 3) in its first place", first)
	}
	// Trades are never touched.
	n := 0
	for _, k := range kinds {
		if k == "trade" {
			n++
		}
	}
	if n != 3 {
		t.Fatalf("%d trades left, want all 3", n)
	}
}
