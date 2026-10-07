package wallet

import (
	"context"
	"hoanxu/internal/platform"
)

// Reconcile compares balance and entries in one MVCC statement, and never repairs money.
// A concurrent write increments revision, preventing deletion of newly queued work.
func Reconcile(ctx context.Context, s *platform.Store, limit int) (int, error) {
	rows, err := s.Pool.Query(ctx, `SELECT q.account_id::text,q.revision,a.balance=coalesce((SELECT sum(e.amount) FROM wallet_entries e WHERE e.account_id=a.id),0) FROM ledger_reconciliation_queue q JOIN wallet_accounts a ON a.id=q.account_id ORDER BY q.updated_at,q.account_id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	type result struct {
		id       string
		revision int64
		balanced bool
	}
	var results []result
	for rows.Next() {
		var r result
		if err = rows.Scan(&r.id, &r.revision, &r.balanced); err != nil {
			rows.Close()
			return 0, err
		}
		results = append(results, r)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	mismatches := 0
	for _, r := range results {
		if !r.balanced {
			mismatches++
			continue
		}
		if _, err = s.Pool.Exec(ctx, `DELETE FROM ledger_reconciliation_queue WHERE account_id=$1 AND revision=$2`, r.id, r.revision); err != nil {
			return mismatches, err
		}
	}
	return mismatches, nil
}
func FullReconcile(ctx context.Context, s *platform.Store) (int, error) {
	var count int
	err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT a.id FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0)) x`).Scan(&count)
	return count, err
}
