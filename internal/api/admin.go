package api

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/auth"
	"hoanxu/internal/browser"
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
		s.list(w, r, `SELECT jsonb_build_object('id',u.id,'name',u.name,'email',u.email,'role',u.role,'blocked',u.blocked,'trackingCode',u.tracking_code,'createdAt',u.created_at,'available',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='available'),0),'held',coalesce((SELECT balance FROM wallet_accounts WHERE user_id=u.id AND kind='held'),0),'coins',coalesce((SELECT balance FROM coin_accounts WHERE user_id=u.id),0)) FROM users u WHERE role='customer' AND ($3='' OR name ILIKE '%'||$3||'%' OR email ILIKE '%'||$3||'%') ORDER BY created_at DESC,id DESC LIMIT $1 OFFSET $2`, l, o, q)
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
		s.list(w, r, `SELECT jsonb_build_object('id',id,'name',name,'channel',channel,'cost',cost,'stock',stock,'active',active) FROM gift_catalog ORDER BY id`)
	}))
	r.Patch("/admin/gifts/{id}", s.allowed("gifts", true, func(w http.ResponseWriter, r *http.Request) {
		var p struct {
			Name   string `json:"name"`
			Cost   int64  `json:"cost"`
			Stock  int    `json:"stock"`
			Active bool   `json:"active"`
		}
		if !s.body(w, r, &p) {
			return
		}
		if !platform.Text(p.Name, 1, 80) || p.Cost <= 0 || p.Cost > 1000000 || p.Stock < 0 || p.Stock > 100000 {
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
		s.list(w, r, `SELECT jsonb_build_object('id',id,'name',name,'status',status,'settings',settings) FROM affiliate_channels ORDER BY id`)
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
		if p.Status == "available" && (!s.Affiliate.Enabled || !s.Affiliate.TrackingVerified) {
			s.reply(w, r, 0, nil, platform.Fail(409, "TRACKING_NOT_VERIFIED", "Cần xác minh tích hợp và tracking trước khi bật."))
			return
		}
		if p.Status != "available" && p.Status != "not_configured" && p.Status != "temporarily_unavailable" {
			s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_STATUS", "Trạng thái không hợp lệ."))
			return
		}
		tx, e := s.Store.Pool.Begin(r.Context())
		if e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		defer tx.Rollback(r.Context())
		_, e = tx.Exec(r.Context(), `UPDATE affiliate_channels SET status=$2,settings=jsonb_build_object('template',$3::text) WHERE id=$1`, id, p.Status, p.Template)
		if e == nil {
			e = platform.Audit(r.Context(), tx, user(r).ID, "channel_updated", id, p)
		}
		if e == nil {
			e = tx.Commit(r.Context())
		}
		s.reply(w, r, 200, p, e)
	}))
	r.Get("/admin/browser", s.allowed("settings", false, func(w http.ResponseWriter, r *http.Request) {
		v := map[string]any{"enabled": s.Affiliate.CheckEnabled(), "trackingVerified": s.Affiliate.TrackingVerified}
		if s.Affiliate.Browser != nil {
			v["browser"] = s.Affiliate.Browser.Status()
		}
		s.reply(w, r, 200, v, nil)
	}))
	r.Put("/admin/browser/cookies", s.allowed("settings", false, s.pasteShopeeCookies))
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
		rows, e := s.pageRows(r, `SELECT jsonb_build_object('id',r.id,'userId',r.user_id,'name',u.name,'giftName',g.name,'giftId',r.gift_id,'cost',r.cost,'status',r.status,'cipher',r.voucher_cipher,'reason',r.reason,'createdAt',r.created_at) FROM gift_redemptions r JOIN gift_catalog g ON g.id=r.gift_id JOIN users u ON u.id=r.user_id WHERE ($4::boolean OR r.user_id=$1) ORDER BY r.created_at DESC,r.id DESC LIMIT $2 OFFSET $3`, user(r).ID, l, o, admin)
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
		Tracking    string `json:"trackingCode"`
		Channel     string `json:"channel"`
		Publisher   string `json:"publisher"`
		ExternalID  string `json:"externalId"`
		LineID      string `json:"lineId"`
		ProductName string `json:"productName"`
		Value       int64  `json:"value"`
		Commission  int64  `json:"commission"`
		Evidence    string `json:"evidence"`
	}
	if !s.body(w, r, &p) {
		return
	}
	if !platform.Text(p.ProductName, 1, 200) || !platform.Text(p.Publisher, 1, 100) || !platform.Text(p.ExternalID, 1, 100) || !platform.Text(p.LineID, 1, 100) || !platform.Text(p.Evidence, 3, 500) || p.Value < 0 || p.Commission < 0 || p.Value > 1e12 || p.Commission > 1e12 {
		s.reply(w, r, 0, nil, platform.Fail(422, "VALIDATION_ERROR", "Thiếu nguồn/bằng chứng hoặc số tiền không hợp lệ."))
		return
	}
	v, e := s.Store.Action(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), "manual-order", p, func(tx pgx.Tx) (any, error) {
		var id, owner, link, policy string
		var tier *string
		var minBps, maxBps int
		e := tx.QueryRow(r.Context(), `SELECT user_id::text,id::text,policy_id::text,tier_code,min_share_bps,max_share_bps FROM affiliate_links WHERE tracking_code=$1 AND channel=$2`, p.Tracking, p.Channel).Scan(&owner, &link, &policy, &tier, &minBps, &maxBps)
		if e == pgx.ErrNoRows {
			return nil, platform.Fail(422, "TRACKING_NOT_FOUND", "Không tìm thấy tracking đúng kênh.")
		}
		if e != nil {
			return nil, platform.Conflict(e)
		}
		if e = cashback.LockOrder(r.Context(), tx, p.Channel, p.Publisher, p.ExternalID, p.LineID); e != nil {
			return nil, e
		}
		var exists bool
		if e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM orders WHERE channel=$1 AND publisher=$2 AND external_id=$3 AND line_id=$4)`, p.Channel, p.Publisher, p.ExternalID, p.LineID).Scan(&exists); e != nil {
			return nil, e
		}
		if exists {
			return nil, platform.Fail(409, "CONFLICT", "Dữ liệu đã tồn tại.")
		}
		bps, e := cashback.Sample(nil, minBps, maxBps)
		if e != nil {
			return nil, e
		}
		cash, e := cashback.Amount(p.Commission, bps)
		if e != nil {
			return nil, e
		}
		e = tx.QueryRow(r.Context(), `INSERT INTO orders(user_id,link_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,ordered_at,source_status,tier_code,share_bps) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,now(),'approved',$12,$13) RETURNING id::text`, owner, link, policy, p.Channel, p.Publisher, p.ExternalID, p.LineID, p.ProductName, p.Value, p.Commission, cash, tier, bps).Scan(&id)
		if e != nil {
			return nil, platform.Conflict(e)
		}
		e = platform.Audit(r.Context(), tx, user(r).ID, "manual_order", id, p)
		return map[string]any{"id": id, "tierCode": tier, "sharePercent": cashback.Percent(bps), "cashback": cash}, e
	})
	s.reply(w, r, 201, v, e)
}
func (s *Server) pasteShopeeCookies(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 96*1024)
	var p struct {
		Cookie string `json:"cookie"`
	}
	if !s.body(w, r, &p) {
		return
	}
	cookies, e := browser.ParseCookies(p.Cookie)
	if e != nil {
		s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_SHOPEE_COOKIES", "Cookie không hợp lệ. Dán JSON cookie xuất từ affiliate.shopee.vn hoặc Cookie header, tối đa 64 KB."))
		return
	}
	if e = s.Store.Limit(r.Context(), "shopee-cookies:"+user(r).ID, 5); e != nil {
		s.reply(w, r, 0, nil, e)
		return
	}
	if s.Affiliate.Browser == nil {
		s.reply(w, r, 0, nil, platform.Fail(503, "BROWSER_UNAVAILABLE", "Chromium chưa sẵn sàng. Kiểm tra CHROME_PATH."))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	status, e := s.Affiliate.Browser.PasteCookies(ctx, p.Cookie)
	if e != nil {
		code, message := "BROWSER_UNAVAILABLE", "Không áp dụng được cookie. Kiểm tra Chromium và thử lại."
		if errors.Is(e, context.DeadlineExceeded) || errors.Is(e, context.Canceled) {
			code = "BROWSER_TIMEOUT"
		}
		s.reply(w, r, 0, nil, platform.Fail(503, code, message))
		return
	}
	tx, e := s.Store.Pool.Begin(r.Context())
	if e == nil {
		defer tx.Rollback(r.Context())
		e = platform.Audit(r.Context(), tx, user(r).ID, "shopee_cookie_imported", "shopee", map[string]int{"cookieCount": len(cookies)})
		if e == nil {
			e = tx.Commit(r.Context())
		}
	}
	s.reply(w, r, 200, status, e)
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
	var exists bool
	e = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM affiliate_links WHERE tracking_code=$1)`, p.TrackingCode).Scan(&exists)
	if e == nil && !exists {
		e = platform.Fail(422, "TRACKING_NOT_FOUND", "Tracking không có trong hệ thống.")
	}
	if e == nil {
		_, e = tx.Exec(r.Context(), `UPDATE import_rows SET payload=jsonb_set(payload,'{trackingCode}',to_jsonb($3::text)),status='valid',error=NULL WHERE batch_id=$1 AND row_number=$2 AND status='unmatched'`, chi.URLParam(r, "id"), chi.URLParam(r, "number"), p.TrackingCode)
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
