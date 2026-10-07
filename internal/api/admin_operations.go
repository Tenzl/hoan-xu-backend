package api

import (
	"github.com/go-chi/chi/v5"
	"hoanxu/internal/platform"
	"net/http"
)

func (s *Server) adminOperationRoutes(r chi.Router) {
	for _, action := range []string{"permissions", "reset-password", "status"} {
		method := http.MethodPut
		if action == "reset-password" {
			method = http.MethodPost
		}
		if action == "status" {
			method = http.MethodPatch
		}
		r.MethodFunc(method, "/admin/internal-accounts/{id}/"+action, s.allowed("internal", true, func(w http.ResponseWriter, r *http.Request) {
			if !s.customerID(w, r, chi.URLParam(r, "id")) {
				return
			}
			var p struct {
				Permissions *[]string `json:"permissions"`
				Password    string    `json:"password"`
				Blocked     *bool     `json:"blocked"`
			}
			if !s.body(w, r, &p) {
				return
			}
			if (action == "permissions" && p.Permissions == nil) || (action == "status" && p.Blocked == nil) {
				s.reply(w, r, 0, nil, platform.Fail(422, "VALIDATION_ERROR", "Thiếu thông tin tài khoản."))
				return
			}
			var permissions []string
			var blocked bool
			if p.Permissions != nil {
				permissions = *p.Permissions
			}
			if p.Blocked != nil {
				blocked = *p.Blocked
			}
			err := s.Auth.UpdateInternal(r.Context(), user(r).ID, chi.URLParam(r, "id"), action, permissions, p.Password, blocked)
			s.reply(w, r, 200, map[string]bool{"updated": err == nil}, err)
		}))
	}
	r.Get("/admin/work-queues", func(w http.ResponseWriter, r *http.Request) {
		u := user(r)
		if u.Role != "staff" && u.Role != "admin" {
			s.reply(w, r, 0, nil, platform.Fail(403, "FORBIDDEN", "Bạn không có quyền."))
			return
		}
		result := map[string]int64{}
		counts := []struct{ permission, key, sql string }{
			{"orders", "pendingOrders", `SELECT count(*) FROM orders WHERE status='pending'`},
			{"withdrawals", "pendingWithdrawals", `SELECT count(*) FROM withdrawals WHERE status='pending'`},
			{"withdrawals", "processingWithdrawals", `SELECT count(*) FROM withdrawals WHERE status='processing'`},
			{"gifts", "pendingGifts", `SELECT count(*) FROM gift_redemptions WHERE status='pending'`},
		}
		for _, c := range counts {
			if !u.Can(c.permission) {
				continue
			}
			var n int64
			if err := s.Store.Pool.QueryRow(r.Context(), c.sql).Scan(&n); err != nil {
				s.reply(w, r, 0, nil, err)
				return
			}
			result[c.key] = n
		}
		s.reply(w, r, 200, result, nil)
	})
	r.Get("/admin/notification-recipients", s.allowed("notifications", false, func(w http.ResponseWriter, r *http.Request) {
		l, o := page(r)
		q := r.URL.Query().Get("q")
		if len([]rune(q)) > 100 {
			s.reply(w, r, 0, nil, platform.Fail(422, "VALIDATION_ERROR", "Từ khóa tối đa 100 ký tự."))
			return
		}
		s.list(w, r, `SELECT jsonb_build_object('id',id,'name',name,'email',email) FROM users WHERE role='customer' AND ($3='' OR name ILIKE '%'||$3||'%' OR email ILIKE '%'||$3||'%') ORDER BY name,id LIMIT $1 OFFSET $2`, l, o, q)
	}))
}
