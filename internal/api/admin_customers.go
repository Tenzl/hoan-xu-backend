package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
)

// Classification is derived from email, including historical whitespace-only values.
const customerKindSQL = `CASE WHEN u.email ~ '[^[:space:]]' THEN 'new' ELSE 'legacy' END`
const newCustomerSQL = `u.role='customer' AND u.email ~ '[^[:space:]]'`
const customerSQL = `WITH periods AS (SELECT 'week' AS period,date_trunc('week',now() AT TIME ZONE 'Asia/Ho_Chi_Minh') AT TIME ZONE 'Asia/Ho_Chi_Minh' AS starts UNION ALL SELECT 'month',date_trunc('month',now() AT TIME ZONE 'Asia/Ho_Chi_Minh') AT TIME ZONE 'Asia/Ho_Chi_Minh'), customer_totals AS (
 SELECT p.period,v.id,sum(o.cashback)::bigint AS xu,count(*) AS orders FROM periods p JOIN orders o ON o.approved_at>=p.starts AND o.approved_at<=now() JOIN users v ON v.id=o.user_id WHERE o.status='approved' AND v.role='customer' AND NOT v.blocked GROUP BY p.period,v.id HAVING sum(o.cashback)>0
), customer_ranks AS (SELECT *,row_number() OVER(PARTITION BY period ORDER BY xu DESC,orders DESC,id) AS rank FROM customer_totals)
SELECT jsonb_build_object('id',u.id,'name',u.name,'email',u.email,'role',u.role,'weekRank',(SELECT rank FROM customer_ranks WHERE period='week' AND id=u.id),'monthRank',(SELECT rank FROM customer_ranks WHERE period='month' AND id=u.id),'kind',` + customerKindSQL + `,'blocked',u.blocked,'goldTotal',coalesce((SELECT gold_total FROM wallet_user_totals WHERE user_id=u.id),0),'goldUsed',coalesce((SELECT gold_used FROM wallet_user_totals WHERE user_id=u.id),0),'goldAvailable',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='available'),0),'goldHeld',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='held'),0),'goldGiftHeld',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='gift_held'),0),'goldDebt',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='debt'),0),'greenAvailable',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='green_available'),0),'greenGiftHeld',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='green_gift_held'),0),'trackingCode',u.tracking_code,'createdAt',u.created_at,'available',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='available'),0),'held',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='held'),0),'giftHeld',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='gift_held'),0)) FROM users u`

const newCustomerDashboardSQL = `WITH new_customers AS (SELECT u.id FROM users u WHERE ` + newCustomerSQL + `)
SELECT jsonb_build_object(
 'commission',coalesce(sum(o.commission) FILTER(WHERE o.status='approved'),0),
 'cashback',coalesce(sum(o.cashback) FILTER(WHERE o.status='approved'),0),
 'retained',coalesce(sum(o.commission-o.cashback) FILTER(WHERE o.status='approved'),0),
 'taxAmount',CASE WHEN count(*) FILTER(WHERE o.status='approved' AND NOT (` + orderCommissionKnownSQL + `))=0 THEN coalesce(sum(` + orderTaxSQL + `) FILTER(WHERE o.status='approved'),0) END,
 'projectedProfit',CASE WHEN count(*) FILTER(WHERE o.status='approved' AND NOT (` + orderCommissionKnownSQL + `))=0 THEN coalesce(sum(o.commission-o.cashback-(` + orderTaxSQL + `)) FILTER(WHERE o.status='approved'),0) END,
 'cashProfit',CASE WHEN count(*) FILTER(WHERE o.status='approved' AND NOT (` + orderCommissionKnownSQL + `))=0 THEN coalesce(sum(o.commission-(` + orderTaxSQL + `)) FILTER(WHERE o.status='approved'),0)-(SELECT coalesce(sum(w.amount),0) FROM withdrawals w JOIN new_customers n ON n.id=w.user_id WHERE w.status='paid') END,
 'profitUnavailableOrders',count(*) FILTER(WHERE o.status='approved' AND NOT (` + orderCommissionKnownSQL + `)),
 'pendingCommission',coalesce(sum(o.commission) FILTER(WHERE o.status='pending'),0),
 'pendingOrders',count(*) FILTER(WHERE o.status='pending'),
 'users',(SELECT count(*) FROM new_customers),
 'links',(SELECT count(*) FROM affiliate_links l JOIN new_customers n ON n.id=l.user_id),
 'pendingWithdrawals',(SELECT count(*) FROM withdrawals w JOIN new_customers n ON n.id=w.user_id WHERE w.status IN ('pending','processing')),
 'pendingGifts',(SELECT count(*) FROM gift_redemptions g JOIN new_customers n ON n.id=g.user_id WHERE g.status='pending'),
 'paid',(SELECT coalesce(sum(w.amount),0) FROM withdrawals w JOIN new_customers n ON n.id=w.user_id WHERE w.status='paid')
) FROM orders o JOIN new_customers n ON n.id=o.user_id`

