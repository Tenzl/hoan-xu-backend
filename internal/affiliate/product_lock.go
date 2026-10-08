package affiliate

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

// Serialize generation/deletion across API instances without a transaction over
// the Shopee network request. Release the session lock before returning the connection.
func (s *Service) lockProduct(ctx context.Context, user, canonical string) (*pgxpool.Conn, func(), error) {
	key := "affiliate-product:" + user + ":" + canonical
	var conn *pgxpool.Conn
	for {
		var err error
		conn, err = s.Store.Pool.Acquire(ctx)
		if err != nil {
			return nil, nil, err
		}
		var locked bool
		if err = conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil {
			cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
			_ = conn.Hijack().Close(cleanup)
			done()
			return nil, nil, err
		}
		if locked {
			break
		}
		// Waiters must not occupy every pooled connection while the winner works.
		conn.Release()
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
	unlock := func() {
		cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		var released bool
		if err := conn.QueryRow(cleanup, `SELECT pg_advisory_unlock(hashtextextended($1,0))`, key).Scan(&released); err != nil || !released {
			_ = conn.Hijack().Close(cleanup)
			return
		}
		conn.Release()
	}
	return conn, unlock, nil
}
