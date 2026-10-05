package orders

import (
	"context"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
)

type Service struct{ Store *platform.Store }
type Event struct {
	Action     string `json:"action"`
	Reason     string `json:"reason"`
	Commission int64  `json:"commission"`
}

func (s *Service) Event(ctx context.Context, actor, id, key string, p Event) (any, error) {
	return s.Store.Action(ctx, actor, key, "order-event:"+id, p, func(tx pgx.Tx) (any, error) {
		var user, status, sourceStatus string
		var cash, commission int64
		var bps int
		e := tx.QueryRow(ctx, `SELECT user_id::text,status,cashback,commission,source_status,share_bps FROM orders WHERE id=$1 FOR UPDATE`, id).Scan(&user, &status, &cash, &commission, &sourceStatus, &bps)
		if e != nil {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có đơn.")
		}
		next := status
		switch p.Action {
		case "approved", "rejected":
			if p.Action == "approved" && sourceStatus != "approved" {
				return nil, platform.Fail(409, "SOURCE_NOT_APPROVED", "Báo cáo sàn chưa duyệt hoa hồng; chưa thể cộng ví.")
			}
			if status != "pending" {
				return nil, platform.Fail(409, "INVALID_TRANSITION", "Đơn không còn chờ duyệt.")
			}
			next = p.Action
			if next == "approved" {
				e = wallet.Credit(ctx, tx, user, "order_credit:"+id, "Hoàn tiền đơn hàng", cash)
			} else if !platform.Text(p.Reason, 3, 500) {
				return nil, platform.Fail(422, "REASON_REQUIRED", "Cần lý do từ chối.")
			}
		case "adjustment":
			if status != "approved" || p.Commission < 0 || p.Commission > 1e12 || !platform.Text(p.Reason, 3, 500) {
				return nil, platform.Fail(422, "INVALID_ADJUSTMENT", "Điều chỉnh cần đơn đã duyệt, số hoa hồng hợp lệ và lý do.")
			}
			newCash, err := cashback.Amount(p.Commission, bps)
			if err != nil {
				return nil, err
			}
			delta := newCash - cash
			if delta > 0 {
				e = wallet.Credit(ctx, tx, user, "order_adjust:"+id+":"+key, "Điều chỉnh hoàn tiền", delta)
			} else if delta < 0 {
				if _, e = tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); e != nil {
					return nil, e
				}
				var available int64
				if e = tx.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available' FOR UPDATE`, user).Scan(&available); e != nil {
					return nil, e
				}
				take := min(available, -delta)
				debt := -delta - take
				entries := []wallet.Entry{{User: user, Kind: "available", Amount: -take}, {Kind: "system", Amount: take}}
				if debt > 0 {
					entries = append(entries, wallet.Entry{User: user, Kind: "debt", Amount: debt}, wallet.Entry{Kind: "system", Amount: -debt})
				}
				e = wallet.Post(ctx, tx, "order_adjust:"+id+":"+key, "Điều chỉnh hoàn tiền", entries)
			}
			cash, commission = newCash, p.Commission
		default:
			return nil, platform.Fail(422, "INVALID_ACTION", "Thao tác không hợp lệ.")
		}
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `UPDATE orders SET status=$2,cashback=$3,commission=$4,approved_at=CASE WHEN $2='approved' THEN coalesce(approved_at,now()) ELSE approved_at END WHERE id=$1`, id, next, cash, commission)
		if e != nil {
			return nil, e
		}
		_, e = tx.Exec(ctx, `INSERT INTO order_events(order_id,actor_id,action,reason,payload) VALUES($1,$2,$3,$4,jsonb_build_object('commission',$5::bigint,'cashback',$6::bigint,'shareBps',$7::integer))`, id, actor, p.Action, p.Reason, commission, cash, bps)
		if e != nil {
			return nil, e
		}
		e = platform.Audit(ctx, tx, actor, "order_"+p.Action, id, p)
		return map[string]any{"id": id, "status": next, "cashback": cash}, e
	})
}
