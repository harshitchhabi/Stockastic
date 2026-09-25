package pgstore

import (
	"context"
	"testing"
	"time"

	"stockastic/api/internal/store"
)

func TestLookupTablesFollowTheRecordAndCanBeRebuilt(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	l := open(t, dsn)
	now := time.Now().UTC()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(l.Append(store.KindUser, map[string]any{"ID": "a1", "Email": "a@x.in", "DisplayName": "Alpha", "PasswordHash": "SECRET-HASH", "Role": "investor", "Status": "active", "CreatedAt": now}))
	must(l.Append(store.KindUser, map[string]any{"ID": "a1", "Email": "a@x.in", "DisplayName": "Alpha Two", "PasswordHash": "SECRET-HASH", "Role": "investor", "Status": "active", "Warnings": 1, "CreatedAt": now}))
	must(l.Append(store.KindTrade, map[string]any{"ID": "t1", "ClientTradeID": "c1", "AccountID": "a1", "Symbol": "ABC", "Side": 1, "Qty": 10, "Price": 5000, "At": now}))
	must(l.Append(store.KindTrade, map[string]any{"ID": "t2", "ClientTradeID": "c2", "AccountID": "a1", "Symbol": "ABC", "Side": 2, "Qty": 4, "Price": 5100, "At": now}))
	must(l.Append(store.KindGrant, map[string]any{"accountId": "a1", "symbol": "XYZ", "qty": 3}))
	must(l.Append(store.KindPrices, map[string]any{"tick": 1, "at": now, "prices": map[string]int64{"ABC": 5000, "XYZ": 700}}))
	must(l.Append(store.KindPrices, map[string]any{"tick": 1, "at": now, "prices": map[string]int64{"ABC": 5000, "XYZ": 700}}))
	must(l.Append(store.KindActivity, store.Activity{Account: "a1", Type: "login", IP: "1.2.3.4", UserAgent: "ua\x00x", At: now.UnixMilli()}))
	must(l.Append(store.KindActivity, store.Activity{Account: "b2", Type: "login", IP: "1.2.3.4", At: now.UnixMilli()}))
	must(l.Append(store.KindWallets, store.Wallets{At: now.UnixMilli(), W: map[string][2]int64{"a1": {100, 900}}}))
	must(l.Append(store.KindWallets, store.Wallets{At: now.Add(time.Minute).UnixMilli(), W: map[string][2]int64{"a1": {120, 950}}}))
	must(l.Append("garbage-kind", map[string]any{"x": 1}))

	p, err := NewProjector(ctx, dsn, nil)
	must(err)
	defer p.pool.Close()
	for {
		n, err := p.RunOnce(ctx)
		must(err)
		if n == 0 {
			break
		}
	}
	check := func() {
		t.Helper()
		count := func(q string) (n int) {
			if err := p.pool.QueryRow(ctx, q).Scan(&n); err != nil {
				t.Fatal(q, err)
			}
			return
		}
		if n := count("SELECT count(*) FROM accounts WHERE display_name = 'Alpha Two' AND warnings = 1"); n != 1 {
			t.Fatalf("accounts wrong (%d)", n)
		}
		if n := count("SELECT count(*) FROM trades"); n != 2 {
			t.Fatalf("%d trades", n)
		}
		if n := count("SELECT count(*) FROM price_ticks"); n != 2 {
			t.Fatalf("%d price rows, want 2 (duplicates ignored)", n)
		}
		if n := count("SELECT count(*) FROM wallet_history"); n != 2 {
			t.Fatalf("%d wallet rows", n)
		}
		// cash: -10*5000 + 4*5100 = -29600; ABC position 6, XYZ 3
		if n := count("SELECT cash_change_paise::int FROM v_cash_change WHERE account_id = 'a1'"); n != -29600 {
			t.Fatalf("cash change %d", n)
		}
		if n := count("SELECT qty::int FROM v_positions WHERE account_id = 'a1' AND symbol = 'ABC'"); n != 6 {
			t.Fatalf("ABC position %d", n)
		}
		if n := count("SELECT count(*) FROM v_shared_addresses"); n != 1 {
			t.Fatalf("shared addresses %d", n)
		}
	}
	check()
	// The password hash must never be copied into a lookup table.
	var leaked int
	must(p.pool.QueryRow(ctx, "SELECT count(*) FROM accounts WHERE row_to_json(accounts)::text LIKE '%SECRET%'").Scan(&leaked))
	if leaked != 0 {
		t.Fatal("a password hash reached the lookup tables")
	}
	// Running again changes nothing; wiping and rebuilding gives the same answers.
	if n, err := p.RunOnce(ctx); err != nil || n != 0 {
		t.Fatalf("second run took in %d (%v)", n, err)
	}
	must(p.Rebuild(ctx))
	check()

	h := p.History()
	pts, err := h.WalletHistory(ctx, "a1", 10)
	if err != nil || len(pts) != 2 || pts[1].Value != 950 {
		t.Fatalf("wallet history %v %v", pts, err)
	}
	if rows, err := h.Activity(ctx, "a1", 10); err != nil || len(rows) != 1 || rows[0].IP != "1.2.3.4" {
		t.Fatalf("activity %v %v", rows, err)
	}
	if rows, err := h.Ledger(ctx, "a1", 10); err != nil || len(rows) != 3 {
		t.Fatalf("ledger %v %v", rows, err)
	}
	if b, err := h.Behind(ctx); err != nil || b != 0 {
		t.Fatalf("behind %d %v", b, err)
	}
}

func TestAResetStartsANewEpoch(t *testing.T) {
	dsn := testDSN(t)
	ctx := context.Background()
	l := open(t, dsn)
	now := time.Now().UTC()
	_ = l.Append(store.KindTrade, map[string]any{"ID": "t1", "AccountID": "a1", "Symbol": "ABC", "Side": 1, "Qty": 1, "Price": 100, "At": now})
	_ = l.Append(store.KindReset, map[string]any{"at": now.UnixMilli()})
	_ = l.Append(store.KindTrade, map[string]any{"ID": "t2", "AccountID": "a1", "Symbol": "ABC", "Side": 1, "Qty": 2, "Price": 100, "At": now})
	p, err := NewProjector(ctx, dsn, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer p.pool.Close()
	if _, err := p.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var qty int
	if err := p.pool.QueryRow(ctx, "SELECT qty::int FROM v_positions WHERE account_id = 'a1' AND epoch = (SELECT epoch FROM projector_state)").Scan(&qty); err != nil || qty != 2 {
		t.Fatalf("current-epoch position %d (%v), want 2", qty, err)
	}
}
