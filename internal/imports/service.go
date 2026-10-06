package imports

import (
	"context"
	"encoding/json"
	"errors"
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
	counts := map[string]int{"valid": 0, "invalid": 0, "unmatched": 0, "ignored": 0}
	for i, row := range rows {
		status, err := validateRow(ctx, tx, s.Store, &row)
		if err != nil {
			return nil, err
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
	status := "applied"
	validation, err := validateRow(ctx, tx, s.Store, &row)
	if err != nil {
		return false, err
	}
	if validation != "valid" {
		status = validation
	} else {
		if row.NativeShopee {
			a, err := Attribute(ctx, tx, s.Store, &row)
			if err != nil {
				return false, err
			}
			if e = platform.LockTracking(ctx, tx, a.User, row.Tracking); e != nil {
				return false, e
			}
		}
		if e = cashback.LockOrder(ctx, tx, row.Channel, row.Publisher, row.OrderID, row.LineID); e != nil {
			return false, e
		}
		var oldStatus, mode, trackingCode string
		var oldCommission, oldValue int64
		var oldBps int
		e = tx.QueryRow(ctx, `SELECT status,commission,value,share_bps,cashback_mode,coalesce(tracking_code,'') FROM orders WHERE channel=$1 AND publisher=$2 AND external_id=$3 AND line_id=$4 FOR UPDATE`, row.Channel, row.Publisher, row.OrderID, row.LineID).Scan(&oldStatus, &oldCommission, &oldValue, &oldBps, &mode, &trackingCode)
		if e == nil {
			if trackingCode != "" && trackingCode != row.Tracking {
				status = "ignored"
				row.Error = "Tracking không khớp đơn đã nhập"
			} else {
				status = "duplicate"
				if oldStatus == "approved" && (oldCommission != row.Commission || row.Status != "approved" || oldValue != row.Value) {
					status = "adjustment"
					// Preserve the existing explicit ledger adjustment flow, but retain the latest source state.
					_, e = tx.Exec(ctx, `UPDATE orders SET source_status=$5 WHERE channel=$1 AND publisher=$2 AND external_id=$3 AND line_id=$4`, row.Channel, row.Publisher, row.OrderID, row.LineID, row.Status)
					if e != nil {
						return false, e
					}
				} else if oldStatus == "pending" || oldStatus == "rejected" {
					cash, er := cashback.OrderAmount(row.Commission, oldBps, mode)
					if er != nil {
						return false, er
					}
					next := "pending"
					if row.Status == "rejected" {
						cash = 0
						next = "rejected"
					}
					_, e = tx.Exec(ctx, `UPDATE orders SET source_status=$8,product_name=$5,value=$6,commission=$7,cashback=$9,status=$10,ordered_at=$11 WHERE channel=$1 AND publisher=$2 AND external_id=$3 AND line_id=$4`, row.Channel, row.Publisher, row.OrderID, row.LineID, row.Name, row.Value, row.Commission, row.Status, cash, next, row.Date)
					if e != nil {
						return false, e
					}
					status = "applied"
				}
			}
		} else if errors.Is(e, pgx.ErrNoRows) {
			_, _, e = InsertSignedOrder(ctx, tx, s.Store, &row)
			if e != nil {
				return false, e
			}
		} else {
			return false, e
		}
	}

	_, e = tx.Exec(ctx, `UPDATE import_rows SET status=$3,error=nullif($4,'') WHERE batch_id=$1 AND row_number=$2`, batch, number, status, row.Error)
	if e != nil {
		return false, e
	}
	return false, tx.Commit(ctx)
}
