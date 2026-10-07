package affiliate

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"strings"
	"time"
)

// CreateLinkOperation survives a disconnected caller; a key is never sampled twice.
func (s *Service) CreateLinkOperation(ctx context.Context, user, key, raw string) (any, error) {
	if !platform.Text(key, 8, 128) {
		return nil, platform.Fail(422, "IDEMPOTENCY_REQUIRED", "Cần khóa cho yêu cầu tạo link.")
	}
	hash := platform.Hash(strings.TrimSpace(raw))
	tag, err := s.Store.Pool.Exec(ctx, `INSERT INTO link_operations(user_id,key,payload_hash,status) VALUES($1,$2,$3,'running') ON CONFLICT DO NOTHING`, user, key, hash)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		var existing, status string
		var response []byte
		var link *string
		var code, message *string
		var httpStatus *int
		err = s.Store.Pool.QueryRow(ctx, `SELECT payload_hash,status,response||jsonb_build_object('canDelete',false),link_id::text,error_status,error_code,error_message FROM link_operations WHERE user_id=$1 AND key=$2`, user, key).Scan(&existing, &status, &response, &link, &httpStatus, &code, &message)
		if err != nil {
			return nil, err
		}
		if existing != hash {
			return nil, platform.Fail(409, "IDEMPOTENCY_CONFLICT", "Khóa yêu cầu đã dùng cho dữ liệu khác.")
		}
		switch status {
		case "succeeded":
			if link == nil {
				return nil, platform.Fail(410, "LINK_DELETED", "Link của yêu cầu này đã bị xóa.")
			}
			return json.RawMessage(response), nil
		case "failed":
			if httpStatus != nil && code != nil && message != nil {
				return nil, platform.Fail(*httpStatus, *code, *message)
			}
		case "indeterminate":
			return nil, platform.Fail(409, "LINK_CREATION_UNCERTAIN", "Yêu cầu tạo link chưa xác định kết quả. Kiểm tra danh sách link trước khi thử yêu cầu mới.")
		}
		return nil, platform.Fail(409, "LINK_CREATION_IN_PROGRESS", "Yêu cầu tạo link đang được xử lý.")
	}
	work, cancel := context.WithTimeout(context.WithoutCancel(ctx), 55*time.Second)
	defer cancel()
	if s.Lifetime != nil {
		stop := context.AfterFunc(s.Lifetime, cancel)
		defer stop()
	}
	value, err := s.createLink(work, user, raw, func(tx pgx.Tx, id string, result any) error {
		payload, e := json.Marshal(result)
		if e != nil {
			return e
		}
		_, e = tx.Exec(work, `UPDATE link_operations SET status='succeeded',link_id=$3,response=$4,updated_at=now() WHERE user_id=$1 AND key=$2 AND status='running'`, user, key, id, payload)
		return e
	})
	if err == nil {
		return value, nil
	}
	// Even on a timeout, persist terminal state using a small independent DB budget.
	mark, done := context.WithTimeout(context.Background(), 5*time.Second)
	defer done()
	status := "failed"
	problem := &platform.Error{Status: 500, Code: "INTERNAL_ERROR", Message: "Không xử lý được yêu cầu."}
	var known *platform.Error
	if errors.As(err, &known) {
		problem = known
	}
	if work.Err() != nil || problem.Code == "SHOPEE_TIMEOUT" {
		status = "indeterminate"
	}
	_, markErr := s.Store.Pool.Exec(mark, `UPDATE link_operations SET status=$3,error_status=$4,error_code=$5,error_message=$6,updated_at=now() WHERE user_id=$1 AND key=$2 AND status='running'`, user, key, status, problem.Status, problem.Code, problem.Message)
	if markErr != nil {
		return nil, markErr
	}
	if status == "indeterminate" {
		return nil, platform.Fail(409, "LINK_CREATION_UNCERTAIN", "Yêu cầu tạo link chưa xác định kết quả. Kiểm tra danh sách link trước khi thử yêu cầu mới.")
	}
	return nil, err
}
