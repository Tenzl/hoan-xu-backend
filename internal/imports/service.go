package imports

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"log/slog"
	"time"
)

type Service struct{ Store *platform.Store }

func (s *Service) Preview(ctx context.Context, actor, filename, hash string, mapping map[string]string, rows []Row) (any, error) {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var id string
	m, _ := json.Marshal(mapping)
	e = tx.QueryRow(ctx, `INSERT INTO import_batches(actor_id,filename,file_hash,mapping) VALUES($1,$2,$3,$4) RETURNING id::text`, actor, filename, hash, m).Scan(&id)
	if e != nil {
		return nil, e
	}
	counts := map[string]int{"valid": 0, "invalid": 0, "unmatched": 0}
	for i, row := range rows {
		status := "valid"
		if row.Error != "" {
			status = "invalid"
		} else {
			var exists bool
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM affiliate_links WHERE tracking_code=$1)`, row.Tracking).Scan(&exists)
			if e != nil {
				return nil, e
			}
			if !exists {
				status = "unmatched"
			}
		}
		counts[status]++
		b, _ := json.Marshal(row)
		if _, e = tx.Exec(ctx, `INSERT INTO import_rows VALUES($1,$2,$3,$4,nullif($5,''))`, id, i+1, b, status, row.Error); e != nil {
			return nil, e
		}
	}
	if e = platform.Audit(ctx, tx, actor, "import_preview", id, counts); e != nil {
		return nil, e
	}
	return map[string]any{"id": id, "counts": counts, "status": "preview"}, tx.Commit(ctx)
}
func (s *Service) Commit(ctx context.Context, actor, key, id string) (any, error) {
	return s.Store.Action(ctx, actor, key, "import-commit:"+id, map[string]string{"id": id}, func(tx pgx.Tx) (any, error) {
		var st string
		if e := tx.QueryRow(ctx, `SELECT status FROM import_batches WHERE id=$1 FOR UPDATE`, id).Scan(&st); e != nil {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có batch.")
		}
		if st != "preview" {
			return nil, platform.Fail(409, "INVALID_TRANSITION", "Batch đã được commit.")
		}
		var invalid int
		if e := tx.QueryRow(ctx, `SELECT count(*) FROM import_rows WHERE batch_id=$1 AND status='invalid'`, id).Scan(&invalid); e != nil {
			return nil, e
		}
		if invalid > 0 {
			return nil, platform.Fail(422, "CSV_ERRORS", "Cần sửa lỗi cấu trúc trước khi commit.")
		}
		_, e := tx.Exec(ctx, `UPDATE import_batches SET status='queued' WHERE id=$1`, id)
		return map[string]string{"id": id, "status": "queued"}, e
	})
}
func (s *Service) Run(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			if e := s.work(ctx); e != nil {
				slog.Error("import_worker", "error", e)
			}
		}
	}
}
func (s *Service) work(ctx context.Context) error {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var batch string
	e = tx.QueryRow(ctx, `SELECT id::text FROM import_batches WHERE status='queued' OR (status='processing' AND lease_until<now()) ORDER BY created_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&batch)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	_, e = tx.Exec(ctx, `UPDATE import_batches SET status='processing',lease_until=now()+interval '2 minutes',attempts=attempts+1 WHERE id=$1`, batch)
	if e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		done, e := s.applyOne(ctx, batch)
		if e != nil {
			_, mark := s.Store.Pool.Exec(ctx, `UPDATE import_batches SET status='failed',error='Lỗi xử lý batch; kiểm tra log và thử lại.' WHERE id=$1`, batch)
			if mark != nil {
				return mark
			}
			return e
		}
		if done {
			return nil
		}
	}
}
func (s *Service) applyOne(ctx context.Context, batch string) (bool, error) {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return false, e
	}
	defer tx.Rollback(ctx)
	if _, e = tx.Exec(ctx, `UPDATE import_batches SET lease_until=now()+interval '2 minutes' WHERE id=$1`, batch); e != nil {
		return false, e
	}
	var number int
	var payload []byte
	e = tx.QueryRow(ctx, `SELECT row_number,payload FROM import_rows WHERE batch_id=$1 AND status='valid' ORDER BY row_number FOR UPDATE SKIP LOCKED LIMIT 1`, batch).Scan(&number, &payload)
	if e == pgx.ErrNoRows {
		_, e = tx.Exec(ctx, `UPDATE import_batches SET status='completed',lease_until=NULL WHERE id=$1`, batch)
		if e != nil {
			return false, e
		}
		return true, tx.Commit(ctx)
	}
	if e != nil {
		return false, e
	}
	var row Row
	if e = json.Unmarshal(payload, &row); e != nil {
		return false, e
	}
	var user, link, policy string
	var tier *string
	var minBps, maxBps int
	e = tx.QueryRow(ctx, `SELECT user_id::text,id::text,policy_id::text,tier_code,min_share_bps,max_share_bps FROM affiliate_links WHERE tracking_code=$1 AND channel=$2`, row.Tracking, row.Channel).Scan(&user, &link, &policy, &tier, &minBps, &maxBps)
	status := "applied"
	if e == pgx.ErrNoRows {
		status = "unmatched"
	} else if e != nil {
		return false, e
	} else {
		if e = cashback.LockOrder(ctx, tx, row.Channel, row.Publisher, row.OrderID, row.LineID); e != nil {
			return false, e
		}
		var oldStatus string
		var oldCommission, oldValue int64
		var oldBps int
		e = tx.QueryRow(ctx, `SELECT status,commission,value,share_bps FROM orders WHERE channel=$1 AND publisher=$2 AND external_id=$3 AND line_id=$4 FOR UPDATE`, row.Channel, row.Publisher, row.OrderID, row.LineID).Scan(&oldStatus, &oldCommission, &oldValue, &oldBps)
		if e == nil {
			status = "duplicate"
			if oldStatus == "approved" && (oldCommission != row.Commission || row.Status != "approved" || oldValue != row.Value) {
				status = "adjustment"
			} else if oldStatus == "pending" {
				cash, er := cashback.Amount(row.Commission, oldBps)
				if er != nil {
					return false, er
				}
				_, e = tx.Exec(ctx, `UPDATE orders SET source_status=$8,product_name=$5,value=$6,commission=$7,cashback=$9 WHERE channel=$1 AND publisher=$2 AND external_id=$3 AND line_id=$4`, row.Channel, row.Publisher, row.OrderID, row.LineID, row.Name, row.Value, row.Commission, row.Status, cash)
				if e != nil {
					return false, e
				}
				status = "applied"
			}
		} else if e == pgx.ErrNoRows {
			bps, er := cashback.Sample(nil, minBps, maxBps)
			if er != nil {
				return false, er
			}
			cash, er := cashback.Amount(row.Commission, bps)
			if er != nil {
				return false, er
			}
			_, e = tx.Exec(ctx, `INSERT INTO orders(user_id,link_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,ordered_at,source_status,tier_code,share_bps) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$13,$11,$12,$14,$15)`, user, link, policy, row.Channel, row.Publisher, row.OrderID, row.LineID, row.Name, row.Value, row.Commission, row.Date, row.Status, cash, tier, bps)
			if e != nil {
				return false, e
			}
		} else {
			return false, e
		}
	}
	_, e = tx.Exec(ctx, `UPDATE import_rows SET status=$3 WHERE batch_id=$1 AND row_number=$2`, batch, number, status)
	if e != nil {
		return false, e
	}
	return false, tx.Commit(ctx)
}
