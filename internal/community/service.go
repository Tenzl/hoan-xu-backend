package community

import (
	"context"
	"hoanxu/internal/platform"
	"strings"
)

type Service struct{ Store *platform.Store }

func (s *Service) Post(ctx context.Context, user, channel, body string) (any, error) {
	body = strings.TrimSpace(body)
	if channel != "shopee" && channel != "lazada" && channel != "tiktok" && channel != "tiki" {
		return nil, platform.Fail(422, "INVALID_CHANNEL", "Kênh không hợp lệ.")
	}
	if !platform.Text(body, 1, 400) {
		return nil, platform.Fail(422, "VALIDATION_ERROR", "Nội dung cần 1–400 ký tự.")
	}
	var id string
	e := s.Store.Pool.QueryRow(ctx, `INSERT INTO deals(user_id,channel,body) VALUES($1,$2,$3) RETURNING id::text`, user, channel, body).Scan(&id)
	return map[string]string{"id": id}, e
}
func (s *Service) Like(ctx context.Context, user, id string, like bool) error {
	if like {
		tag, e := s.Store.Pool.Exec(ctx, `INSERT INTO deal_likes SELECT id,$2 FROM deals WHERE id=$1 AND NOT hidden AND NOT deleted ON CONFLICT DO NOTHING`, id, user)
		if e != nil {
			return e
		}
		_ = tag
		return nil
	}
	_, e := s.Store.Pool.Exec(ctx, `DELETE FROM deal_likes WHERE deal_id=$1 AND user_id=$2`, id, user)
	return e
}
func (s *Service) Moderate(ctx context.Context, actor, id, action, reason string) error {
	if !platform.Text(reason, 3, 500) {
		return platform.Fail(422, "REASON_REQUIRED", "Cần lý do kiểm duyệt.")
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var tag interface{}
	_ = tag
	switch action {
	case "hide", "show":
		_, e = tx.Exec(ctx, `UPDATE deals SET hidden=$2,reason=$3 WHERE id=$1 AND NOT deleted`, id, action == "hide", reason)
	case "delete":
		_, e = tx.Exec(ctx, `UPDATE deals SET deleted=true,reason=$2 WHERE id=$1`, id, reason)
	default:
		return platform.Fail(422, "INVALID_ACTION", "Thao tác không hợp lệ.")
	}
	if e != nil {
		return e
	}
	if e = platform.Audit(ctx, tx, actor, "deal_"+action, id, map[string]string{"reason": reason}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func (s *Service) Notify(ctx context.Context, actor, recipient, title, body string) (any, error) {
	if !platform.Text(title, 1, 80) || !platform.Text(body, 1, 1000) || (recipient != "" && !platform.ID(recipient)) {
		return nil, platform.Fail(422, "VALIDATION_ERROR", "Thông báo không hợp lệ.")
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO notifications(recipient_id,title,body) VALUES(nullif($1,'')::uuid,$2,$3) RETURNING id::text`, recipient, title, body).Scan(&id)
	if e != nil {
		return nil, e
	}
	if e = platform.Audit(ctx, tx, actor, "notification_created", id, map[string]string{"recipient": recipient}); e != nil {
		return nil, e
	}
	return map[string]string{"id": id}, tx.Commit(ctx)
}
