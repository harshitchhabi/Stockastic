package store_test

import (
	"errors"
	"path/filepath"
	"testing"

	"stockastic/api/internal/store"
)

func TestASecondServerCannotOpenTheSameLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wal")
	first, err := store.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.OpenFile(path); !errors.Is(err, store.ErrLocked) {
		t.Fatalf("a second open of a log in use: %v, want ErrLocked", err)
	}
	// Once the first server stops, the log can be opened again (this is a normal restart).
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	again, err := store.OpenFile(path)
	if err != nil {
		t.Fatalf("reopening after a clean stop: %v", err)
	}
	_ = again.Close()
}
