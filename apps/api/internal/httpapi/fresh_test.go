package httpapi_test

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"stockastic/api/internal/app"
	"stockastic/api/internal/pgstore"
	"stockastic/api/internal/store"
)

// "Start completely fresh" deletes every team and teammate and signs everyone out; the organisers stay, the same
// people can register again with the same emails, and it holds across a restart. An ordinary restart never resets.
func TestStartCompletelyFresh(t *testing.T) {
	t.Run("memory", func(t *testing.T) { startFresh(t, newEnv(t, store.NewMem(), rb(t, 100)), nil) })
	t.Run("postgres", func(t *testing.T) {
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
		open := func() store.Log {
			l, err := pgstore.Open(ctx, pgstore.Options{DSN: dsn, Rules: app.CompactRules()})
			if err != nil {
				t.Fatal(err)
			}
			return l
		}
		e := newEnv(t, open(), rb(t, 100))
		e.reopen = open
		startFresh(t, e, func() {
			// the lookup tables follow: only the organisers' accounts are left
			proj, err := pgstore.NewProjector(ctx, dsn, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer proj.Stop()
			for i := 0; i < 20; i++ {
				if n, err := proj.RunOnce(ctx); err != nil || n == 0 {
					break
				}
			}
			var teams int
			if err := c.QueryRow(ctx, "SELECT count(*) FROM accounts WHERE NOT is_admin").Scan(&teams); err != nil {
				t.Fatal(err)
			}
			// the two teams registered again after the reset are the only ones
			if teams != 2 {
				t.Fatalf("team rows in the lookup table after starting fresh: %d, want the 2 registered since", teams)
			}
		})
		c.Close(ctx)
	})
}

func startFresh(t *testing.T, e *env, afterwards func()) {
	adm := e.admin()
	lead, _ := e.signup("Comet")
	other, _ := e.signup("Nova")
	code := teamCode(t, e, lead)
	mate := join(e, code, "mate").Body["token"].(string)
	e.openMarket(adm)
	if r := e.call("PUT", "/api/watchlist", lead, map[string]any{"symbols": []string{}}); r.Status != 200 {
		t.Fatalf("watchlist: %d", r.Status)
	}

	// an ordinary restart keeps everyone
	e = e.restart(rb(t, 100))
	adm = e.admin()
	if r := e.call("GET", "/api/auth/me", mate, nil); r.Status != 200 {
		t.Fatalf("a teammate after an ordinary restart: %d %s", r.Status, r.Raw)
	}

	if r := e.call("POST", "/api/admin/event/start-fresh", lead, map[string]any{}); r.Status != 403 {
		t.Fatalf("a team started the event fresh: %d", r.Status)
	}
	if r := e.call("POST", "/api/admin/event/start-fresh", adm, map[string]any{}); r.Status != 200 {
		t.Fatalf("start fresh: %d %s", r.Status, r.Raw)
	}
	check := func(e *env, when string) {
		for name, tok := range map[string]string{"leader": lead, "teammate": mate, "other team": other} {
			if r := e.call("GET", "/api/auth/me", tok, nil); r.Status != 401 {
				t.Fatalf("%s: the old %s's sign-in still works: %d", when, name, r.Status)
			}
		}
		st := e.call("GET", "/api/admin/standings", e.admin(), nil).Body
		if n := len(st["teams"].([]any)); n != 0 {
			t.Fatalf("%s: %d teams left", when, n)
		}
		if ov := e.call("GET", "/api/admin/overview", e.admin(), nil).Body["clock"].(map[string]any); ov["status"] != "not_started" {
			t.Fatalf("%s: the clock is %v, want not started", when, ov["status"])
		}
	}
	check(e, "straight after")
	e = e.restart(rb(t, 100))
	check(e, "after a restart")

	// the same people register again with the same emails
	lead2, _ := e.signup("Comet")
	code2 := teamCode(t, e, lead2)
	if r := join(e, code2, "mate"); r.Status != 200 {
		t.Fatalf("the teammate joining again with the same email: %d %s", r.Status, r.Raw)
	}
	e.signup("Nova")
	if v := e.call("GET", "/api/team", lead2, nil).Body; len(v["members"].([]any)) != 1 {
		t.Fatalf("the new team: %v", v)
	}
	if afterwards != nil {
		afterwards()
	}
}
