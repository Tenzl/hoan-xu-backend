package wallet

import (
	"context"
	"sort"

	"github.com/jackc/pgx/v5"
)

// RefreshGoldTotals runs after source mutations, before committing their transaction.
// Recomputing under the existing gold system lock avoids duplicate increments on
// retries and preserves the current approved-order total after corrections.
func RefreshGoldTotals(ctx context.Context, tx pgx.Tx, users ...string) error {
	var enabled bool
	if e := tx.QueryRow(ctx, `SELECT to_regclass('wallet_user_totals') IS NOT NULL`).Scan(&enabled); e != nil {
		return e
	}
	// Historical import/migration tooling also supports schemas preceding this table.
	if !enabled {
		return nil
	}
	if _, e := tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); e != nil {
		return e
	}
	ids := append([]string(nil), users...)
	sort.Strings(ids)
	for i, user := range ids {
		if i > 0 && user == ids[i-1] {
			continue
		}
		_, e := tx.Exec(ctx, `INSERT INTO wallet_user_totals(user_id,gold_total,gold_used)
   SELECT $1::uuid,
    coalesce((SELECT sum(cashback) FROM orders WHERE user_id=$1 AND status='approved'),0),
    coalesce((SELECT sum(amount) FROM withdrawals WHERE user_id=$1 AND status='paid'),0)+
    coalesce((SELECT sum(-e.amount) FROM wallet_entries e JOIN wallet_accounts a ON a.id=e.account_id JOIN wallet_transactions t ON t.id=e.transaction_id WHERE a.user_id=$1 AND a.kind='available' AND e.amount<0 AND left(t.reference,12)='xu_exchange:'),0)
   ON CONFLICT(user_id) DO UPDATE SET gold_total=excluded.gold_total,gold_used=excluded.gold_used`, user)
		if e != nil {
			return e
		}
	}
	return nil
}
