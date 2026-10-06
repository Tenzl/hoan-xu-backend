package affiliate

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"time"
)

// Order state is authoritative. A late report can revive a cancelled link.
const LinksSQL = `SELECT jsonb_build_object('id',l.id,'channel',l.channel,'originalUrl',l.original_url,'affiliateUrl',l.affiliate_url,'trackingCode',l.tracking_code,'createdAt',l.created_at,'expiresAt',l.expires_at,'policyId',l.policy_id,'tierCode',l.tier_code,'minSharePercent',l.min_share_bps::numeric/100,'maxSharePercent',l.max_share_bps::numeric/100,'effectiveSharePercent',l.effective_share_bps::numeric/100,'payoutFactor',l.payout_factor::text,'legacy',l.tracking_sub_ids IS NULL,'status',CASE WHEN l.tracking_sub_ids IS NULL THEN 'legacy' WHEN o.pending>0 THEN 'progress' WHEN o.approved>0 THEN 'completed' WHEN o.total>0 OR l.expires_at<=now() OR l.lifecycle_status='cancelled' THEN 'cancelled' ELSE 'active' END,'canDelete',l.tracking_sub_ids IS NOT NULL AND o.pending=0 AND o.approved=0) FROM affiliate_links l CROSS JOIN LATERAL (SELECT count(*) AS total,count(*) FILTER(WHERE status='pending') AS pending,count(*) FILTER(WHERE status='approved') AS approved FROM orders WHERE user_id=l.user_id AND tracking_code=l.tracking_code) o`

func (s *Service) DeleteLink(ctx context.Context, user, id string) error {
	if !platform.ID(id) {
		return platform.Fail(404, "NOT_FOUND", "Không có link.")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var code string
	err = tx.QueryRow(ctx, `SELECT tracking_code FROM affiliate_links WHERE id=$1 AND user_id=$2`, id, user).Scan(&code)
	if err == pgx.ErrNoRows {
		return platform.Fail(404, "NOT_FOUND", "Không có link.")
	}
	if err != nil {
		return err
	}
	if err = platform.LockTracking(ctx, tx, user, code); err != nil {
		return err
	}
	var legacy bool
	err = tx.QueryRow(ctx, `SELECT tracking_sub_ids IS NULL FROM affiliate_links WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user).Scan(&legacy)
	if err == pgx.ErrNoRows {
		return platform.Fail(404, "NOT_FOUND", "Không có link.")
	}
	if err != nil {
		return err
	}
	if legacy {
		return platform.Fail(409, "LEGACY_LINK_READ_ONLY", "Link lịch sử chỉ được xem, không được xóa.")
	}
	var locked bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=$1 AND tracking_code=$2 AND status IN ('pending','approved'))`, user, code).Scan(&locked); err != nil {
		return err
	}
	if locked {
		return platform.Fail(409, "LINK_HAS_ORDERS", "Link đang xử lý hoặc hoàn thành, không được xóa.")
	}
	if _, err = tx.Exec(ctx, `DELETE FROM affiliate_links WHERE id=$1 AND user_id=$2`, id, user); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Bounded batches and a per-token lock allow multiple workers safely.
func (s *Service) CancelExpired(ctx context.Context, now time.Time) error {
	rows, err := s.Store.Pool.Query(ctx, `SELECT id::text,user_id::text,tracking_code FROM affiliate_links l WHERE tracking_sub_ids IS NOT NULL AND expires_at<=$1 AND cancellation_notified_at IS NULL AND NOT EXISTS(SELECT 1 FROM orders WHERE user_id=l.user_id AND tracking_code=l.tracking_code AND status IN ('pending','approved')) ORDER BY expires_at,id LIMIT 200`, now)
	if err != nil {
		return err
	}
	type candidate struct{ id, user, code string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.user, &c.code); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		if err = s.cancelOne(ctx, c.id, c.user, c.code, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) cancelOne(ctx context.Context, id, user, code string, now time.Time) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = platform.LockTracking(ctx, tx, user, code); err != nil {
		return err
	}
	var expiry time.Time
	err = tx.QueryRow(ctx, `UPDATE affiliate_links l SET lifecycle_status='cancelled',cancellation_notified_at=$2 WHERE id=$1 AND tracking_sub_ids IS NOT NULL AND expires_at<=$2 AND cancellation_notified_at IS NULL AND NOT EXISTS(SELECT 1 FROM orders WHERE user_id=l.user_id AND tracking_code=l.tracking_code AND status IN ('pending','approved')) RETURNING expires_at`, id, now).Scan(&expiry)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	body := fmt.Sprintf("Link %s đã bị cancel — hết thời hạn hoàn Xu lúc %s. Đơn đặt đúng hạn vẫn được ghi nhận khi có báo cáo Shopee, kể cả báo cáo về muộn.", id, expiry.In(time.FixedZone("Vietnam", 7*3600)).Format("02/01/2006 15:04 GMT+7"))
	if _, err = tx.Exec(ctx, `INSERT INTO notifications(recipient_id,title,body) VALUES($1,'Link đã bị cancel — hết thời hạn hoàn Xu',$2)`, user, body); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
