package imports

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"strings"
)

func SourceLineID(row Row) (string, error) {
	if strings.TrimSpace(row.PromotionID) == "" {
		row.PromotionID = "0"
	}
	if len(row.OrderID) > 100 || !orderID.MatchString(row.OrderID) {
		return "", errors.New("Order id không hợp lệ")
	}
	for _, id := range []string{row.ConversionID, row.ShopID, row.ItemID, row.ModelID, row.PromotionID} {
		if len(id) > 100 || !exactID.MatchString(id) {
			return "", errors.New("Cần đầy đủ ID nguồn Shopee, không dùng dạng khoa học")
		}
	}
	key, _ := json.Marshal([]string{row.OrderID, row.ConversionID, row.ShopID, row.ItemID, row.ModelID, row.PromotionID})
	return "shopee:" + platform.Hash(string(key)), nil
}
func InsertSignedOrder(ctx context.Context, tx pgx.Tx, s *platform.Store, row *Row) (string, int64, error) {
	a, e := Attribute(ctx, tx, s, row)
	if e != nil {
		return "", 0, e
	}
	return InsertAttributedOrder(ctx, tx, row, a)
}
func InsertAttributedOrder(ctx context.Context, tx pgx.Tx, row *Row, a Attribution) (string, int64, error) {
	var e error
	if e = platform.LockTracking(ctx, tx, a.User, row.Tracking); e != nil {
		return "", 0, e
	}
	cash, e := cashback.AmountRoundedUp(row.Commission, a.EffectiveBps)
	if e != nil {
		return "", 0, e
	}
	next := "pending"
	if row.Status == "rejected" {
		cash = 0
		next = "rejected"
	}
	ids, _ := json.Marshal(row.SubIDs)
	report, _ := json.Marshal(row.ReportMetadata)
	var id string
	e = tx.QueryRow(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,ordered_at,source_status,tier_code,share_bps,status,tracking_code,tracking_sub_ids,link_created_at,link_expires_at,cashback_mode,source_report) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,'signed_link',$20) RETURNING id::text`, a.User, a.Policy, row.Channel, row.Publisher, row.OrderID, row.LineID, row.Name, row.Value, row.Commission, cash, row.Date, row.Status, a.Claims.Tier, a.EffectiveBps, next, row.Tracking, ids, a.Claims.CreatedAt, a.Claims.ExpiresAt(), report).Scan(&id)
	return id, cash, e
}