func (s *Server) customerReadRoutes(r chi.Router) {
	both := func(recent bool, handler http.HandlerFunc) http.HandlerFunc {
		return s.allowed("users", recent, s.allowed("orders", recent, handler))
	}
	r.Get("/admin/users/{userId}", both(false, func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "userId")
		if !s.customerID(w, r, id) {
			return
		}
		s.one(w, r, customerSQL+` WHERE u.id=$1 AND u.role='customer'`, id)
	}))
	r.Get("/admin/users/{userId}/orders", both(false, func(w http.ResponseWriter, r *http.Request) {
		id := chi.URLParam(r, "userId")
		if !s.customerID(w, r, id) {
			return
		}
		status := r.URL.Query().Get("status")
		if status != "" && status != "pending" && status != "approved" && status != "rejected" {
			s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_STATUS", "Trạng thái đơn không hợp lệ."))
			return
		}
		if _, err := s.Store.One(r.Context(), `SELECT to_jsonb(id) FROM users WHERE id=$1 AND role='customer'`, id); err != nil {
			s.reply(w, r, 0, nil, err)
			return
		}
		limit, offset := page(r)
		s.list(w, r, adminOrderSQL+` WHERE o.user_id=$1 AND ($4='' OR o.status=$4) ORDER BY o.ordered_at DESC,o.id DESC LIMIT $2 OFFSET $3`, id, limit, offset, status)
	}))
	r.Get("/admin/users/{userId}/orders/{orderId}", both(false, func(w http.ResponseWriter, r *http.Request) {
		id, order := chi.URLParam(r, "userId"), chi.URLParam(r, "orderId")
		if !s.customerID(w, r, id) || !s.customerID(w, r, order) {
			return
		}
		s.one(w, r, adminOrderSQL+` WHERE o.id=$1 AND o.user_id=$2 AND u.role='customer'`, order, id)
	}))
	r.Patch("/admin/users/{userId}/name", both(true, s.renameLegacyCustomer))
	r.Post("/admin/users/{userId}/orders", both(true, s.addLegacyCustomerOrder))
	r.Post("/admin/users/{userId}/orders/batch", both(true, s.addLegacyCustomerOrderBatch))
}

