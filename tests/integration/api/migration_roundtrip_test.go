package api

import (
	"context"
	"hoanxu/internal/wallet"
	"os"
	"path/filepath"
	"testing"
)

func TestAuditMigrationsRoundTripPreservesFinancialHistory(t *testing.T) {
	store, user, _ := testStore(t)
	ctx := context.Background()
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.Credit(ctx, tx, user, "migration-funds", "Historical funds", 12345); err == nil {
		err = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		t.Fatal(err)
	}
	var before string
	digest := `SELECT md5(string_agg(to_jsonb(t)::text,E'\n' ORDER BY to_jsonb(t)::text)) FROM wallet_entries t`
	if err = store.Pool.QueryRow(ctx, digest).Scan(&before); err != nil {
		t.Fatal(err)
	}
	names := []string{"000016_import_review", "000017_link_operations", "000018_file_lifecycle", "000019_ledger_reconciliation", "000020_link_product_name", "000021_internal_access"}
	run := func(name, direction string) {
		t.Helper()
		sql, err := os.ReadFile(filepath.Join("../../database/migrations", name+"."+direction+".sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Pool.Exec(ctx, string(sql)); err != nil {
			t.Fatalf("%s %s: %v", name, direction, err)
		}
	}
	for i := len(names) - 1; i >= 0; i-- {
		run(names[i], "down")
	}
	for _, name := range names {
		run(name, "up")
	}
	var after string
	if err = store.Pool.QueryRow(ctx, digest).Scan(&after); err != nil || after != before {
		t.Fatal("historical ledger changed", err)
	}
	var balance int64
	if err = store.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, user).Scan(&balance); err != nil || balance != 12345 {
		t.Fatal(balance, err)
	}
	if n, err := wallet.FullReconcile(ctx, store); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
