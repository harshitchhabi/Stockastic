package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"stockastic/api/internal/store"
)

type rec struct {
	kind string
	raw  string
}

func readAll(t *testing.T, path string) []rec {
	t.Helper()
	l, err := store.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var out []rec
	if err := l.Replay(func(k string, raw json.RawMessage) error { out = append(out, rec{k, string(raw)}); return nil }); err != nil {
		t.Fatal(err)
	}
	return out
}

func idKey(raw json.RawMessage) string {
	var v struct{ ID string }
	_ = json.Unmarshal(raw, &v)
	return v.ID
}

var rules = map[string]store.Rule{
	store.KindUser:   {Latest: 1, Key: idKey},
	store.KindClock:  {Latest: 1},
	store.KindPrices: {Latest: 3},
}

func writeSample(t *testing.T, path string) {
	t.Helper()
	l, err := store.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	add := func(k string, v any) {
		if err := l.Append(k, v); err != nil {
			t.Fatal(err)
		}
	}
	add(store.KindUser, map[string]any{"ID": "u1", "Name": "first"})
	add(store.KindUser, map[string]any{"ID": "u2", "Name": "second"})
	add(store.KindTrade, map[string]any{"n": 1}) // a trade by u1
	add(store.KindClock, map[string]any{"v": 1})
	add(store.KindPrices, map[string]any{"tick": 1})
	add(store.KindUser, map[string]any{"ID": "u1", "Name": "renamed"}) // u1 changes AFTER its trade
	add(store.KindPrices, map[string]any{"tick": 2})
	add(store.KindTrade, map[string]any{"n": 2})
	add(store.KindClock, map[string]any{"v": 2})
	add(store.KindPrices, map[string]any{"tick": 3})
	add(store.KindPrices, map[string]any{"tick": 4})
	add(store.KindAudit, map[string]any{"who": "organiser"})
	add(store.KindPrices, map[string]any{"tick": 5})
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCompactionDropsWhatWasReplacedAndKeepsEverythingElse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	writeSample(t, path)
	res, err := store.Compact(path, rules)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != 4 || res.Kept != 9 || res.AfterBytes >= res.BeforeBytes {
		t.Fatalf("result = %+v, want 4 dropped (1 user, 1 clock, 2 old price records), 9 kept, and a smaller file", res)
	}
	got := readAll(t, path)
	kinds := ""
	for _, r := range got {
		kinds += r.kind[:1]
	}
	// The renamed user stays at the position of its FIRST record, ahead of its trade; the newest clock and
	// the newest three price records survive; both trades and the audit entry are untouched.
	if got[0].kind != store.KindUser || got[0].raw != `{"ID":"u1","Name":"renamed"}` {
		t.Fatalf("first record = %+v: an account must still come before its trades, with its newest content", got[0])
	}
	want := []struct{ kind, raw string }{
		{"user", `{"ID":"u1","Name":"renamed"}`}, {"user", `{"ID":"u2","Name":"second"}`}, {"trade", `{"n":1}`},
		{"trade", `{"n":2}`}, {"clock", `{"v":2}`}, {"prices", `{"tick":3}`}, {"prices", `{"tick":4}`},
		{"audit", `{"who":"organiser"}`}, {"prices", `{"tick":5}`},
	}
	if len(got) != len(want) {
		t.Fatalf("%d records survived (%s), want %d", len(got), kinds, len(want))
	}
	trades := 0
	for _, r := range got {
		if r.kind == "trade" {
			trades++
		}
	}
	if trades != 2 {
		t.Fatalf("%d trades survived, want both", trades)
	}
	seen := map[string]bool{}
	for _, r := range got {
		seen[r.kind+r.raw] = true
	}
	for _, w := range want {
		if !seen[w.kind+w.raw] {
			t.Errorf("missing %s %s", w.kind, w.raw)
		}
	}
}

func TestCompactingTwiceChangesNothingTheSecondTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	writeSample(t, path)
	if _, err := store.Compact(path, rules); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	res, err := store.Compact(path, rules)
	if err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if res.Dropped != 0 || string(before) != string(after) {
		t.Fatalf("a second compaction changed the log: %+v", res)
	}
}

func TestRestartingManyTimesKeepsTheLogBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	var sizes []int64
	for run := 0; run < 6; run++ {
		if _, err := store.Compact(path, rules); err != nil { // what the server does at every start
			t.Fatal(err)
		}
		l, err := store.OpenFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 500; i++ { // a session's worth of superseded price records
			_ = l.Append(store.KindPrices, map[string]any{"tick": run*500 + i, "pad": "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"})
		}
		_ = l.Append(store.KindUser, map[string]any{"ID": "u1", "run": run})
		_ = l.Close()
		st, _ := os.Stat(path)
		sizes = append(sizes, st.Size())
	}
	// Without compaction the file would grow six-fold; with it, it settles.
	if sizes[5] > sizes[1]*2 {
		t.Fatalf("the log kept growing across restarts: %v", sizes)
	}
	if n := len(readAll(t, path)); n > 500+10 {
		t.Fatalf("%d records after many restarts", n)
	}
}

func TestATornTailAndAStaleTempFileAreCleanedUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	writeSample(t, path)
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`{"k":"trade","at":"2026-01-01T00:00:00Z","v":{"n":9`)
	_ = f.Close()
	_ = os.WriteFile(path+".compact.tmp", []byte("junk from a crash"), 0o644)

	res, err := store.Compact(path, rules)
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped == 0 {
		t.Fatalf("nothing was rewritten: %+v", res)
	}
	if _, err := os.Stat(path + ".compact.tmp"); !os.IsNotExist(err) {
		t.Fatal("the temporary file was left behind")
	}
	for _, r := range readAll(t, path) {
		if r.raw == `{"n":9` {
			t.Fatal("the torn record survived")
		}
	}
}

func TestCompactionRefusesWhileAServerHasTheLogOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	l, err := store.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_ = l.Append(store.KindClock, map[string]any{"v": 1})
	if _, err := store.Compact(path, rules); err == nil {
		t.Fatal("compacted a log a running server was writing to")
	}
	if _, err := store.Compact(filepath.Join(t.TempDir(), "missing"), rules); err != nil {
		t.Fatalf("a missing log is not an error: %v", err)
	}
}

func TestTheDiskGuardRefusesWritesWhenSpaceIsLow(t *testing.T) {
	dir := t.TempDir()
	free, err := store.FreeBytes(dir)
	if err != nil || free == 0 {
		t.Fatalf("FreeBytes = %d, %v", free, err)
	}
	if err := (&store.DiskGuard{Dir: dir, MinFree: 1}).Check(); err != nil {
		t.Fatalf("plenty of space, refused: %v", err)
	}
	if err := (&store.DiskGuard{Dir: dir, MinFree: free + (1 << 40)}).Check(); err != store.ErrDiskLow {
		t.Fatalf("a nearly full disk: %v, want ErrDiskLow", err)
	}
	if err := (&store.DiskGuard{Dir: dir}).Check(); err != nil {
		t.Fatal("a guard with no minimum must not block")
	}
	var none *store.DiskGuard
	if err := none.Check(); err != nil {
		t.Fatal("a missing guard must not block")
	}
}
