package api

import (
	"context"
	"hoanxu/internal/wallet"
	"testing"
)

func TestIncrementalReconciliationDetectsDriftWithoutRepairingBalance(t *testing.T) {
	s, user, _ := testStore(t)
	ctx := context.Background()
	if n, err := wallet.Reconcile(ctx, s, 100); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	var pending int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM ledger_reconciliation_queue`).Scan(&pending); err != nil || pending != 0 {
		t.Fatal(pending, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE wallet_accounts SET balance=123 WHERE user_id=$1 AND kind='available'`, user); err != nil {
		t.Fatal(err)
	}
	if n, err := wallet.Reconcile(ctx, s, 100); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	var balance int64
	if err := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, user).Scan(&balance); err != nil || balance != 123 {
		t.Fatal("reconciliation modified money", balance, err)
	}
	if n, err := wallet.FullReconcile(ctx, s); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE wallet_accounts SET balance=0 WHERE user_id=$1 AND kind='available'`, user); err != nil {
		t.Fatal(err)
	}
	if n, err := wallet.Reconcile(ctx, s, 100); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}