func (s *Server) addLegacyCustomerOrderBatch(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "userId")
	if !s.customerID(w, r, id) {
		return
	}
	var body struct {
		Orders []json.RawMessage `json:"orders"`
	}
	if !s.body(w, r, &body) {
		return
	}
	if len(body.Orders) < 1 || len(body.Orders) > 100 {
		s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_ORDER_BATCH", "Nhập từ 1–100 đơn mỗi lần."))
		return
	}
	orders := make([]legacyOrderInput, len(body.Orders))
	now := time.Now()
	for i, raw := range body.Orders {
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(raw, &fields)
		d := json.NewDecoder(bytes.NewReader(raw))
		d.DisallowUnknownFields()
		err := d.Decode(&orders[i])
		p := &orders[i]
		p.ProductName = strings.TrimSpace(p.ProductName)
		p.Note = strings.TrimSpace(p.Note)
		field := ""
		for key, value := range fields {
			if key != "productName" && key != "orderedAt" && key != "cashback" && key != "note" {
				field = key
				break
			}
			if bytes.Equal(value, []byte("null")) {
				field = key
				break
			}
		}
		switch {
		case field != "":
		case err != nil:
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) && typeErr.Field != "" {
				field = typeErr.Field
			} else {
				field = "orderedAt / JSON"
			}
		case !platform.Text(p.ProductName, 1, 200):
			field = "productName (1–200 ký tự)"
		case p.OrderedAt.IsZero() || p.OrderedAt.After(now):
			field = "orderedAt (không trong tương lai)"
		case p.Cashback < 1 || p.Cashback > 1e12:
			field = "cashback (1–1.000.000.000.000 Xu)"
		case !platform.Text(p.Note, 0, 500):
			field = "note (tối đa 500 ký tự)"
		}
		if field != "" {
			message := localMessage(r, "Đơn {index}: trường {field} không hợp lệ.")
			message = strings.ReplaceAll(strings.ReplaceAll(message, "{index}", fmt.Sprint(i+1)), "{field}", localMessage(r, field))
			s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_ORDER_BATCH", message))
			return
		}
	}
	if err := requireLegacyCustomer(r.Context(), s.Store.Pool, id, false); err != nil {
		s.reply(w, r, 0, nil, err)
		return
	}
	v, err := s.Store.Action(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), "legacy-order-batch:"+id, orders, func(tx pgx.Tx) (any, error) {
		created := make([]any, 0, len(orders))
		var total int64
		for _, p := range orders {
			item, err := s.createLegacyOrder(r.Context(), tx, user(r).ID, id, p)
			if err != nil {
				return nil, err
			}
			created = append(created, item)
			total += p.Cashback
		}
		return map[string]any{"orders": created, "totalCashback": total}, nil
	})
	s.reply(w, r, 201, v, err)
}

func (s *Server) customerID(w http.ResponseWriter, r *http.Request, id string) bool {
	if platform.ID(id) {
		return true
	}
	s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_ID", "ID không hợp lệ."))
	return false
}

type customerQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func requireLegacyCustomer(ctx context.Context, q customerQuerier, id string, lock bool) error {
	query := `SELECT ` + customerKindSQL + ` FROM users u WHERE u.id=$1 AND u.role='customer'`
	if lock {
		query += ` FOR UPDATE`
	}
	var kind string
	err := q.QueryRow(ctx, query, id).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return platform.Fail(404, "NOT_FOUND", "Không có khách này.")
	}
	if err != nil {
		return err
	}
	if kind != "legacy" {
		return platform.Fail(403, "CUSTOMER_READ_ONLY", "Người dùng mới chỉ được xem tại mục quản lý người dùng.")
	}
	return nil
}

