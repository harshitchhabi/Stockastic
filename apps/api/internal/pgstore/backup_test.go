package pgstore

import (
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5"
)

// The whole point of a backup is that it can be restored. This proves the exact commands in docs/deployment.md
// (pg_dump -Fc, then pg_restore into an empty database) bring back every record, so the deploy doc is not just
// a claim nobody has run. It needs pg_dump and pg_restore on PATH (they ship with any PostgreSQL install) and is
// skipped if they are not found, rather than failing a machine that only has the server binaries.
func TestABackupCanBeRestoredIntoAnEmptyDatabase(t *testing.T) {
	dsn := testDSN(t)
	dump, err := exec.LookPath("pg_dump")
	if err != nil {
		t.Skip("pg_dump is not on PATH")
	}
	restore, err := exec.LookPath("pg_restore")
	if err != nil {
		t.Skip("pg_restore is not on PATH")
	}
	ctx := context.Background()

	l := open(t, dsn)
	for i := 0; i < 30; i++ {
		if err := l.Append("trade", map[string]int{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	dumpFile := filepath.Join(t.TempDir(), "backup.dump")
	if out, err := exec.Command(dump, "-Fc", "--dbname="+dsn, "-f", dumpFile).CombinedOutput(); err != nil {
		t.Fatalf("pg_dump: %v: %s", err, out)
	}

	// The restore target is a fresh database on the same server, matching how a real recovery works (createdb,
	// then pg_restore into it). It is derived from dsn's own connection so the test needs no extra configuration.
	admin, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "DROP DATABASE IF EXISTS stockastic_restore_test"); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE DATABASE stockastic_restore_test"); err != nil {
		t.Fatal(err)
	}
	admin.Close(ctx)
	restoreDSN := withDatabase(t, dsn, "stockastic_restore_test")

	if out, err := exec.Command(restore, "--dbname="+restoreDSN, dumpFile).CombinedOutput(); err != nil {
		t.Fatalf("pg_restore: %v: %s", err, out)
	}

	r, err := Open(ctx, Options{DSN: restoreDSN})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	var n int
	if err := r.Replay(func(kind string, raw json.RawMessage) error { n++; return nil }); err != nil {
		t.Fatal(err)
	}
	if n != 30 {
		t.Fatalf("the restored database replayed %d records, want 30", n)
	}
}

// withDatabase returns dsn with its database name replaced.
func withDatabase(t *testing.T, dsn, db string) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	return "postgres://" + cfg.User + "@" + cfg.Host + ":" + itoa(cfg.Port) + "/" + db
}

func itoa(p uint16) string {
	if p == 0 {
		return "5432"
	}
	b := [5]byte{}
	i := len(b)
	for p > 0 {
		i--
		b[i] = byte('0' + p%10)
		p /= 10
	}
	return string(b[i:])
}
