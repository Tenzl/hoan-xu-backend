package platform

import (
	"context"
	"github.com/jackc/pgx/v5"
)

// Acquire before locking an order or link row. Serializes deletion, imports,
// cancellation notifications and review events for the same customer/token.
func LockTracking(ctx context.Context, tx pgx.Tx, user, code string) error {
	if code == "" {
		return nil
	}
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "cashback-link:"+user+":"+code)
	return err
}
