package rewards

import (
	"context"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
	"time"
)

type Service struct{ Store *platform.Store }

func Coins(ctx context.Context, tx pgx.Tx, user, ref, description string, delta int64) error {
	tag, e := tx.Exec(ctx, `UPDATE coin_accounts SET balance=balance+$2 WHERE user_id=$1 AND balance+$2>=0`, user, delta)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return platform.Fail(409, "INSUFFICIENT_COINS", "Xu không đủ.")
	}
	_, e = tx.Exec(ctx, `INSERT INTO coin_transactions(user_id,reference,amount,description) VALUES($1,$2,$3,$4)`, user, ref, delta, description)
	return e
}
func (s *Service) Checkin(ctx context.Context, user string) (any, error) {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
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
	if _, e = tx.Exec(ctx, `INSERT INTO checkins VALUES($1,$2,$3,$4)`, user, day, next, award); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(ctx, `UPDATE coin_accounts SET streak=$2,best=greatest(best,$2),last_day=$3 WHERE user_id=$1`, user, next, day); e != nil {
		return nil, e
	}
	if e = Coins(ctx, tx, user, "checkin:"+user+":"+day, "Điểm danh", int64(award)); e != nil {
		return nil, e
	}
	return map[string]any{"streak": next, "award": award, "day": day}, tx.Commit(ctx)
}
func (s *Service) Exchange(ctx context.Context, user, key string, n int64) (any, error) {
	amount, e := ExchangeAmount(n)
	if e != nil {
		return nil, platform.Fail(422, "INVALID_COINS", e.Error())
	}
	return s.Store.Action(ctx, user, key, "coin-exchange", map[string]int64{"coins": n}, func(tx pgx.Tx) (any, error) {
		var enabled bool
		if e := tx.QueryRow(ctx, `SELECT coalesce((settings->>'coinExchangeEnabled')::boolean,false) FROM app_settings`).Scan(&enabled); e != nil {
			return nil, e
		}
		if !enabled {
			return nil, platform.Fail(409, "EXCHANGE_DISABLED", "Đổi xu sang tiền chưa được bật.")
		}
		if _, e := tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); e != nil {
			return nil, e
		}
		ref := "exchange:" + user + ":" + key
		if e := Coins(ctx, tx, user, ref, "Đổi xu thành tiền", -n); e != nil {
			return nil, e
		}
		e := wallet.Credit(ctx, tx, user, ref, "Đổi xu thành tiền", amount)
		return map[string]int64{"amount": amount, "coins": n}, e
	})
}
func (s *Service) Redeem(ctx context.Context, user, key, gift string) (any, error) {
	return s.Store.Action(ctx, user, key, "gift-redeem", map[string]string{"giftId": gift}, func(tx pgx.Tx) (any, error) {
		var cost int64
		var stock int
		var active bool
		e := tx.QueryRow(ctx, `SELECT cost,stock,active FROM gift_catalog WHERE id=$1 FOR UPDATE`, gift).Scan(&cost, &stock, &active)
		if e != nil {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có quà này.")
		}
		if !active || stock < 1 {
			return nil, platform.Fail(409, "OUT_OF_STOCK", "Quà hiện chưa có mã trong kho.")
		}
		var id string
		e = tx.QueryRow(ctx, `INSERT INTO gift_redemptions(user_id,gift_id,cost) VALUES($1,$2,$3) RETURNING id::text`, user, gift, cost).Scan(&id)
		if e != nil {
			return nil, platform.Conflict(e)
		}
		if e = Coins(ctx, tx, user, "gift_hold:"+id, "Giữ xu đổi quà", -cost); e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE gift_catalog SET stock=stock-1 WHERE id=$1`, gift)
		return map[string]string{"id": id, "status": "pending"}, e
	})
}
func (s *Service) GiftEvent(ctx context.Context, actor, id, key, action, code, reason string) (any, error) {
	p := map[string]string{"action": action, "code": code, "reason": reason}
	return s.Store.Action(ctx, actor, key, "gift-event:"+id, p, func(tx pgx.Tx) (any, error) {
		var user, gift, st string
		var cost int64
		e := tx.QueryRow(ctx, `SELECT user_id::text,gift_id,status,cost FROM gift_redemptions WHERE id=$1 FOR UPDATE`, id).Scan(&user, &gift, &st, &cost)
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
			_, e = tx.Exec(ctx, `INSERT INTO notifications(recipient_id,title,body) VALUES($1,'Voucher đã sẵn sàng','Mở Đổi quà để xem mã voucher của bạn.')`, user)
		} else if action == "rejected" {
			if !platform.Text(reason, 3, 500) {
				return nil, platform.Fail(422, "REASON_REQUIRED", "Cần lý do.")
			}
			next = "rejected"
			e = Coins(ctx, tx, user, "gift_refund:"+id, "Hoàn xu đổi quà", cost)
			if e == nil {
				_, e = tx.Exec(ctx, `UPDATE gift_catalog SET stock=stock+1 WHERE id=$1`, gift)
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
