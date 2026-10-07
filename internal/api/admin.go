package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/auth"
	"hoanxu/internal/cashback"
	"hoanxu/internal/community"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"hoanxu/internal/rewards"
	"hoanxu/internal/settings"
	"hoanxu/internal/wallet"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (s *Server) adminRoutes(r chi.Router) {
	r.Post("/admin/browser/verifications", s.allowed("settings", true, s.startShopeeVerification))
	r.Get("/admin/browser/verifications/{id}", s.allowed("settings", false, s.getShopeeVerification))
	r.Get("/admin/browser/settings", s.allowed("settings", false, s.getShopeeSettings))
	r.Put("/admin/browser/settings", s.allowed("settings", true, s.putShopeeSettings))
	r.Get("/admin/cashback-policies/current", s.allowed("settings", false, func(w http.ResponseWriter, r *http.Request) {
		p, e := (&cashback.Service{Store: s.Store}).Current(r.Context())
		s.reply(w, r, 200, p, e)
	}))
	r.Post("/admin/cashback-policies", s.allowed("settings", true, func(w http.ResponseWriter, r *http.Request) {
		var p cashback.Input
		if !s.body(w, r, &p) {
			return
		}
		v, e := (&cashback.Service{Store: s.Store}).Create(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), p)
		s.reply(w, r, 201, v, e)
	}))
	r.Get("/admin/dashboard", s.allowed("audit", false, func(w http.ResponseWriter, r *http.Request) {
		s.one(w, r, `SELECT jsonb_build_object('commission',coalesce(sum(commission) FILTER(WHERE status='approved'),0),'cashback',coalesce(sum(cashback) FILTER(WHERE status='approved'),0),'retained',coalesce(sum(commission-cashback) FILTER(WHERE status='approved'),0),'pendingCommission',coalesce(sum(commission) FILTER(WHERE status='pending'),0),'pendingOrders',count(*) FILTER(WHERE status='pending'),'users',(SELECT count(*) FROM users WHERE role='customer'),'links',(SELECT count(*) FROM affiliate_links),'pendingWithdrawals',(SELECT count(*) FROM withdrawals WHERE status IN ('pending','processing')),'pendingGifts',(SELECT count(*) FROM gift_redemptions WHERE status='pending'),'paid',(SELECT coalesce(sum(amount),0) FROM withdrawals WHERE status='paid')) FROM orders`)
	}))
	r.Get("/admin/orders", s.allowed("orders", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		st := r.URL.Query().Get("status")
		s.list(w, r, orderSQL+` WHERE ($3='' OR o.status=$3) ORDER BY o.created_at DESC,o.id DESC LIMIT $1 OFFSET $2`, l, o, st)
	}))
	r.Post("/admin/orders/{id}/events", s.allowed("orders", true, func(w http.ResponseWriter, r *http.Request) {
		var p orders.Event
		if !s.body(w, r, &p) {
			return
		}
		v, e := (&orders.Service{Store: s.Store}).Event(r.Context(), user(r).ID, chi.URLParam(r, "id"), r.Header.Get("Idempotency-Key"), p)
		s.reply(w, r, 201, v, e)
	}))
	r.Post("/admin/orders", s.allowed("orders", true, s.manualOrder))
	r.Get("/admin/users", s.allowed("users", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		q := r.URL.Query().Get("q")
		s.list(w, r, `SELECT jsonb_build_object('id',u.id,'name',u.name,'email',u.email,'role',u.role,'blocked',u.blocked,'trackingCode',u.tracking_code,'createdAt',u.created_at,'available',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='available'),0),'held',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='held'),0),'giftHeld',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='gift_held'),0)) FROM users u WHERE role='customer' AND ($3='' OR name ILIKE '%'||$3||'%' OR email ILIKE '%'||$3||'%') ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET $2`, l, o, q)
	}))
	r.Patch("/admin/users/{id}", s.allowed("users", true, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Blocked bool   `json:"blocked"`
			Reason  string `json:"reason"`
		}
		if !s.body(w, r, &p) {
			return
		}
		if !platform.Text(p.Reason, 3, 500) {
			s.reply(w, r, 0, nil, platform.Fail(422, "REASON_REQUIRED", "Cần lý do khóa/mở khóa."))
			return
		}
		tx, e := s.Store.Pool.Begin(r.Context())
		if e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		defer tx.Rollback(r.Context())
		tag, e := tx.Exec(r.Context(), `UPDATE users SET blocked=$2 WHERE id=$1 AND role='customer'`, chi.URLParam(r, "id"), p.Blocked)
		if e == nil && tag.RowsAffected() == 0 {
			e = platform.Fail(404, "NOT_FOUND", "Không có khách này.")
		}
		if e == nil {
			_, e = tx.Exec(r.Context(), `DELETE FROM sessions WHERE user_id=$1`, chi.URLParam(r, "id"))
		}
		if e == nil {
			e = platform.Audit(r.Context(), tx, user(r).ID, "customer_blocked", chi.URLParam(r, "id"), p)
		}
		if e == nil {
			e = tx.Commit(r.Context())
		}
		s.reply(w, r, 200, p, e)
	}))
	r.Get("/admin/internal-accounts", s.allowed("internal", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		s.list(w, r, `SELECT jsonb_build_object('id',u.id,'name',u.name,'username',c.username,'role',u.role,'blocked',u.blocked,'mustChangePassword',c.must_change,'permissions',coalesce((SELECT jsonb_agg(permission) FROM user_permissions WHERE user_id=u.id),'[]')) FROM users u JOIN internal_credentials c ON c.user_id=u.id ORDER BY u.created_at DESC LIMIT $1 OFFSET $2`, l, o)
	}))
	r.Post("/admin/internal-accounts", s.allowed("internal", true, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Username    string   `json:"username"`
			Name        string   `json:"name"`
			Password    string   `json:"password"`
			Role        string   `json:"role"`
			Permissions []string `json:"permissions"`
		}
		if !s.body(w, r, &p) {
			return
		}
		id, e := auth.CreateInternal(r.Context(), s.Store, user(r).ID, p.Username, p.Name, p.Password, p.Role, p.Permissions)
		s.reply(w, r, 201, map[string]string{"id": id}, e)
	}))
	r.Post("/admin/internal-accounts/{id}/reset", s.allowed("internal", true, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Password    string   `json:"password"`
			Permissions []string `json:"permissions"`
			Blocked     bool     `json:"blocked"`
		}
		if !s.body(w, r, &p) {
			return
		}
		id := chi.URLParam(r, "id")
		if id == user(r).ID {
			s.reply(w, r, 0, nil, platform.Fail(409, "SELF_RESET_DENIED", "Dùng Đổi mật khẩu cho tài khoản hiện tại."))
			return
		}
		e := s.Auth.ResetInternal(r.Context(), user(r).ID, id, p.Password, p.Permissions, p.Blocked)
		s.reply(w, r, 200, map[string]bool{"reset": e == nil}, e)
	}))
	r.Get("/admin/withdrawals", s.allowed("withdrawals", false, s.withdrawalList(true)))
	r.Post("/admin/withdrawals/{id}/events", s.allowed("withdrawals", true, func(w http.ResponseWriter, r *http.Request) {
		var p wallet.Event
		if !s.body(w, r, &p) {
			return
		}
		v, e := (&wallet.Service{Store: s.Store}).Process(r.Context(), user(r).ID, chi.URLParam(r, "id"), r.Header.Get("Idempotency-Key"), p)
		s.reply(w, r, 201, v, e)
	}))
	r.Get("/admin/gifts", s.allowed("gifts", false, func(w http.ResponseWriter, r *http.Request) {
		s.list(w, r, `SELECT jsonb_build_object('id',id,'name',name,'channel',channel,'costXu',cost,'costUnit','xu','stock',stock,'active',active) FROM gift_catalog ORDER BY id`)
	}))
	r.Patch("/admin/gifts/{id}", s.allowed("gifts", true, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Name   string `json:"name"`
			Cost   int64  `json:"costXu"`
			Stock  int    `json:"stock"`
			Active bool   `json:"active"`
		}
		if !s.body(w, r, &p) {
			return
		}
		if !platform.Text(p.Name, 1, 80) || p.Cost <= 0 || p.Cost > 1000000000000 || p.Stock < 0 || p.Stock > 100000 {
			s.reply(w, r, 0, nil, platform.Fail(422, "VALIDATION_ERROR", "Danh mục quà không hợp lệ."))
			return
		}
		tx, e := s.Store.Pool.Begin(r.Context())
		if e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		defer tx.Rollback(r.Context())
		_, e = tx.Exec(r.Context(), `UPDATE gift_catalog SET name=$2,cost=$3,stock=$4,active=$5 WHERE id=$1`, chi.URLParam(r, "id"), p.Name, p.Cost, p.Stock, p.Active)
		if e == nil {
			e = platform.Audit(r.Context(), tx, user(r).ID, "gift_updated", chi.URLParam(r, "id"), p)
		}
		if e == nil {
			e = tx.Commit(r.Context())
		}
		s.reply(w, r, 200, p, e)
	}))
	r.Get("/admin/gift-redemptions", s.allowed("gifts", false, s.redemptionList(true)))
	r.Post("/admin/gift-redemptions/{id}/events", s.allowed("gifts", true, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Action string `json:"action"`
			Code   string `json:"code"`
			Reason string `json:"reason"`
		}
		if !s.body(w, r, &p) {
			return
		}
		v, e := (&rewards.Service{Store: s.Store}).GiftEvent(r.Context(), user(r).ID, chi.URLParam(r, "id"), r.Header.Get("Idempotency-Key"), p.Action, p.Code, p.Reason)
		s.reply(w, r, 201, v, e)
	}))
	r.Get("/admin/deals", s.allowed("community", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		s.list(w, r, `SELECT jsonb_build_object('id',d.id,'name',u.name,'body',d.body,'channel',d.channel,'hidden',d.hidden,'deleted',d.deleted,'createdAt',d.created_at,'likes',(SELECT count(*) FROM deal_likes WHERE deal_id=d.id)) FROM deals d JOIN users u ON u.id=d.user_id ORDER BY d.created_at DESC LIMIT $1 OFFSET $2`, l, o)
	}))
	r.Post("/admin/deals/{id}/events", s.allowed("community", false, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Action string `json:"action"`
			Reason string `json:"reason"`
		}
		if !s.body(w, r, &p) {
			return
		}
		e := (&community.Service{Store: s.Store}).Moderate(r.Context(), user(r).ID, chi.URLParam(r, "id"), p.Action, p.Reason)
		s.reply(w, r, 201, p, e)
	}))
	r.Get("/admin/notifications", s.allowed("notifications", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		s.list(w, r, `SELECT jsonb_build_object('id',id,'title',title,'body',body,'recipientId',recipient_id,'createdAt',created_at) FROM notifications WHERE NOT deleted ORDER BY created_at DESC LIMIT $1 OFFSET $2`, l, o)
	}))
	r.Post("/admin/notifications", s.allowed("notifications", false, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			RecipientID string `json:"recipientId"`
			Title       string `json:"title"`
			Body        string `json:"body"`
		}
		if !s.body(w, r, &p) {
			return
		}
		v, e := (&community.Service{Store: s.Store}).Notify(r.Context(), user(r).ID, p.RecipientID, p.Title, p.Body)
		s.reply(w, r, 201, v, e)
	}))
	r.Delete("/admin/notifications/{id}", s.allowed("notifications", false, func(w http.ResponseWriter, r *http.Request) {
		tx, e := s.Store.Pool.Begin(r.Context())
		if e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		defer tx.Rollback(r.Context())
		_, e = tx.Exec(r.Context(), `UPDATE notifications SET deleted=true WHERE id=$1`, chi.URLParam(r, "id"))
		if e == nil {
			e = platform.Audit(r.Context(), tx, user(r).ID, "notification_deleted", chi.URLParam(r, "id"), nil)
		}
		if e == nil {
			e = tx.Commit(r.Context())
		}
		s.reply(w, r, 200, map[string]bool{"deleted": true}, e)
	}))
	r.Get("/admin/settings", s.allowed("settings", false, func(w http.ResponseWriter, r *http.Request) {
		s.one(w, r, `SELECT settings-'sharePercent' FROM app_settings`)
	}))
	r.Put("/admin/settings", s.allowed("settings", true, func(w http.ResponseWriter, r *http.Request) {
		var p settings.Input
		if !s.body(w, r, &p) {
			return
		}
		e := (&settings.Service{Store: s.Store}).Update(r.Context(), user(r).ID, p)
		s.reply(w, r, 200, p, e)
	}))
	r.Get("/admin/affiliate-channels", s.allowed("settings", false, func(w http.ResponseWriter, r *http.Request) {
		status := "not_configured"
		if s.Affiliate != nil && s.Affiliate.CheckEnabled() && s.Affiliate.TrackingVerified {
			status = "available"
		}
		s.list(w, r, `SELECT jsonb_build_object('id',id,'name',name,'status',CASE WHEN id='shopee' AND $1::boolean THEN $2::text ELSE status END,'settings',settings-'runtimeConfigs') FROM affiliate_channels ORDER BY id`, s.Affiliate != nil && s.Affiliate.ManagedConfig, status)
	}))
	r.Patch("/admin/affiliate-channels/{id}", s.allowed("settings", true, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Status   string `json:"status"`
			Template string `json:"template"`
		}
		if !s.body(w, r, &p) {
			return
		}
		id := chi.URLParam(r, "id")
		if id != "shopee" {
			s.reply(w, r, 0, nil, platform.Fail(409, "DEMO_ONLY", "Kênh này vẫn giữ mẫu."))
			return
		}
		s.reply(w, r, 0, nil, platform.Fail(410, "SHOPEE_SETTINGS_MOVED", "Cấu hình Shopee đã chuyển sang form Kết nối Shopee."))
	}))
	r.Get("/admin/browser", s.allowed("settings", false, func(w http.ResponseWriter, r *http.Request) {
		v := map[string]any{"enabled": s.Affiliate.CheckEnabled(), "trackingVerified": s.Affiliate.TrackingVerified, "remoteAvailable": s.RemoteBrowser != nil && s.BrowserMode != "local" && user(r).Role == "admin", "localAvailable": s.LocalBrowser && user(r).Role == "admin"}
		publisher, err := s.Affiliate.PublisherID(r.Context())
		if err != nil {
			s.reply(w, r, 0, nil, err)
			return
		}
		v["publisher"] = publisher
		if s.Affiliate.Browser != nil {
			v["browser"] = s.Affiliate.Browser.Status()
		}
		s.reply(w, r, 200, v, nil)
	}))
	r.Put("/admin/browser/publisher", s.allowed("settings", true, func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, 0, nil, platform.Fail(410, "SHOPEE_SETTINGS_MOVED", "Cấu hình Shopee đã chuyển sang form Kết nối Shopee."))
	}))

	r.Put("/admin/browser/cookies", s.allowed("settings", false, func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, 0, nil, platform.Fail(410, "COOKIE_IMPORT_REMOVED", "Nhập cookie đã được tắt. Mở Chrome trên server để đăng nhập Shopee trực tiếp."))
	}))
	r.Post("/admin/browser/access", s.allowed("settings", true, func(w http.ResponseWriter, r *http.Request) {
		if user(r).Role != "admin" {
			s.reply(w, r, 0, nil, platform.Fail(403, "FORBIDDEN", "Chỉ quản trị viên được mở Chrome trên server."))
			return
		}
		if ((!s.LocalBrowser && s.BrowserMode == "local") || (s.RemoteBrowser == nil && !s.LocalBrowser)) || s.Affiliate.Browser == nil {
			s.reply(w, r, 0, nil, platform.Fail(503, "BROWSER_UNAVAILABLE", "Chrome từ xa chưa được cấu hình."))
			return
		}
		if e := s.Store.Limit(r.Context(), "browser-access:"+user(r).ID, 6); e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		if e := s.Affiliate.Browser.OpenInteractive(); e != nil {
			s.reply(w, r, 0, nil, platform.Fail(503, "BROWSER_UNAVAILABLE", "Chromium chưa sẵn sàng. Kiểm tra kết nối browser và màn hình điều khiển."))
			return
		}
		// Record who opened the display; never log the access ticket or cookies.
		tx, e := s.Store.Pool.Begin(r.Context())
		if e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		defer tx.Rollback(r.Context())
		action := "remote_browser_opened"
		if s.LocalBrowser {
			action = "local_browser_opened"
		}
		e = platform.Audit(r.Context(), tx, user(r).ID, action, "browser", map[string]any{})
		if e == nil {
			e = tx.Commit(r.Context())
		}
		if e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		if s.LocalBrowser {
			s.reply(w, r, 201, map[string]any{"local": true, "browser": s.Affiliate.Browser.Status()}, nil)
			return
		}
		cookie, _ := r.Cookie("hx_session")
		access, e := s.RemoteBrowser.Issue(r.Context(), cookie.Value)
		s.reply(w, r, 201, access, e)
	}))
	r.Post("/admin/browser/session-checks", s.allowed("settings", false, func(w http.ResponseWriter, r *http.Request) {
		if e := s.Store.Limit(r.Context(), "shopee-session:"+user(r).ID, 6); e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		if s.Affiliate.Browser == nil {
			s.reply(w, r, 0, nil, platform.Fail(503, "BROWSER_UNAVAILABLE", "Chromium chưa sẵn sàng. Kiểm tra CHROME_PATH."))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		s.reply(w, r, 201, s.Affiliate.Browser.RefreshSession(ctx), nil)
	}))
	r.Get("/admin/audit-logs", s.allowed("audit", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		s.list(w, r, `SELECT jsonb_build_object('id',id,'actorId',actor_id,'action',action,'resource',resource,'payload',payload,'createdAt',created_at) FROM audit_logs ORDER BY created_at DESC LIMIT $1 OFFSET $2`, l, o)
	}))
	r.Get("/admin/ledger-check", s.allowed("audit", false, func(w http.ResponseWriter, r *http.Request) {
		s.list(w, r, `SELECT jsonb_build_object('accountId',a.id,'kind',a.kind,'balance',a.balance,'ledgerBalance',coalesce(sum(e.amount),0)) FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0)`)
	}))
	r.Post("/admin/private-files", s.allowed("withdrawals", true, s.uploadEvidence))
	r.Post("/admin/order-imports", s.allowed("orders", false, s.previewCSV))
	r.Get("/admin/order-imports", s.allowed("orders", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		s.list(w, r, `SELECT jsonb_build_object('id',b.id,'filename',b.filename,'status',b.status,'error',b.error,'createdAt',b.created_at,'counts',(SELECT jsonb_object_agg(status,n) FROM (SELECT status,count(*) n FROM import_rows WHERE batch_id=b.id GROUP BY status) c)) FROM import_batches b ORDER BY created_at DESC LIMIT $1 OFFSET $2`, l, o)
	}))
	r.Get("/admin/order-imports/{id}/rows", s.allowed("orders", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		s.list(w, r, `SELECT jsonb_build_object('number',row_number,'status',status,'error',error,'payload',payload) FROM import_rows WHERE batch_id=$1 ORDER BY row_number LIMIT $2 OFFSET $3`, chi.URLParam(r, "id"), l, o)
	}))
	r.Post("/admin/order-imports/{id}/commit", s.allowed("orders", true, func(w http.ResponseWriter, r *http.Request) {
		v, e := (&imports.Service{Store: s.Store}).Commit(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), chi.URLParam(r, "id"))
		s.reply(w, r, 202, v, e)
	}))
	r.Post("/admin/order-imports/{id}/retry", s.allowed("orders", true, func(w http.ResponseWriter, r *http.Request) {
		_, e := s.Store.Pool.Exec(r.Context(), `UPDATE import_batches SET status='queued',error=NULL WHERE id=$1 AND status='failed'`, chi.URLParam(r, "id"))
		s.reply(w, r, 202, map[string]bool{"queued": true}, e)
	}))
	r.Post("/admin/order-imports/{id}/rows/{number}/resolve", s.allowed("orders", true, s.resolveRow))
}
func (s *Server) withdrawalList(admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		rows, e := s.pageRows(r, `SELECT jsonb_build_object('id',w.id,'userId',w.user_id,'name',u.name,'bank',w.bank,'details',w.bank_details,'amount',w.amount,'status',w.status,'processorId',w.processor_id,'bankReference',w.bank_reference,'evidenceId',w.evidence_path,'reason',w.reason,'createdAt',w.created_at) FROM withdrawals w JOIN users u ON u.id=w.user_id WHERE ($4::boolean OR w.user_id=$1) ORDER BY w.created_at DESC,w.id DESC LIMIT $2 OFFSET $3`, user(r).ID, l, o, admin)
		if e == nil {
			for i, b := range rows {
				var v map[string]any
				if e = json.Unmarshal(b, &v); e != nil {
					break
				}
				raw, er := s.Store.Decrypt(v["details"].(string))
				if er != nil {
					e = er
					break
				}
				var d map[string]string
				if er = json.Unmarshal([]byte(raw), &d); er != nil {
					e = er
					break
				}
				delete(v, "details")
				v["account"], v["holder"] = d["account"], d["holder"]
				rows[i], _ = json.Marshal(v)
			}
		}
		s.reply(w, r, 200, rows, e)
	}
}
func (s *Server) redemptionList(admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		rows, e := s.pageRows(r, `SELECT jsonb_build_object('id',r.id,'userId',r.user_id,'name',u.name,'giftName',g.name,'giftId',r.gift_id,'costXu',r.cost_xu,'legacyCost',CASE WHEN r.cost_unit='legacy_coin' THEN r.cost ELSE NULL END,'costUnit',r.cost_unit,'status',r.status,'cipher',r.voucher_cipher,'reason',r.reason,'createdAt',r.created_at) FROM gift_redemptions r JOIN gift_catalog g ON g.id=r.gift_id JOIN users u ON u.id=r.user_id WHERE ($4::boolean OR r.user_id=$1) ORDER BY r.created_at DESC,r.id DESC LIMIT $2 OFFSET $3`, user(r).ID, l, o, admin)
		if e == nil {
			for i, b := range rows {
				var v map[string]any
				if e = json.Unmarshal(b, &v); e != nil {
					break
				}
				if c, ok := v["cipher"].(string); ok && !admin {
					code, er := s.Store.Decrypt(c)
					if er != nil {
						e = er
						break
					}
					v["code"] = code
				}
				delete(v, "cipher")
				rows[i], _ = json.Marshal(v)
			}
		}
		s.reply(w, r, 200, rows, e)
	}
}
func (s *Server) saveFile(r *http.Request, purpose string) (string, string, error) {
	if e := r.ParseMultipartForm(10 << 20); e != nil {
		return "", "", platform.Fail(422, "INVALID_UPLOAD", "File tối đa 10 MB.")
	}
	defer r.MultipartForm.RemoveAll()
	f, h, e := r.FormFile("file")
	if e != nil {
		return "", "", platform.Fail(422, "FILE_REQUIRED", "Cần chọn file.")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 10*1024*1024+1))
	if e != nil {
		return "", "", e
	}
	if len(b) > 10*1024*1024 || len(b) == 0 {
		return "", "", platform.Fail(422, "INVALID_UPLOAD", "File rỗng hoặc vượt 10 MB.")
	}
	ct := http.DetectContentType(b)
	if purpose == "evidence" && ct != "image/png" && ct != "image/jpeg" && ct != "application/pdf" {
		return "", "", platform.Fail(422, "INVALID_UPLOAD", "Bằng chứng chỉ nhận PNG, JPEG, PDF.")
	}
	if purpose == "csv" {
		ct = "text/csv"
	}
	if e = os.MkdirAll(s.PrivateDir, 0700); e != nil {
		return "", "", e
	}
	name := platform.Token()
	path := filepath.Join(s.PrivateDir, name)
	if e = os.WriteFile(path, b, 0600); e != nil {
		return "", "", e
	}
	var id string
	e = s.Store.Pool.QueryRow(r.Context(), `INSERT INTO private_files(owner_id,purpose,name,path,content_type) VALUES($1,$2,$3,$4,$5) RETURNING id::text`, user(r).ID, purpose, filepath.Base(h.Filename), name, ct).Scan(&id)
	if e != nil {
		_ = os.Remove(path)
		return "", "", e
	}
	return id, string(b), nil
}
func (s *Server) uploadEvidence(w http.ResponseWriter, r *http.Request) {
	id, _, e := s.saveFile(r, "evidence")
	s.reply(w, r, 201, map[string]string{"id": id}, e)
}
func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	var owner, purpose, name, path, ct string
	e := s.Store.Pool.QueryRow(r.Context(), `SELECT owner_id::text,purpose,name,path,content_type FROM private_files WHERE id=$1`, chi.URLParam(r, "id")).Scan(&owner, &purpose, &name, &path, &ct)
	if e != nil {
		s.reply(w, r, 0, nil, platform.Fail(404, "NOT_FOUND", "Không có file."))
		return
	}
	u := user(r)
	if u.ID != owner && !(purpose == "evidence" && u.Can("withdrawals")) && !(purpose == "csv" && u.Can("orders")) {
		s.reply(w, r, 0, nil, platform.Fail(403, "FORBIDDEN", "Không có quyền tải file."))
		return
	}
	if filepath.Base(path) != path {
		s.reply(w, r, 0, nil, platform.Fail(500, "INVALID_PATH", "Đường dẫn file không hợp lệ."))
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": name}))
	w.Header().Set("Cache-Control", "no-store")
	http.ServeFile(w, r, filepath.Join(s.PrivateDir, path))
}
func (s *Server) previewCSV(w http.ResponseWriter, r *http.Request) {
	id, raw, e := s.saveFile(r, "csv")
	if e != nil {
		s.reply(w, r, 0, nil, e)
		return
	}
	mapping := map[string]string{}
	if m := r.FormValue("mapping"); m != "" {
		if e = json.Unmarshal([]byte(m), &mapping); e != nil {
			s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_MAPPING", "Mapping không hợp lệ."))
			return
		}
	}
	rows, e := imports.Parse(strings.NewReader(raw), mapping)
	if e != nil {
		s.reply(w, r, 0, nil, platform.Fail(422, "CSV_INVALID", e.Error()))
		return
	}
	var filename string
	if e = s.Store.Pool.QueryRow(r.Context(), `SELECT name FROM private_files WHERE id=$1`, id).Scan(&filename); e != nil {
		s.reply(w, r, 0, nil, e)
		return
	}
	v, e := (&imports.Service{Store: s.Store}).Preview(r.Context(), user(r).ID, filename, platform.Hash(raw), mapping, rows)
	s.reply(w, r, 201, v, e)
}
func (s *Server) manualOrder(w http.ResponseWriter, r *http.Request) {
	var p struct {
		Tracking     string    `json:"trackingCode"`
		Channel      string    `json:"channel"`
		Publisher    string    `json:"publisher"`
		ExternalID   string    `json:"externalId"`
		LineID       string    `json:"lineId"`
		ProductName  string    `json:"productName"`
		Value        int64     `json:"value"`
		Commission   int64     `json:"commission"`
		Evidence     string    `json:"evidence"`
		SubIDs       [5]string `json:"subIds"`
		ShopID       string    `json:"shopId"`
		ItemID       string    `json:"itemId"`
		ConversionID string    `json:"conversionId"`
		ModelID      string    `json:"modelId"`
		PromotionID  string    `json:"promotionId"`
		OrderedAt    time.Time `json:"orderedAt"`
	}
	if !s.body(w, r, &p) {
		return
	}
	if !platform.Text(p.ProductName, 1, 200) || !platform.Text(p.Evidence, 3, 500) || p.Channel != "shopee" || p.Value < 0 || p.Value > 1e12 || p.Commission < 0 || p.Commission > 1e12 || p.OrderedAt.IsZero() {
		s.reply(w, r, 0, nil, platform.Fail(422, "SIGNED_REPORT_REQUIRED", "Cần đủ ID nguồn Shopee, Sub_id1–5, giờ đặt đơn và bằng chứng."))
		return
	}
	row := imports.Row{NativeShopee: true, Channel: p.Channel, Publisher: p.Publisher, OrderID: p.ExternalID, Tracking: p.Tracking, Name: p.ProductName, Value: p.Value, Commission: p.Commission, SubIDs: p.SubIDs, ShopID: p.ShopID, ItemID: p.ItemID, ConversionID: p.ConversionID, ModelID: p.ModelID, PromotionID: p.PromotionID, Date: p.OrderedAt, Status: "approved"}
	line, e := imports.SourceLineID(row)
	if e != nil {
		s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_SOURCE_ID", e.Error()))
		return
	}
	row.LineID = line
	v, e := s.Store.Action(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), "manual-order", p, func(tx pgx.Tx) (any, error) {
		a, e := imports.Attribute(r.Context(), tx, s.Store, &row)
		if e != nil {
			var invalid *imports.Ineligible
			if errors.As(e, &invalid) {
				return nil, platform.Fail(422, "INVALID_TRACKING", e.Error())
			}
			return nil, e
		}
		if e := platform.LockTracking(r.Context(), tx, a.User, row.Tracking); e != nil {
			return nil, e
		}
		if e := cashback.LockOrder(r.Context(), tx, row.Channel, row.Publisher, row.OrderID, row.LineID); e != nil {
			return nil, e
		}
		id, cash, e := imports.InsertSignedOrder(r.Context(), tx, s.Store, &row)
		if e != nil {
			return nil, platform.Conflict(e)
		}
		e = platform.Audit(r.Context(), tx, user(r).ID, "manual_order", id, p)
		return map[string]any{"id": id, "cashback": cash}, e
	})
	s.reply(w, r, 201, v, e)
}
func (s *Server) resolveRow(w http.ResponseWriter, r *http.Request) {
	var p struct {
		TrackingCode string `json:"trackingCode"`
		Reason       string `json:"reason"`
	}
	if !s.body(w, r, &p) {
		return
	}
	if !platform.Text(p.Reason, 3, 500) {
		s.reply(w, r, 0, nil, platform.Fail(422, "REASON_REQUIRED", "Cần lý do khớp tracking."))
		return
	}
	tx, e := s.Store.Pool.Begin(r.Context())
	if e != nil {
		s.reply(w, r, 0, nil, e)
		return
	}
	defer tx.Rollback(r.Context())
	// Resolution is only allowed against an existing legacy order; signed native rows cannot be rewritten.
	var payload []byte
	e = tx.QueryRow(r.Context(), `SELECT payload FROM import_rows WHERE batch_id=$1 AND row_number=$2 AND status='unmatched' FOR UPDATE`, chi.URLParam(r, "id"), chi.URLParam(r, "number")).Scan(&payload)
	var row imports.Row
	if e == nil {
		e = json.Unmarshal(payload, &row)
	}
	if e == nil && row.NativeShopee {
		e = platform.Fail(422, "SIGNED_REPORT_REQUIRED", "Không thể sửa Sub_id của báo cáo đã ký.")
	}
	var exists bool
	if e == nil {
		e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM orders o LEFT JOIN affiliate_links l ON l.id=o.link_id WHERE o.channel=$1 AND o.publisher=$2 AND o.external_id=$3 AND o.line_id=$4 AND coalesce(o.tracking_code,l.tracking_code)=$5 AND o.cashback_mode='commission_share')`, row.Channel, row.Publisher, row.OrderID, row.LineID, p.TrackingCode).Scan(&exists)
	}
	if e == nil && !exists {
		e = platform.Fail(422, "TRACKING_NOT_FOUND", "Mã link cũ chỉ cập nhật được đơn đã tồn tại.")
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `UPDATE import_rows SET payload=jsonb_set(payload,'{trackingCode}',to_jsonb($3::text)),status='valid',error=NULL WHERE batch_id=$1 AND row_number=$2`, chi.URLParam(r, "id"), chi.URLParam(r, "number"), p.TrackingCode)
	}

	if e == nil {
		_, e = tx.Exec(r.Context(), `UPDATE import_batches SET status='queued' WHERE id=$1 AND status='completed'`, chi.URLParam(r, "id"))
	}
	if e == nil {
		e = platform.Audit(r.Context(), tx, user(r).ID, "import_row_resolved", chi.URLParam(r, "id"), p)
	}
	if e == nil {
		e = tx.Commit(r.Context())
	}
	s.reply(w, r, 200, p, e)
}
