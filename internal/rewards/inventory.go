package rewards

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
)

type GiftInput struct {
	Name           string `json:"name"`
	Channel        string `json:"channel,omitempty"`
	ImageURL       string `json:"imageUrl,omitempty"`
	ImagePositionY *int   `json:"imagePositionY,omitempty"`
	Description    string `json:"description,omitempty"`
	CostXu         int64  `json:"costXu"`
	Stock          int    `json:"stock"`
	Active         bool   `json:"active"`
	Icon           string `json:"icon"`
}
type Gift struct {
	ID       string `json:"id"`
	CostUnit string `json:"costUnit"`
	Currency string `json:"currency"`
	GiftInput
}
type GiftPatch struct {
	Name           *string `json:"name"`
	Channel        *string `json:"channel"`
	CostXu         *int64  `json:"costXu"`
	Stock          *int    `json:"stock"`
	ExpectedStock  *int    `json:"expectedStock"`
	Active         *bool   `json:"active"`
	Icon           *string `json:"icon"`
	ImageURL       *string `json:"imageUrl,omitempty"`
	ImagePositionY *int    `json:"imagePositionY,omitempty"`
	Description    *string `json:"description,omitempty"`
}

func validGift(p GiftInput) bool {
	channel := p.Channel == "" || p.Channel == "shopee" || p.Channel == "lazada" || p.Channel == "tiktok" || p.Channel == "tiki"
	icon := false
	for _, v := range []string{"gift", "ticket", "shopping-bag", "box", "coffee", "headphones", "star", "heart"} {
		if p.Icon == v {
			icon = true
		}
	}
	position := p.ImagePositionY == nil || (*p.ImagePositionY >= 0 && *p.ImagePositionY <= 100)
	return platform.Text(p.Name, 1, 80) && p.CostXu > 0 && p.CostXu <= 1000000000000 && p.Stock >= 0 && p.Stock <= 100000 && channel && icon && position && len([]rune(p.Description)) <= 2000 && validGiftImage(p.ImageURL)
}
func validGiftImage(value string) bool {
	if value == "" {
		return true
	}
	if len([]rune(value)) > 2048 || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Hostname() != "" && parsed.User == nil
}
func (s *Service) CreateGift(ctx context.Context, actor, key string, p GiftInput) (any, error) {
	p.Name = strings.TrimSpace(p.Name)
	p.ImageURL = strings.TrimSpace(p.ImageURL)
	if p.Icon == "" {
		p.Icon = "gift"
	}
	if !validGift(p) {
		return nil, platform.Fail(422, "VALIDATION_ERROR", "Danh mục quà không hợp lệ.")
	}
	return s.Store.Action(ctx, actor, key, "gift-create", p, func(tx pgx.Tx) (any, error) {
		var id string
		if e := tx.QueryRow(ctx, `INSERT INTO gift_catalog(id,name,channel,cost,stock,active,icon,image_url,description,image_position_y) VALUES(gen_random_uuid()::text,$1,nullif($2,''),$3,$4,$5,$6,$7,$8,coalesce($9,50)) RETURNING id`, p.Name, p.Channel, p.CostXu, p.Stock, p.Active, p.Icon, p.ImageURL, p.Description, p.ImagePositionY).Scan(&id); e != nil {
			return nil, e
		}
		return Gift{ID: id, CostUnit: "xu", Currency: "green", GiftInput: p}, platform.Audit(ctx, tx, actor, "gift_created", id, p)
	})
}
func (s *Service) UpdateGift(ctx context.Context, actor, id, key string, p GiftPatch) (any, error) {
	if p.Name == nil && p.Channel == nil && p.CostXu == nil && p.Stock == nil && p.Active == nil && p.Icon == nil && p.ImageURL == nil && p.Description == nil && p.ImagePositionY == nil {
		return nil, platform.Fail(422, "VALIDATION_ERROR", "Cần chọn thông tin quà để cập nhật.")
	}
	if p.Stock != nil && p.ExpectedStock == nil {
		return nil, platform.Fail(422, "EXPECTED_STOCK_REQUIRED", "Cần tồn kho đã hiển thị để cập nhật số lượng.")
	}
	return s.Store.Action(ctx, actor, key, "gift-update:"+id, p, func(tx pgx.Tx) (any, error) {
		g := Gift{ID: id, CostUnit: "xu", Currency: "green"}
		e := tx.QueryRow(ctx, `SELECT name,coalesce(channel,''),cost,stock,active,icon,image_url,description,image_position_y FROM gift_catalog WHERE id=$1 FOR UPDATE`, id).Scan(&g.Name, &g.Channel, &g.CostXu, &g.Stock, &g.Active, &g.Icon, &g.ImageURL, &g.Description, &g.ImagePositionY)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có quà này.")
		}
		if e != nil {
			return nil, e
		}
		if p.Stock != nil && g.Stock != *p.ExpectedStock {
			return nil, platform.Fail(409, "GIFT_STOCK_CHANGED", "Tồn kho đã thay đổi. Vui lòng tải lại trước khi cập nhật.")
		}
		if p.Name != nil {
			g.Name = strings.TrimSpace(*p.Name)
		}
		if p.Channel != nil {
			g.Channel = *p.Channel
		}
		if p.CostXu != nil {
			g.CostXu = *p.CostXu
		}
		if p.Stock != nil {
			g.Stock = *p.Stock
		}
		if p.Active != nil {
			g.Active = *p.Active
		}
		if p.Icon != nil {
			g.Icon = *p.Icon
		}
		if p.ImageURL != nil {
			g.ImageURL = strings.TrimSpace(*p.ImageURL)
		}
		if p.Description != nil {
			g.Description = *p.Description
		}
		if p.ImagePositionY != nil {
			g.ImagePositionY = p.ImagePositionY
		}
		if !validGift(g.GiftInput) {
			return nil, platform.Fail(422, "VALIDATION_ERROR", "Danh mục quà không hợp lệ.")
		}
		if _, e = tx.Exec(ctx, `UPDATE gift_catalog SET name=$2,channel=nullif($3,''),cost=$4,stock=$5,active=$6,icon=$7,image_url=$8,description=$9,image_position_y=$10 WHERE id=$1`, id, g.Name, g.Channel, g.CostXu, g.Stock, g.Active, g.Icon, g.ImageURL, g.Description, g.ImagePositionY); e != nil {
			return nil, e
		}
		return g, platform.Audit(ctx, tx, actor, "gift_updated", id, p)
	})
}

