package orders

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"hoanxu/internal/tracking"
	"hoanxu/internal/wallet"
	"time"
)

type Service struct{ Store *platform.Store }
type Event struct {
	Action     string `json:"action"`
	Reason     string `json:"reason"`
	Commission int64  `json:"commission"`
}

func (s *Service) Event(ctx context.Context, actor, id, key string, p Event) (any, error) {
	return s.Store.Action(ctx, actor, key, "order-event:"+id, p, func(tx pgx.Tx) (any, error) {
		var lockUser, lockCode string
		if err := tx.QueryRow(ctx, `SELECT user_id::text,coalesce(tracking_code,'') FROM orders WHERE id=$1`, id).Scan(&lockUser, &lockCode); err != nil {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có đơn.")
		}
		if err := platform.LockTracking(ctx, tx, lockUser, lockCode); err != nil {
			return nil, err
		}
		var user, status, sourceStatus string
		var cash, commission int64
		var bps int
		var mode, publisher, policyID string

		var issued, expires *time.Time
		var at time.Time
		var subIDs []byte
		var internalRejection bool
		e := tx.QueryRow(ctx, `SELECT user_id::text,status,cashback,commission,source_status,share_bps,cashback_mode,publisher,ordered_at,link_created_at,link_expires_at,tracking_sub_ids,policy_id::text FROM orders WHERE id=$1 FOR UPDATE`, id).Scan(&user, &status, &cash, &commission, &sourceStatus, &bps, &mode, &publisher, &at, &issued, &expires, &subIDs, &policyID)
		if e != nil {
			return nil, platform.Fail(404, "NOT_FOUND", "Không có đơn.")
		}
		if e = tx.QueryRow(ctx, `SELECT internally_rejected FROM orders WHERE id=$1`, id).Scan(&internalRejection); e != nil {
			return nil, e
		}
		next := status
		switch p.Action {
		case "approved", "rejected":
			if p.Action == "approved" && mode == "signed_link" {
				var ids [5]string
				if json.Unmarshal(subIDs, &ids) != nil {
					return nil, platform.Fail(409, "INVALID_TRACKING", "Thiếu tracking đối soát.")
				}
				claims, err := tracking.Verify(ids, publisher, s.Store.SignTracking)
				if err != nil || issued == nil || expires == nil || !claims.CreatedAt.Equal(*issued) || !claims.ExpiresAt().Equal(*expires) || !claims.Eligible(at) {
					return nil, platform.Fail(409, "LINK_INELIGIBLE", "Đơn không đủ điều kiện thời hạn hoặc chữ ký hoàn Xu.")
				}
				var tax int
				if err = tx.QueryRow(ctx, `SELECT tax_bps FROM cashback_policies WHERE tracking_version=$1 AND id=$2`, claims.Policy, policyID).Scan(&tax); err != nil {
					return nil, err
				}
				effective, err := cashback.EffectiveRate(claims.Bps, tax)
				expected, amountErr := cashback.AmountRoundedUp(commission, effective)
				if err != nil || amountErr != nil || effective != bps || !tracking.MatchesFactor(ids[3], (cashback.LinkRate{EffectiveBps: effective}).Factor()) || expected != cash {
					return nil, platform.Fail(409, "INVALID_TRACKING", "Tỷ lệ hoặc tiền hoàn không khớp đối soát.")
				}
				var matches bool
				if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND tracking_code=$2 AND role='customer' AND NOT blocked)`, user, ids[0]).Scan(&matches); err != nil {
					return nil, err
				}
				if !matches {
					return nil, platform.Fail(409, "INVALID_CUSTOMER", "Khách đối soát không hợp lệ.")
				}
			}
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
			if next == "rejected" {
				internalRejection = true
			}
		case "reopened":
			if status != "rejected" || !internalRejection || sourceStatus == "rejected" {
				return nil, platform.Fail(409, "INVALID_TRANSITION", "Chỉ mở lại đơn bị quản trị từ chối khi nguồn sàn còn hợp lệ.")
			}
			if !platform.Text(p.Reason, 3, 500) {
				return nil, platform.Fail(422, "REASON_REQUIRED", "Cần lý do mở lại đơn.")
			}
			cash, e = cashback.OrderAmount(commission, bps, mode)
			next, internalRejection = "pending", false
		case "adjustment":
			if status != "approved" || p.Commission < 0 || p.Commission > 1e12 || !platform.Text(p.Reason, 3, 500) {
				return nil, platform.Fail(422, "INVALID_ADJUSTMENT", "Điều chỉnh cần đơn đã duyệt, số hoa hồng hợp lệ và lý do.")
			}
			newCash, err := cashback.OrderAmount(p.Commission, bps, mode)
			if err != nil {
				return nil, err
			}
			if mode == "signed_link" && sourceStatus == "rejected" {
				newCash = 0
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
		_, e = tx.Exec(ctx, `UPDATE orders SET status=$2,cashback=$3,commission=$4,internally_rejected=$5,approved_at=CASE WHEN $2='approved' THEN coalesce(approved_at,now()) ELSE approved_at END WHERE id=$1`, id, next, cash, commission, internalRejection)
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
