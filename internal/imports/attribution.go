package imports

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"

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

// Attribute uses the authenticated snapshot; saved links and policy rates are
// deliberately absent from reconciliation. Policies remain audit references.
func Attribute(ctx context.Context, tx pgx.Tx, s *platform.Store, row *Row) (Attribution, error) {
	var a Attribution
	if !row.NativeShopee || row.Channel != "shopee" {
		return a, ineligible("Không thuộc chiến dịch Hoàn Xu hoặc dùng mã link cũ")
	}
	c, err := s.Tracking.Decode(row.SubIDs)
	if err != nil {
		var missing *tracking.KeyError
		if errors.As(err, &missing) {
			return a, platform.TrackingError(err)
		}
		return a, ineligible("Tracking v4 không hợp lệ hoặc sai xác thực")
	}
	if row.Publisher != "" && row.Publisher != c.Publisher {
		return a, ineligible("Affiliate ID không hợp lệ")
	}
	var authorized bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM tracking_publishers WHERE publisher=$1 UNION ALL SELECT 1 FROM affiliate_channels WHERE id='shopee' AND settings->>'publisher'=$1)`, c.Publisher).Scan(&authorized)
	if err != nil {
		return a, err
	}
	if !authorized {
		return a, ineligible("Affiliate ID không hợp lệ")
	}
	if row.ShopID != strconv.FormatUint(c.Shop, 10) || row.ItemID != strconv.FormatUint(c.Item, 10) {
		return a, ineligible("Sản phẩm không khớp link Hoàn Xu")
	}
	if !c.Eligible(row.Date) {
		return a, ineligible("Order Time trước thời điểm phát hành tracking")
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM users WHERE id=$1 AND role='customer' AND NOT blocked`, c.Customer).Scan(&a.User)
	if err == pgx.ErrNoRows {
		return a, ineligible("Khách hàng không hợp lệ")
	}
	if err != nil {
		return a, err
	}
	err = tx.QueryRow(ctx, `SELECT id::text FROM cashback_policies WHERE tracking_version=$1`, c.Policy).Scan(&a.Policy)
	if err == pgx.ErrNoRows {
		return a, ineligible("Phiên bản chính sách không hợp lệ")
	}
	if err != nil {
		return a, err
	}
	row.Publisher = c.Publisher
	row.Tracking, err = tracking.Code(row.SubIDs)
	if err != nil {
		return a, err
	}
	a.Claims = c
	a.EffectiveBps = c.EffectiveBps
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
	row.Error = "Chỉ hỗ trợ báo cáo Shopee có đủ năm phần tracking v4"
	return "ignored", nil
}
