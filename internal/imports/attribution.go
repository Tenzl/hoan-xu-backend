package imports

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"hoanxu/internal/tracking"
	"strconv"
)

type Ineligible struct{ Reason string }

func (e *Ineligible) Error() string { return e.Reason }
func ineligible(s string) error     { return &Ineligible{s} }

type Attribution struct {
	User, Policy string
	Claims       tracking.Claims
	EffectiveBps int
}

// Attribute validates a native report against the signed promise and archived policy.
// It never trusts an administrator-supplied customer or a currently active policy.
func Attribute(ctx context.Context, tx pgx.Tx, s *platform.Store, row *Row) (Attribution, error) {
	var a Attribution
	if !row.NativeShopee || row.Channel != "shopee" || row.SubIDs[1] != "hoanxu" {
		return a, ineligible("Không thuộc chiến dịch Hoàn Xu hoặc dùng mã link cũ")
	}
	var publisher string
	rows, e := tx.Query(ctx, `SELECT publisher FROM tracking_publishers UNION SELECT settings->>'publisher' FROM affiliate_channels WHERE id='shopee' AND coalesce(settings->>'publisher','')<>''`)
	if e != nil {
		return a, e
	}
	var candidates []string
	for rows.Next() {
		var candidate string
		if e = rows.Scan(&candidate); e != nil {
			rows.Close()
			return a, e
		}
		candidates = append(candidates, candidate)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return a, e
	}
	var c tracking.Claims
	matches := 0
	for _, candidate := range candidates {
		if row.Publisher != "" && row.Publisher != candidate {
			continue
		}
		claims, err := tracking.Verify(row.SubIDs, candidate, s.SignTracking)
		if err == nil {
			matches++
			publisher, c = candidate, claims
		}
	}
	if matches != 1 {
		return a, ineligible("Chữ ký tracking hoặc Affiliate ID không hợp lệ")
	}
	row.Publisher = publisher
	if row.ShopID != strconv.FormatUint(c.Shop, 10) || row.ItemID != strconv.FormatUint(c.Item, 10) {
		return a, ineligible("Sản phẩm không khớp link Hoàn Xu")
	}
	if !c.Eligible(row.Date) {
		return a, ineligible("Order Time nằm ngoài thời hạn hoàn Xu của link")
	}
	e = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE tracking_code=$1 AND role='customer' AND NOT blocked`, row.SubIDs[0]).Scan(&a.User)
	if e == pgx.ErrNoRows {
		return a, ineligible("Khách hàng không hợp lệ")
	}
	if e != nil {
		return a, e
	}
	var min, max, tax int
	e = tx.QueryRow(ctx, `SELECT p.id::text,t.min_share_bps,t.max_share_bps,p.tax_bps FROM cashback_policies p JOIN cashback_tiers t ON t.policy_id=p.id WHERE p.tracking_version=$1 AND t.tier_code=$2`, c.Policy, c.Tier).Scan(&a.Policy, &min, &max, &tax)
	if e == pgx.ErrNoRows {
		return a, ineligible("Phiên bản chính sách không hợp lệ")
	}
	if e != nil {
		return a, e
	}
	if c.Bps < min || c.Bps > max {
		return a, ineligible("Tỷ lệ đã chốt không hợp lệ")
	}
	effective, e := cashback.EffectiveRate(c.Bps, tax)
	if e != nil || !tracking.MatchesFactor(row.SubIDs[3], (cashback.LinkRate{EffectiveBps: effective}).Factor()) {
		return a, ineligible("Hệ số hoàn Xu không khớp chính sách")
	}
	row.Tracking = row.SubIDs[2]
	a.Claims = c
	a.EffectiveBps = effective
	return a, nil
}

func validateRow(ctx context.Context, tx pgx.Tx, s *platform.Store, row *Row) (string, error) {
	if row.Error != "" {
		return "invalid", nil
	}
	if row.NativeShopee {
		_, e := Attribute(ctx, tx, s, row)
		if e != nil {
			var invalid *Ineligible
			if !errors.As(e, &invalid) {
				return "", e
			}
			row.Error = e.Error()
			return "ignored", nil
		}
		return "valid", nil
	}
	// Legacy tracking is usable solely for updating an existing order.
	var exists bool
	e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders o LEFT JOIN affiliate_links l ON l.id=o.link_id WHERE o.channel=$1 AND o.publisher=$2 AND o.external_id=$3 AND o.line_id=$4 AND coalesce(o.tracking_code,l.tracking_code)=$5 AND o.cashback_mode='commission_share')`, row.Channel, row.Publisher, row.OrderID, row.LineID, row.Tracking).Scan(&exists)
	if e != nil {
		return "", e
	}
	if exists {
		return "valid", nil
	}
	row.Error = "Mã link cũ không được tạo thêm đơn Hoàn Xu"
	return "ignored", nil
}
