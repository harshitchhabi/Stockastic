package httpapi_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"stockastic/api/internal/app"
	"stockastic/api/internal/pgstore"
	"stockastic/api/internal/store"
)

// The same chaos test as above, but with PostgreSQL as the durable record. Needs TEST_DATABASE_URL pointing at an
// empty database made for testing (its public schema is wiped).
func TestChaosThenRestartIsIdenticalOnPostgres(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)
	open := func() store.Log {
		l, err := pgstore.Open(ctx, pgstore.Options{DSN: dsn, Rules: app.CompactRules()})
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	chaosThenRestart(t, open(), open)
}

// Sign-ups and sign-ins are recorded with the address and browser, reach the lookup tables, and are shown to the
// organiser. A wrong password is recorded without an account.
func TestSignInsAreRecordedAndShownToTheOrganiser(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Exec(ctx, "DROP SCHEMA public CASCADE; CREATE SCHEMA public"); err != nil {
		t.Fatal(err)
	}
	c.Close(ctx)
	l, err := pgstore.Open(ctx, pgstore.Options{DSN: dsn, Rules: app.CompactRules()})
	if err != nil {
		t.Fatal(err)
	}
	proj, err := pgstore.NewProjector(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer proj.Stop()
	e := newEnvWith(t, l, rb(t, 0), testScenario(), func(c *app.Config) {
		c.Track, c.History, c.Health = true, proj.History(), l.Healthy
	})
	tok, id := e.signup("Tracked")
	_ = tok
	e.call("POST", "/api/auth/login", "", map[string]any{"email": "tracked@test.local", "password": "wrong-password-1"})
	e.call("POST", "/api/auth/login", "", map[string]any{"email": "tracked@test.local", "password": "password-123"})
	adm := e.admin()
	var acts []any
	for i := 0; i < 40; i++ {
		time.Sleep(250 * time.Millisecond)
		if _, err := proj.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		r := e.call("GET", "/api/admin/accounts/"+id+"/history", adm, nil)
		if r.Status != 200 || r.Body["available"] != true {
			t.Fatalf("history: %d %s", r.Status, r.Raw)
		}
		acts, _ = r.Body["activity"].([]any)
		if len(acts) >= 2 {
			break
		}
	}
	if len(acts) < 2 {
		t.Fatalf("expected the sign-up and sign-in to be recorded, got %v", acts)
	}
	if r := e.call("GET", "/api/admin/shared-addresses", adm, nil); r.Status != 200 {
		t.Fatalf("shared addresses: %d", r.Status)
	}
	if r := e.call("GET", "/readyz", "", nil); r.Status != 200 {
		t.Fatalf("readyz %d", r.Status)
	}
	if r := e.call("GET", "/api/admin/accounts/"+id+"/history", tok, nil); r.Status != 403 {
		t.Fatalf("a team read the history: %d", r.Status)
	}
}
