package rewards

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
	"time"
)

type Service struct{ Store *platform.Store }

func lockSystem(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`)
	return e
}
func (s *Service) Checkin(ctx context.Context, user string) (any, error) {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = lockSystem(ctx, tx); e != nil {
		return nil, e
	}
	var last string
	var st int
	e = tx.QueryRow(ctx, `SELECT coalesce(last_day::text,''),streak FROM coin_accounts WHERE user_id=$1 FOR UPDATE`, user).Scan(&last, &st)
	if e != nil {
		return nil, e
	}
	day := LocalDay(time.Now())
	next, award, e := NextCheckin(last, st, day)
	if e != nil {
		return nil, platform.Fail(409, "ALREADY_CHECKED_IN", "Bạn đã điểm danh hôm nay.")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO checkins(user_id,day,streak,award,award_xu) VALUES($1,$2,$3,$4::integer,$4::integer::bigint)`, user, day, next, award); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE coin_accounts SET streak=$2,best=greatest(best,$2),last_day=$3 WHERE user_id=$1`, user, next, day); e != nil {
		return nil, e
	}
	if e = wallet.Credit(ctx, tx, user, "checkin:"+user+":"+day, "Điểm danh", int64(award)); e != nil {
		return nil, e
	}
	var available int64
	if e = tx.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, user).Scan(&available); e != nil {
		return nil, e
	}
	return map[string]any{"streak": next, "awardXu": award, "available": available, "day": day}, tx.Commit(ctx)
}
func (s *Service) Exchange(ctx context.Context, user, key string, n int64) (any, error) {
	return nil, platform.Fail(410, "COINS_ALREADY_UNIFIED", "Xu đã được nhập vào ví chung.")
}

func (s *Service) Redeem(ctx context.Context, user, key, gift string) (any, error) {
	return s.RedeemQuoted(ctx, user, key, gift, nil)
}

// RedeemQuoted locks the catalog price before reserving Xu. Older API clients
// without a quote retain their existing behavior.
func (s *Service) RedeemQuoted(ctx context.Context, user, key, gift string, expectedCost *int64) (any, error) {
	if expectedCost != nil && (*expectedCost <= 0 || *expectedCost > 1000000000000) {
		return nil, platform.Fail(422, "VALIDATION_ERROR", "Giá đổi quà không hợp lệ.")
	}
	payload := map[string]any{"giftId": gift}
	if expectedCost != nil {
		payload["expectedCostXu"] = *expectedCost
	}
	return s.Store.Action(ctx, user, key, "gift-redeem", payload, func(tx pgx.Tx) (any, error) {
		if e := lockSystem(ctx, tx); e != nil {
			return nil, e
		}
		var cost int64
		var stock int
		var active bool
		e := tx.QueryRow(ctx, `SELECT cost,stock,active FROM gift_catalog WHERE id=$1 FOR UPDATE`, gift).Scan(&cost, &stock, &active)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có quà này.")
		}
		if e != nil {
			return nil, e
		}
		if !active || stock < 1 {
			return nil, platform.Fail(409, "OUT_OF_STOCK", "Quà hiện chưa có mã trong kho.")
		}
		if expectedCost != nil && cost != *expectedCost {
			return nil, platform.Fail(409, "GIFT_PRICE_CHANGED", "Giá đổi quà đã thay đổi. Vui lòng kiểm tra lại.")
		}
		var id string
		e = tx.QueryRow(ctx, `INSERT INTO gift_redemptions(user_id,gift_id,cost,cost_xu,cost_unit) VALUES($1,$2,$3,$3,'xu') RETURNING id::text`, user, gift, cost).Scan(&id)
		if e != nil {
			return nil, platform.Conflict(e)
		}
		if e = wallet.Post(ctx, tx, "gift_hold:"+id, "Giữ Xu đổi quà", []wallet.Entry{{User: user, Kind: "available", Amount: -cost}, {User: user, Kind: "gift_held", Amount: cost}}); e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE gift_catalog SET stock=stock-1 WHERE id=$1`, gift)
		return map[string]any{"id": id, "status": "pending", "costXu": cost}, e
	})
}
func (s *Service) GiftEvent(ctx context.Context, actor, id, key, action, code, reason string) (any, error) {
	p := map[string]string{"action": action, "code": code, "reason": reason}
	return s.Store.Action(ctx, actor, key, "gift-event:"+id, p, func(tx pgx.Tx) (any, error) {
		if e := lockSystem(ctx, tx); e != nil {
			return nil, e
		}
		var user, gift, st string
		var cost int64
		e := tx.QueryRow(ctx, `SELECT user_id::text,gift_id,status,cost_xu FROM gift_redemptions WHERE id=$1 FOR UPDATE`, id).Scan(&user, &gift, &st, &cost)
		if e != nil {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có yêu cầu.")
		}
		if st != "pending" {
			return nil, platform.Fail(409, "INVALID_TRANSITION", "Yêu cầu đã xử lý.")
		}
		next := ""
		cipher := ""
		if action == "completed" {
			if !platform.Text(code, 3, 500) {
				return nil, platform.Fail(422, "VOUCHER_REQUIRED", "Cần mã voucher.")
			}
			next = "completed"
			cipher = s.Store.Encrypt(code)
			if e = wallet.Post(ctx, tx, "gift_paid:"+id, "Đã cấp voucher", []wallet.Entry{{User: user, Kind: "gift_held", Amount: -cost}, {Kind: "system", Amount: cost}}); e != nil {
				return nil, e
			}
			_, e = tx.Exec(ctx, `INSERT INTO notifications(recipient_id,title,body) VALUES($1,'Voucher đã sẵn sàng','Mở Lịch sử đổi quà để xem mã voucher của bạn.')`, user)
		} else if action == "rejected" {
			if !platform.Text(reason, 3, 500) {
				return nil, platform.Fail(422, "REASON_REQUIRED", "Cần lý do.")
			}
			next = "rejected"
			e = wallet.Post(ctx, tx, "gift_refund:"+id, "Hoàn Xu đổi quà", []wallet.Entry{{User: user, Kind: "gift_held", Amount: -cost}, {User: user, Kind: "available", Amount: cost}})
			if e == nil {
				_, e = tx.Exec(ctx, `UPDATE gift_catalog SET stock=stock+1 WHERE id=$1`, gift)
			}
			if e == nil {
				_, e = tx.Exec(ctx, `INSERT INTO notifications(recipient_id,title,body) VALUES($1,'Yêu cầu đổi quà đã bị từ chối','Xu đã được hoàn vào ví. Xem lý do trong Lịch sử đổi quà.')`, user)
			}
		} else {
			return nil, platform.Fail(422, "INVALID_ACTION", "Thao tác không hợp lệ.")
		}
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE gift_redemptions SET status=$2,voucher_cipher=nullif($3,''),reason=$4 WHERE id=$1`, id, next, cipher, reason)
		if e != nil {
			return nil, e
		}
		e = platform.Audit(ctx, tx, actor, "gift_"+next, id, map[string]string{"reason": reason})
		return map[string]string{"id": id, "status": next}, e
	})
}
