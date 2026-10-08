package affiliate

import (
	"context"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"time"
)

// Saved-link state is independent of order status.
const LinkJSON = `jsonb_build_object('productName',coalesce(nullif(l.product_name,''),(SELECT product_name FROM orders n WHERE n.user_id=l.user_id AND (n.link_id=l.id OR n.tracking_code=l.tracking_code) ORDER BY n.ordered_at DESC,n.id DESC LIMIT 1)),'id',l.id,'channel',l.channel,'originalUrl',l.original_url,'affiliateUrl',l.affiliate_url,'trackingCode',l.tracking_code,'createdAt',l.created_at,'expiresAt',l.expires_at,'autoDeleteAt',CASE WHEN l.tracking_sub_ids IS NULL THEN NULL ELSE l.created_at+interval '120 hours' END,'policyId',l.policy_id,'tierCode',l.tier_code,'tierNameVi',(SELECT name_vi FROM cashback_tiers WHERE policy_id=l.policy_id AND tier_code=l.tier_code),'tierNameEn',(SELECT name_en FROM cashback_tiers WHERE policy_id=l.policy_id AND tier_code=l.tier_code),'minSharePercent',l.min_share_bps::numeric/100,'maxSharePercent',l.max_share_bps::numeric/100,'effectiveSharePercent',l.effective_share_bps::numeric/100,'payoutFactor',l.payout_factor::text,'legacy',l.tracking_sub_ids IS NULL,'status',CASE WHEN l.tracking_sub_ids IS NULL THEN 'legacy' ELSE 'active' END,'canDelete',l.tracking_sub_ids IS NOT NULL AND l.deleted_at IS NULL)`
const LinkFromSQL = `FROM affiliate_links l`
const VisibleLinksSQL = `l.deleted_at IS NULL AND (l.tracking_sub_ids IS NULL OR l.created_at>now()-interval '120 hours')`
const Retention = 5 * 24 * time.Hour

const LinksSQL = "SELECT " + LinkJSON + " " + LinkFromSQL

func (s *Service) DeleteLink(ctx context.Context, user, id string) error {
	_, err := s.deleteLink(ctx, user, id, nil)
	return err
}

func (s *Service) deleteLink(ctx context.Context, user, id string, cutoff *time.Time) (bool, error) {
	if !platform.ID(id) {
		return false, platform.Fail(404, "NOT_FOUND", "Không có link.")
	}
	var canonical string
	err := s.Store.Pool.QueryRow(ctx, `SELECT original_url FROM affiliate_links WHERE id=$1 AND user_id=$2`, id, user).Scan(&canonical)
	if err == pgx.ErrNoRows {
		if cutoff != nil {
			return false, nil
		}
		return false, platform.Fail(404, "NOT_FOUND", "Không có link.")
	}
	if err != nil {
		return false, err
	}
	conn, unlock, err := s.lockProduct(ctx, user, canonical)
	if err != nil {
		return false, err
	}
	defer unlock()
	tx, err := conn.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var code string
	var legacy, due bool
	err = tx.QueryRow(ctx, `SELECT tracking_code,tracking_sub_ids IS NULL,($3::timestamptz IS NULL OR deleted_at IS NOT NULL OR created_at<=$3) FROM affiliate_links WHERE id=$1 AND user_id=$2`, id, user, cutoff).Scan(&code, &legacy, &due)
	if err == pgx.ErrNoRows {
		if cutoff != nil {
			return false, nil
		}
		return false, platform.Fail(404, "NOT_FOUND", "Không có link.")
	}
	if err != nil {
		return false, err
	}
	if legacy {
		return false, platform.Fail(403, "LEGACY_LINK_READ_ONLY", "Link lịch sử — chỉ đọc")
	}
	if !due {
		return false, nil
	}
	if err = deleteSavedLink(ctx, tx, user, id, code); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func deleteSavedLink(ctx context.Context, tx pgx.Tx, user, id, code string) error {
	if err := platform.LockTracking(ctx, tx, user, code); err != nil {
		return err
	}
	// Keep the attribution of any previously linked order before SET NULL detaches it.
	if _, err := tx.Exec(ctx, `UPDATE orders SET tracking_code=$3 WHERE user_id=$1 AND link_id=$2 AND tracking_code IS NULL`, user, id, code); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `DELETE FROM affiliate_links WHERE id=$1 AND user_id=$2 AND tracking_sub_ids IS NOT NULL`, id, user)
	return err
}

// Startup and minute maintenance use bounded batches; order state never blocks cleanup.
func (s *Service) PurgeExpired(ctx context.Context, now time.Time) (int, error) {
	cutoff := now.Add(-Retention)
	rows, err := s.Store.Pool.Query(ctx, `SELECT id::text,user_id::text FROM affiliate_links WHERE tracking_sub_ids IS NOT NULL AND (deleted_at IS NOT NULL OR created_at<=$1) ORDER BY created_at,id LIMIT 200`, cutoff)
	if err != nil {
		return 0, err
	}
	type candidate struct{ id, user string }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.id, &c.user); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	count := 0
	for _, c := range candidates {
		removed, e := s.deleteLink(ctx, c.user, c.id, &cutoff)
		if e != nil {
			return count, e
		}
		if removed {
			count++
		}
	}
	return count, nil
}
