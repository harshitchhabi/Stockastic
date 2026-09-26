package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"stockastic/api/internal/pgstore"
)

// A database that is up on the first try is not slowed down at all.
func TestOpeningTheDatabaseSucceedsAtOnceWhenItIsUp(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := time.Now()
	l, err := openDatabaseRetrying(context.Background(), log, pgstore.Options{DSN: dsn}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if time.Since(start) > 2*time.Second {
		t.Fatalf("took %s to open a database that was already up", time.Since(start))
	}
}

// A database that never comes back must eventually be reported as an error, not retried forever, and the caller
// must see that it really did try more than once (this is what stands between an ordinary bounce and an event
// that silently sits dead).
func TestOpeningTheDatabaseGivesUpAfterItsBudgetHavingTriedMoreThanOnce(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := time.Now()
	// Nothing listens on port 1, so every attempt fails almost immediately (connection refused), which lets this
	// test exercise several retries within a short budget instead of needing a real 3-minute wait.
	_, err := openDatabaseRetrying(context.Background(), log, pgstore.Options{DSN: "postgres://nobody@127.0.0.1:1/nope?connect_timeout=1"}, 2*time.Second)
	if err == nil {
		t.Fatal("expected an error once the retry budget ran out")
	}
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("gave up after only %s, before its budget of 2s; it did not really retry", elapsed)
	}
}