func (s *Server) renameLegacyCustomer(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "userId")
	if !s.customerID(w, r, id) {
		return
	}
	var p struct {
		Name string `json:"name"`
	}
	if !s.body(w, r, &p) {
		return
	}
	p.Name = strings.TrimSpace(p.Name)
	if !platform.Text(p.Name, 1, 80) {
		s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_NAME", "Tên phải có từ 1–80 ký tự."))
		return
	}
	tx, err := s.Store.Pool.Begin(r.Context())
	if err != nil {
		s.reply(w, r, 0, nil, err)
		return
	}
	defer tx.Rollback(r.Context())
	err = requireLegacyCustomer(r.Context(), tx, id, true)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE users SET name=$2 WHERE id=$1`, id, p.Name)
	}
	if err == nil {
		err = platform.Audit(r.Context(), tx, user(r).ID, "legacy_customer_renamed", id, p)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	s.reply(w, r, 200, p, err)
}

type legacyOrderInput struct {
	ProductName string    `json:"productName"`
	OrderedAt   time.Time `json:"orderedAt"`
	Cashback    int64     `json:"cashback"`
	Note        string    `json:"note"`
}

func (s *Server) addLegacyCustomerOrder(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "userId")
	if !s.customerID(w, r, id) {
		return
	}
	var p legacyOrderInput
	if !s.body(w, r, &p) {
		return
	}
	p.ProductName, p.Note = strings.TrimSpace(p.ProductName), strings.TrimSpace(p.Note)
	if !platform.Text(p.ProductName, 1, 200) || !platform.Text(p.Note, 0, 500) || p.Cashback < 1 || p.Cashback > 1e12 || p.OrderedAt.IsZero() || p.OrderedAt.After(time.Now()) {
		s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_LEGACY_ORDER", "Nhập sản phẩm, ngày đặt không trong tương lai và số Xu hoàn hợp lệ."))
		return
	}
	if err := requireLegacyCustomer(r.Context(), s.Store.Pool, id, false); err != nil {
		s.reply(w, r, 0, nil, err)
		return
	}
	v, err := s.Store.Action(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), "legacy-order:"+id, p, func(tx pgx.Tx) (any, error) {
		return s.createLegacyOrder(r.Context(), tx, user(r).ID, id, p)
	})
	s.reply(w, r, 201, v, err)
}

func (s *Server) createLegacyOrder(ctx context.Context, tx pgx.Tx, actor, id string, p legacyOrderInput) (any, error) {
	// Follow the wallet lock order before touching the customer and its accounts.
	if _, err := tx.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); err != nil {
		return nil, err
	}
	if err := requireLegacyCustomer(ctx, tx, id, true); err != nil {
		return nil, err
	}
	if err := platform.Accounts(ctx, tx, id); err != nil {
		return nil, err
	}
	var policy string
	err := tx.QueryRow(ctx, `SELECT id::text FROM cashback_policies WHERE mode='fixed' AND share_percent=100 ORDER BY created_at,id LIMIT 1`).Scan(&policy)
	if errors.Is(err, pgx.ErrNoRows) {
		// Fixed policies do not participate in CurrentCashbackPolicy (tiered only).
		err = tx.QueryRow(ctx, `INSERT INTO cashback_policies(share_percent,mode,created_at) VALUES(100,'fixed','1970-01-01T00:00:00Z') RETURNING id::text`).Scan(&policy)
	}
	if err != nil {
		return nil, err
	}
	var order string
	err = tx.QueryRow(ctx, `WITH generated AS (SELECT gen_random_uuid() AS id) INSERT INTO orders(id,user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,share_bps,ordered_at,approved_at) SELECT id,$1,$2,'shopee','admin-legacy','MANUAL-'||id::text,'1',$3,0,$4,$4,'approved','approved',10000,$5,now() FROM generated RETURNING orders.id::text`, id, policy, p.ProductName, p.Cashback, p.OrderedAt).Scan(&order)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_items(order_id,quantity) VALUES($1,1)`, order); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_events(order_id,actor_id,action,reason,payload) VALUES($1,$2,'approved','Nhập tay cho người dùng cũ',jsonb_build_object('origin','admin-legacy','note',$3::text,'cashback',$4::bigint,'commission',$4::bigint,'shareBps',10000))`, order, actor, p.Note, p.Cashback); err != nil {
		return nil, err
	}
	if err = wallet.Credit(ctx, tx, id, "order_credit:"+order, "Hoàn Xu đơn nhập tay cho người dùng cũ", p.Cashback); err != nil {
		return nil, err
	}
	if err = wallet.RefreshGoldTotals(ctx, tx, id); err != nil {
		return nil, err
	}
	if err = platform.Audit(ctx, tx, actor, "legacy_manual_order", order, p); err != nil {
		return nil, err
	}
	return map[string]any{"id": order, "status": "approved", "cashback": p.Cashback}, nil
}
