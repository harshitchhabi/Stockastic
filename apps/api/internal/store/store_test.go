package store_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"stockastic/api/internal/store"
)

func replayAll(t *testing.T, l store.Log) (kinds []string, vals []string) {
	t.Helper()
	err := l.Replay(func(k string, raw json.RawMessage) error {
		kinds = append(kinds, k)
		vals = append(vals, string(raw))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return
}

func TestFileLogKeepsEverythingAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "wal")
	l, err := store.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := l.Append(store.KindUser, map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	_ = l.Close()

	l2, err := store.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer l2.Close()
	kinds, vals := replayAll(t, l2)
	if len(kinds) != 100 || vals[0] != `{"n":0}` || vals[99] != `{"n":99}` {
		t.Fatalf("replayed %d records, first %q last %q", len(kinds), vals[0], vals[len(vals)-1])
	}
	if l2.Torn != 0 {
		t.Fatalf("Torn = %d on a clean file", l2.Torn)
	}
}

func TestATornFinalRecordFromACrashIsDiscardedAndAppendingContinues(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	l, _ := store.OpenFile(path)
	for i := 0; i < 5; i++ {
		_ = l.Append(store.KindTrade, map[string]int{"n": i})
	}
	_ = l.Close()

	// Simulate power loss halfway through writing the next record.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	_, _ = f.WriteString(`{"k":"batch","at":"2026-01-01T00:00:00Z","v":{"n":5`)
	_ = f.Close()

	l2, err := store.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if l2.Torn == 0 {
		t.Fatal("the torn record was not detected")
	}
	if kinds, _ := replayAll(t, l2); len(kinds) != 5 {
		t.Fatalf("replayed %d records, want the 5 acknowledged ones", len(kinds))
	}
	// New records land cleanly after the good ones, not glued onto the torn fragment.
	if err := l2.Append(store.KindTrade, map[string]int{"n": 6}); err != nil {
		t.Fatal(err)
	}
	_ = l2.Close()

	l3, _ := store.OpenFile(path)
	defer l3.Close()
	kinds, vals := replayAll(t, l3)
	if len(kinds) != 6 || vals[5] != `{"n":6}` {
		t.Fatalf("after recovery: %d records, last %q", len(kinds), vals[len(vals)-1])
	}
	if l3.Torn != 0 {
		t.Fatalf("still torn after recovery: %d", l3.Torn)
	}
}

func TestAppendAfterCloseFailsInsteadOfSilentlyLosingData(t *testing.T) {
	l, _ := store.OpenFile(filepath.Join(t.TempDir(), "wal"))
	_ = l.Close()
	if err := l.Append(store.KindUser, 1); err == nil {
		t.Fatal("an append to a closed log reported success")
	}
}

func TestConcurrentAppendsAreAllStoredWhole(t *testing.T) {
	l, _ := store.OpenFile(filepath.Join(t.TempDir(), "wal"))
	defer l.Close()
	done := make(chan struct{})
	const writers, each = 8, 50
	for w := 0; w < writers; w++ {
		go func(w int) {
			for i := 0; i < each; i++ {
				_ = l.Append(store.KindAudit, map[string]int{"w": w, "i": i})
			}
			done <- struct{}{}
		}(w)
	}
	for w := 0; w < writers; w++ {
		<-done
	}
	if kinds, _ := replayAll(t, l); len(kinds) != writers*each {
		t.Fatalf("stored %d records, want %d", len(kinds), writers*each)
	}
}
