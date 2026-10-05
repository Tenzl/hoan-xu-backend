package affiliate

import (
	"context"
	"strings"

	"hoanxu/internal/platform"
)

func (s *Service) PublisherID(ctx context.Context) (string, error) {
	var id string
	err := s.Store.Pool.QueryRow(ctx, `SELECT coalesce(settings->>'publisher','') FROM affiliate_channels WHERE id='shopee'`).Scan(&id)
	if id == "" && err == nil {
		id = s.Publisher // Compatibility for existing integrations.
	}
	return id, err
}

func (s *Service) SavePublisher(ctx context.Context, actor, id string) error {
	id = strings.TrimSpace(id)
	if len(id) > 32 {
		return platform.Fail(422, "INVALID_AFFILIATE_ID", "Affiliate ID chỉ gồm chữ số, tối đa 32 ký tự.")
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			return platform.Fail(422, "INVALID_AFFILIATE_ID", "Affiliate ID chỉ gồm chữ số, tối đa 32 ký tự.")
		}
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE affiliate_channels SET settings=jsonb_set(settings,'{publisher}',to_jsonb($1::text),true) WHERE id='shopee'`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return platform.Fail(404, "NOT_FOUND", "Không tìm thấy cấu hình Shopee.")
	}
	if err = platform.Audit(ctx, tx, actor, "shopee_publisher_updated", "shopee", map[string]string{"publisher": id}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
