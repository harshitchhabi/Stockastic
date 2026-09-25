package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"

	"stockastic/api/internal/store"
)

func TestCopiesEveryRecordIntoTheDatabase(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "x.wal")
	f, err := store.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 50; i++ {
		if err := f.Append("trade", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	f.Close()
	os.Args = []string{"walimport", "-wal", path, "-db", dsn}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := c.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&n); err != nil || n != 50 {
		t.Fatalf("%d records copied (%v)", n, err)
	}
}
