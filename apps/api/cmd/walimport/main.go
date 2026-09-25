// Command walimport copies the records in a data log file into PostgreSQL, once, so an event that started on the
// file can carry on on the database. It refuses to write into a database that already holds records.
//
//	walimport -wal ./data/stockastic.wal -db "postgres://user:pass@host/stockastic?sslmode=require"
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"

	"stockastic/api/internal/pgstore"
	"stockastic/api/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "walimport:", err)
		os.Exit(1)
	}
}

func run() error {
	walPath := flag.String("wal", "", "path of the data log file (stop the server first)")
	dsn := flag.String("db", os.Getenv("DATABASE_URL"), "PostgreSQL connection string")
	flag.Parse()
	if *walPath == "" || *dsn == "" {
		return fmt.Errorf("both -wal and -db (or DATABASE_URL) are needed")
	}
	ctx := context.Background()

	pg, err := pgstore.Open(ctx, pgstore.Options{DSN: *dsn})
	if err != nil {
		return err
	}
	defer pg.Close()
	conn, err := pgx.Connect(ctx, *dsn)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	var have int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&have); err != nil {
		return err
	}
	if have > 0 {
		return fmt.Errorf("the database already holds %d records; refusing to mix in another log", have)
	}

	f, err := store.OpenFile(*walPath)
	if err != nil {
		return err
	}
	defer f.Close()
	n := 0
	if err := f.Replay(func(kind string, raw json.RawMessage) error {
		n++
		return pg.Append(kind, raw)
	}); err != nil {
		return fmt.Errorf("after %d records: %w", n, err)
	}
	var stored int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&stored); err != nil {
		return err
	}
	if stored != n {
		return fmt.Errorf("read %d records but the database holds %d", n, stored)
	}
	fmt.Printf("copied %d records; the database now holds %d\n", n, stored)
	return nil
}