type OutOfStockResult struct {
	GiftID          string `json:"giftId"`
	Stock           int    `json:"stock"`
	RefundedCount   int    `json:"refundedCount"`
	RefundedXu      int64  `json:"refundedXu"`
	RefundedGoldXu  int64  `json:"refundedGoldXu"`
	RefundedGreenXu int64  `json:"refundedGreenXu"`
}

func (s *Service) OutOfStock(ctx context.Context, actor, id, key string) (any, error) {
	return s.Store.Action(ctx, actor, key, "gift-out-of-stock:"+id, map[string]string{"giftId": id}, func(tx pgx.Tx) (any, error) {
		if e := lockSystem(ctx, tx); e != nil {
			return nil, e
		}
		var stock int
		e := tx.QueryRow(ctx, `SELECT stock FROM gift_catalog WHERE id=$1 FOR UPDATE`, id).Scan(&stock)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có quà này.")
		}
		if e != nil {
			return nil, e
		}
		type pending struct {
			id, user, currency string
			cost               int64
		}
		rows, e := tx.Query(ctx, `SELECT id::text,user_id::text,cost_xu,currency FROM gift_redemptions WHERE gift_id=$1 AND status='pending' ORDER BY id FOR UPDATE`, id)
		if e != nil {
			return nil, e
		}
		items := []pending{}
		for rows.Next() {
			var p pending
			if e = rows.Scan(&p.id, &p.user, &p.cost, &p.currency); e != nil {
				rows.Close()
				return nil, e
			}
			items = append(items, p)
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
		if _, e = tx.Exec(ctx, `UPDATE gift_catalog SET stock=0 WHERE id=$1`, id); e != nil {
			return nil, e
		}
		result := OutOfStockResult{GiftID: id}
		for _, p := range items {
			if e = refundGift(ctx, tx, p.id, p.user, id, p.cost, p.currency, "Hàng đã hết", false); e != nil {
				return nil, e
			}
			if e = platform.Audit(ctx, tx, actor, "gift_refund_out_of_stock", p.id, map[string]string{"reason": "Hàng đã hết", "giftId": id}); e != nil {
				return nil, e
			}
			result.RefundedCount++
			result.RefundedXu += p.cost
			if p.currency == "green" {
				result.RefundedGreenXu += p.cost
			} else {
				result.RefundedGoldXu += p.cost
			}
		}
		return result, platform.Audit(ctx, tx, actor, "gift_out_of_stock", id, result)
	})
}
func formatXu(n int64) string {
	v := strconv.FormatInt(n, 10)
	for i := len(v) - 3; i > 0; i -= 3 {
		v = v[:i] + "." + v[i:]
	}
	return v
}
func notifyGift(ctx context.Context, tx pgx.Tx, user, gift string, cost int64, currency string, completed, outOfStock bool) error {
	var name, giftName string
	if e := tx.QueryRow(ctx, `SELECT u.name,g.name FROM users u CROSS JOIN gift_catalog g WHERE u.id=$1 AND g.id=$2`, user, gift).Scan(&name, &giftName); e != nil {
		return e
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "bạn"
	}
	title := "Yêu cầu đổi quà đã bị từ chối"
	body := fmt.Sprintf("Chào %s, %s đã được hoàn vào ví. Xem lý do trong Lịch sử đổi quà.", name, formatXu(cost)+" Xu"+currencyLabel(currency))
	if completed {
		title = "Voucher đã sẵn sàng"
		body = fmt.Sprintf("Chào %s, voucher %s của bạn đã sẵn sàng. Mở Lịch sử đổi quà để xem mã.", name, giftName)
	} else if outOfStock {
		title = "Hoàn Xu vì hết hàng"
		body = fmt.Sprintf("Chào %s, %s đã hết hàng. %s đã được hoàn vào ví của bạn.", name, giftName, formatXu(cost)+" Xu"+currencyLabel(currency))
	}
	_, e := tx.Exec(ctx, `INSERT INTO notifications(recipient_id,title,body) VALUES($1,$2,$3)`, user, title, body)
	return e
}
func giftAccounts(currency string) (string, string, string) {
	if currency == "green" {
		return "green_available", "green_gift_held", "green_system"
	}
	return "available", "gift_held", "system"
}
func currencyLabel(currency string) string {
	if currency == "green" {
		return " xanh"
	}
	return " vàng"
}
func refundGift(ctx context.Context, tx pgx.Tx, id, user, gift string, cost int64, currency string, reason string, restock bool) error {
	availableKind, heldKind, _ := giftAccounts(currency)
	if e := wallet.Post(ctx, tx, "gift_refund:"+id, "Hoàn Xu đổi quà", []wallet.Entry{{User: user, Kind: heldKind, Amount: -cost}, {User: user, Kind: availableKind, Amount: cost}}); e != nil {
		return e
	}
	if restock {
		if _, e := tx.Exec(ctx, `UPDATE gift_catalog SET stock=stock+1 WHERE id=$1`, gift); e != nil {
			return e
		}
	}
	if _, e := tx.Exec(ctx, `UPDATE gift_redemptions SET status='rejected',voucher_cipher=NULL,reason=$2 WHERE id=$1`, id, reason); e != nil {
		return e
	}
	return notifyGift(ctx, tx, user, gift, cost, currency, false, !restock)
}
