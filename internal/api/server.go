package api

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/cashback"
	"hoanxu/internal/community"
	"hoanxu/internal/platform"
	"hoanxu/internal/remotebrowser"
	"hoanxu/internal/rewards"
	"hoanxu/internal/shopeeconfig"
	"hoanxu/internal/users"
	"hoanxu/internal/wallet"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	Store              *platform.Store
	Auth               *auth.Service
	Affiliate          *affiliate.Service
	RemoteBrowser      *remotebrowser.Service
	LocalBrowser       bool
	Origin, PrivateDir string
	ProxySigningKey    string
	Secure             bool
	Mux                *chi.Mux
	PrepareBrowser     func(shopeeconfig.Config) (func(), error)
	BrowserMode        string
	browserMu          sync.RWMutex
	ConfigVersion      string
	Lifetime           context.Context
	VerifyShopee       func(context.Context, shopeeconfig.Config, string) error
	verificationMu     sync.Mutex
	verificationWG     sync.WaitGroup
	cancelVerification context.CancelFunc
}
type contextKey string

func user(r *http.Request) *auth.User {
	u, _ := r.Context().Value(contextKey("user")).(*auth.User)
	return u
}
func (s *Server) reply(w http.ResponseWriter, r *http.Request, status int, data any, e error) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if e != nil {
		var problem *platform.Error
		if !errors.As(e, &problem) {
			slog.Error("request_failed", "requestId", middleware.GetReqID(r.Context()), "error", e)
			problem = &platform.Error{Status: 500, Code: "INTERNAL_ERROR", Message: "Không xử lý được yêu cầu."}
		}
		if problem.Status == 429 {
			w.Header().Set("Retry-After", "60")
		}
		w.WriteHeader(problem.Status)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": problem.Code, "message": localMessage(r, problem.Message), "details": []any{}}, "requestId": middleware.GetReqID(r.Context())})
		return
	}
	w.WriteHeader(status)
	meta, _ := r.Context().Value(contextKey("pagination")).(map[string]any)
	if meta == nil {
		meta = map[string]any{}
	}
	meta["requestId"] = middleware.GetReqID(r.Context())
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "meta": meta})
}
func decode(r *http.Request, v any) error {
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		if errors.Is(e, cashback.ErrInvalidPercent) {
			return platform.Fail(422, "INVALID_CASHBACK_POLICY", "Tỷ lệ phải là số từ 0–100%, tối đa hai chữ số thập phân.")
		}
		if errors.Is(e, cashback.ErrInvalidTier) {
			return platform.Fail(422, "INVALID_CASHBACK_POLICY", "Cần đủ ba hạng và phiên bản chính sách hợp lệ.")
		}
		return platform.Fail(400, "INVALID_JSON", "JSON không hợp lệ.")
	}
	if d.Decode(&struct{}{}) != io.EOF {
		return platform.Fail(400, "INVALID_JSON", "Chỉ nhận một JSON object.")
	}
	return nil
}
func (s *Server) body(w http.ResponseWriter, r *http.Request, v any) bool {
	if e := decode(r, v); e != nil {
		s.reply(w, r, 0, nil, e)
		return false
	}
	return true
}
func (s *Server) cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", HttpOnly: true, Secure: s.Secure, SameSite: http.SameSiteLaxMode, MaxAge: age})
}
func (s *Server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		r.Body = http.MaxBytesReader(w, r.Body, 12*1024*1024)
		if r.Method != "GET" && r.Method != "HEAD" && r.Method != "OPTIONS" {
			if r.Header.Get("Origin") != s.Origin {
				s.reply(w, r, 0, nil, platform.Fail(403, "ORIGIN_INVALID", "Origin không hợp lệ."))
				return
			}
		}
		identity := "ip:" + clientIP(r, s.ProxySigningKey, time.Now())
		if s.Auth != nil {
			if c, err := r.Cookie("hx_session"); err == nil {
				if u, err := s.Auth.Session(r.Context(), c.Value); err == nil {
					r = r.WithContext(context.WithValue(r.Context(), contextKey("user"), u))
					identity = "user:" + u.ID
				}
			}
		}
		if e := s.Store.Limit(r.Context(), "api:"+identity, 240); e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) protected(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := chi.URLParam(r, "id"); id != "" && !strings.Contains(r.URL.Path, "/admin/gifts/") && !strings.Contains(r.URL.Path, "/admin/affiliate-channels/") && !platform.ID(id) {
			s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_ID", "ID không hợp lệ."))
			return
		}
		c, e := r.Cookie("hx_session")
		if e != nil {
			s.reply(w, r, 0, nil, platform.Fail(401, "UNAUTHENTICATED", "Vui lòng đăng nhập."))
			return
		}
		u := user(r)
		if u == nil {
			u, e = s.Auth.Session(r.Context(), c.Value)
		}
		if e != nil {
			s.reply(w, r, 0, nil, e)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" {
			if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-CSRF-Token")), []byte(u.CSRF)) != 1 {
				s.reply(w, r, 0, nil, platform.Fail(403, "CSRF_INVALID", "Phiên bảo vệ không hợp lệ."))
				return
			}
		}
		if u.MustChange && r.URL.Path != "/api/v1/me/password" && r.URL.Path != "/api/v1/me" && r.URL.Path != "/api/v1/me/sessions" && r.URL.Path != "/api/v1/auth/logout" {
			s.reply(w, r, 0, nil, platform.Fail(403, "PASSWORD_CHANGE_REQUIRED", "Bạn cần đổi mật khẩu tạm."))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey("user"), u)))
	})
}
func (s *Server) customer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user(r).Role != "customer" {
			s.reply(w, r, 0, nil, platform.Fail(403, "CUSTOMER_REQUIRED", "Chức năng dành cho khách đăng nhập Google."))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func (s *Server) allowed(permission string, recent bool, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u := user(r)
		if !u.Can(permission) {
			s.reply(w, r, 0, nil, platform.Fail(403, "FORBIDDEN", "Bạn không có quyền."))
			return
		}
		if recent && !u.Recent {
			s.reply(w, r, 0, nil, platform.Fail(403, "REAUTH_REQUIRED", "Vui lòng xác thực lại mật khẩu."))
			return
		}
		next(w, r)
	}
}
func page(r *http.Request) (int, int) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("perPage"))
	if limit < 1 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if p < 1 {
		p = 1
	}
	if p > 10000 {
		p = 10000
	}
	return limit, (p - 1) * limit
}
func (s *Server) list(w http.ResponseWriter, r *http.Request, q string, args ...any) {
	v, e := s.pageRows(r, q, args...)
	s.reply(w, r, 200, v, e)
}
func (s *Server) one(w http.ResponseWriter, r *http.Request, q string, args ...any) {
	v, e := s.Store.One(r.Context(), q, args...)
	s.reply(w, r, 200, v, e)
}
func New(s *Server) *chi.Mux {
	r := chi.NewRouter()
	s.Mux = r
	r.Use(middleware.RequestID, middleware.Recoverer)
	r.Use(s.browserConfigLock)
	if s.RemoteBrowser != nil {
		r.Mount("/browser", s.RemoteBrowser)
	}
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, 200, map[string]string{"status": "ok"}, nil)
	})
	r.Get("/ping", func(w http.ResponseWriter, r *http.Request) {
		s.reply(w, r, http.StatusOK, map[string]string{
			"status":  "ok",
			"service": "hoan-xu-backend",
			"time":    time.Now().UTC().Format(time.RFC3339),
		}, nil)
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, c := context.WithTimeout(r.Context(), 2*time.Second)
		defer c()
		e := s.Store.Pool.Ping(ctx)
		if e != nil {
			e = platform.Fail(503, "DATABASE_UNAVAILABLE", "Database chưa sẵn sàng.")
		}
		s.reply(w, r, 200, map[string]string{"status": "ready"}, e)
	})
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.security)
		r.Get("/config", func(w http.ResponseWriter, r *http.Request) {
			s.one(w, r, `SELECT settings||jsonb_build_object('googleConfigured',$1::boolean) FROM app_settings`, s.Auth.OAuth != nil)
		})
		r.Get("/affiliate-channels", func(w http.ResponseWriter, r *http.Request) {
			rows, e := s.Store.Queries.ListChannels(r.Context())
			v := []map[string]string{}
			for _, c := range rows {
				status := c.Status
				if c.ID == "shopee" && s.Affiliate != nil && s.Affiliate.ManagedConfig {
					status = "not_configured"
					if s.Affiliate.CheckEnabled() && s.Affiliate.TrackingVerified {
						status = "available"
					}
				}
				v = append(v, map[string]string{"id": c.ID, "name": c.Name, "status": status})
			}
			s.reply(w, r, 200, v, e)
		})
		r.Get("/auth/google", func(w http.ResponseWriter, r *http.Request) {
			location, state, e := s.Auth.GoogleStart(r.Context())
			if e != nil {
				s.reply(w, r, 0, nil, e)
				return
			}
			s.cookie(w, "hx_oauth", state, 600)
			http.Redirect(w, r, location, 302)
		})
		r.Get("/auth/google/callback", func(w http.ResponseWriter, r *http.Request) {
			cookie, e := r.Cookie("hx_oauth")
			state := r.URL.Query().Get("state")
			if e != nil || state == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
				http.Redirect(w, r, s.Origin+"/login?error=google", 302)
				return
			}
			s.cookie(w, "hx_oauth", "", -1)
			token, e := s.Auth.GoogleFinish(r.Context(), state, r.URL.Query().Get("code"))
			if e != nil {
				http.Redirect(w, r, s.Origin+"/login?error=google", 302)
				return
			}
			s.cookie(w, "hx_session", token, 7*86400)
			http.Redirect(w, r, s.Origin+"/", 302)
		})
		r.Post("/auth/internal/login", func(w http.ResponseWriter, r *http.Request) {
			var p struct {
				Username string `json:"username"`
				Password string `json:"password"`
			}
			if !s.body(w, r, &p) {
				return
			}
			ip := clientIP(r, s.ProxySigningKey, time.Now())
			if e := s.Store.Limit(r.Context(), "login:"+ip, 10); e != nil {
				s.reply(w, r, 0, nil, e)
				return
			}
			token, e := s.Auth.Login(r.Context(), p.Username, p.Password)
			if e == nil {
				s.cookie(w, "hx_session", token, 7*86400)
			}
			s.reply(w, r, 200, map[string]bool{"loggedIn": e == nil}, e)
		})
		r.Get("/deals", func(w http.ResponseWriter, r *http.Request) {
			l, o := page(r)
			s.list(w, r, `SELECT jsonb_build_object('id',d.id,'body',d.body,'channel',d.channel,'name',u.name,'createdAt',d.created_at,'likes',(SELECT count(*) FROM deal_likes WHERE deal_id=d.id)) FROM deals d JOIN users u ON u.id=d.user_id WHERE NOT d.hidden AND NOT d.deleted ORDER BY d.created_at DESC,d.id DESC LIMIT $1 OFFSET $2`, l, o)
		})
		r.Get("/leaderboards", s.leaderboard)
		r.Get("/leaderboard", func(w http.ResponseWriter, r *http.Request) {
			s.list(w, r, `SELECT jsonb_build_object('name',left(u.name,1)||'***','cashback',sum(o.cashback),'orders',count(*)) FROM orders o JOIN users u ON u.id=o.user_id WHERE o.status='approved' AND date_trunc('month',o.ordered_at AT TIME ZONE 'Asia/Ho_Chi_Minh')=date_trunc('month',now() AT TIME ZONE 'Asia/Ho_Chi_Minh') GROUP BY u.id ORDER BY sum(o.cashback) DESC LIMIT 5`)
		})
		r.Get("/gifts", func(w http.ResponseWriter, r *http.Request) {
			s.list(w, r, `SELECT jsonb_build_object('id',id,'name',name,'channel',channel,'costXu',cost,'costUnit','xu','stock',stock,'active',active) FROM gift_catalog WHERE active ORDER BY cost,id`)
		})
		r.Post("/product-checks", s.check)
		r.Post("/shopee/check", s.check)
		r.Group(func(r chi.Router) {
			r.Use(s.protected)
			r.Get("/me", func(w http.ResponseWriter, r *http.Request) {
				u, e := s.Auth.Profile(r.Context(), user(r))
				s.reply(w, r, 200, u, e)
			})
			r.Patch("/me", func(w http.ResponseWriter, r *http.Request) {
				var p users.ProfileInput
				if !s.body(w, r, &p) {
					return
				}
				e := (&users.Service{Store: s.Store}).Update(r.Context(), user(r).ID, user(r).Role, p)
				s.reply(w, r, 200, p, e)
			})
			r.Post("/auth/logout", func(w http.ResponseWriter, r *http.Request) {
				_, e := s.Store.Pool.Exec(r.Context(), `DELETE FROM sessions WHERE id=$1`, user(r).SessionID)
				s.cookie(w, "hx_session", "", -1)
				s.reply(w, r, 200, map[string]bool{"loggedOut": true}, e)
			})
			r.Post("/auth/internal/reauth", func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					Password string `json:"password"`
				}
				if !s.body(w, r, &p) {
					return
				}
				e := s.Store.Limit(r.Context(), "reauth:"+user(r).ID, 10)
				if e == nil {
					e = s.Auth.Reauth(r.Context(), user(r), p.Password)
				}
				s.reply(w, r, 200, map[string]bool{"authenticated": e == nil}, e)
			})
			r.Put("/me/password", func(w http.ResponseWriter, r *http.Request) {
				var p struct {
					OldPassword string `json:"oldPassword"`
					Password    string `json:"password"`
				}
				if !s.body(w, r, &p) {
					return
				}
				e := s.Store.Limit(r.Context(), "password:"+user(r).ID, 10)
				if e == nil {
					e = s.Auth.ChangePassword(r.Context(), user(r), p.OldPassword, p.Password)
				}
				s.reply(w, r, 200, map[string]bool{"changed": e == nil}, e)
			})
			r.Get("/me/sessions", func(w http.ResponseWriter, r *http.Request) {
				s.list(w, r, `SELECT jsonb_build_object('id',id,'createdAt',created_at,'expiresAt',expires_at) FROM sessions WHERE user_id=$1 ORDER BY created_at DESC`, user(r).ID)
			})
			r.Delete("/me/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
				_, e := s.Store.Pool.Exec(r.Context(), `DELETE FROM sessions WHERE id=$1 AND user_id=$2`, chi.URLParam(r, "id"), user(r).ID)
				s.reply(w, r, 200, map[string]bool{"revoked": true}, e)
			})
			s.customerRoutes(r)
			s.adminRoutes(r)
			r.Get("/private-files/{id}", s.download)
		})
	})
	return r
}
func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	var p struct {
		URL string `json:"url"`
	}
	if !s.body(w, r, &p) {
		return
	}
	ip := clientIP(r, s.ProxySigningKey, time.Now())
	key, max := "checker:"+ip, 10
	if u := user(r); u != nil {
		key, max = "checker:"+u.ID, 30
	}
	e := s.Store.Limit(r.Context(), key, max)
	var data any
	if e == nil {
		ctx, c := context.WithTimeout(r.Context(), 45*time.Second)
		defer c()
		data, e = s.Affiliate.Check(ctx, p.URL)
	}
	s.reply(w, r, 200, data, e)
}
func (s *Server) customerRoutes(r chi.Router) {
	r.Group(func(r chi.Router) {
		r.Use(s.customer)
		r.Get("/me/dashboard", s.dashboard)
		r.Get("/me/purchases", s.purchases)
		r.Get("/me/leaderboard", s.myLeaderboard)
		r.Get("/wallet", func(w http.ResponseWriter, r *http.Request) {
			s.one(w, r, `SELECT jsonb_build_object('available',coalesce(max(balance) FILTER(WHERE kind='available'),0),'held',coalesce(max(balance) FILTER(WHERE kind='held'),0),'debt',coalesce(max(balance) FILTER(WHERE kind='debt'),0),'giftHeld',coalesce(max(balance) FILTER(WHERE kind='gift_held'),0),'unit','xu') FROM wallet_accounts WHERE user_id=$1`, user(r).ID)
		})
		r.Get("/wallet/transactions", func(w http.ResponseWriter, r *http.Request) {
			l, o := page(r)
			s.list(w, r, `SELECT jsonb_build_object('id',t.id,'description',t.description,'createdAt',t.created_at,'amount',coalesce(sum(e.amount) FILTER(WHERE a.kind='available'),0),'heldAmount',coalesce(sum(e.amount) FILTER(WHERE a.kind='held'),0),'giftHeldAmount',coalesce(sum(e.amount) FILTER(WHERE a.kind='gift_held'),0),'debtAmount',coalesce(sum(e.amount) FILTER(WHERE a.kind='debt'),0),'unit','xu') FROM wallet_transactions t JOIN wallet_entries e ON e.transaction_id=t.id JOIN wallet_accounts a ON a.id=e.account_id WHERE a.user_id=$1 GROUP BY t.id ORDER BY t.created_at DESC,t.id DESC LIMIT $2 OFFSET $3`, user(r).ID, l, o)
		})
		r.Get("/orders", func(w http.ResponseWriter, r *http.Request) {
			l, o := page(r)
			status := r.URL.Query().Get("status")
			s.list(w, r, orderSQL+` WHERE o.user_id=$1 AND ($4='' OR o.status=$4) ORDER BY o.ordered_at DESC,o.id DESC LIMIT $2 OFFSET $3`, user(r).ID, l, o, status)
		})
		r.Get("/orders/{id}", func(w http.ResponseWriter, r *http.Request) {
			s.one(w, r, orderSQL+` WHERE o.id=$1 AND o.user_id=$2`, chi.URLParam(r, "id"), user(r).ID)
		})
		r.Get("/affiliate-links", func(w http.ResponseWriter, r *http.Request) {
			l, o := page(r)
			s.list(w, r, affiliate.LinksSQL+` WHERE l.user_id=$1 ORDER BY l.created_at DESC,l.id DESC LIMIT $2 OFFSET $3`, user(r).ID, l, o)
		})
		r.Delete("/affiliate-links/{id}", func(w http.ResponseWriter, r *http.Request) {
			if err := s.Affiliate.DeleteLink(r.Context(), user(r).ID, chi.URLParam(r, "id")); err != nil {
				s.reply(w, r, 0, nil, err)
				return
			}
			w.WriteHeader(http.StatusNoContent)
		})
		r.Get("/affiliate-links/{id}", func(w http.ResponseWriter, r *http.Request) {
			id := chi.URLParam(r, "id")
			if !platform.ID(id) {
				s.reply(w, r, 0, nil, platform.Fail(404, "NOT_FOUND", "Không có link."))
				return
			}
			s.one(w, r, affiliate.LinksSQL+` WHERE l.id=$1 AND l.user_id=$2`, id, user(r).ID)
		})
		r.Post("/affiliate-links", func(w http.ResponseWriter, r *http.Request) {
			var p struct {
				URL string `json:"url"`
			}
			if !s.body(w, r, &p) {
				return
			}
			e := s.Store.Limit(r.Context(), "link:"+user(r).ID, 30)
			var v any
			if e == nil {
				v, e = s.Affiliate.CreateLinkOperation(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), p.URL)
			}
			s.reply(w, r, 200, v, e)
		})
		r.Get("/withdrawals", s.withdrawalList(false))
		r.Post("/withdrawals", func(w http.ResponseWriter, r *http.Request) {
			var p wallet.WithdrawalInput
			if !s.body(w, r, &p) {
				return
			}
			v, e := (&wallet.Service{Store: s.Store}).Withdraw(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), p)
			s.reply(w, r, 201, v, e)
		})
		r.Get("/checkins", func(w http.ResponseWriter, r *http.Request) {
			s.one(w, r, `SELECT jsonb_build_object('available',(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),'unit','xu','streak',streak,'best',best,'lastDay',last_day,'today',$2::text,'checkedIn',coalesce(last_day=$2::date,false),'days',coalesce((SELECT jsonb_agg(day ORDER BY day) FROM checkins WHERE user_id=$1 AND day>=($2::date-30)),'[]')) FROM coin_accounts WHERE user_id=$1`, user(r).ID, rewards.LocalDay(time.Now()))
		})
		r.Post("/checkins", func(w http.ResponseWriter, r *http.Request) {
			v, e := (&rewards.Service{Store: s.Store}).Checkin(r.Context(), user(r).ID)
			s.reply(w, r, 201, v, e)
		})
		r.Get("/coins/transactions", func(w http.ResponseWriter, r *http.Request) {
			l, o := page(r)
			s.list(w, r, `SELECT jsonb_build_object('id',id,'amount',amount,'description',description,'createdAt',created_at,'unit','legacy_coin','equivalentXu',amount*300) FROM coin_transactions WHERE user_id=$1 ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, user(r).ID, l, o)
		})
		r.Post("/coin-exchanges", func(w http.ResponseWriter, r *http.Request) {
			var p struct {
				Coins int64 `json:"coins"`
			}
			if !s.body(w, r, &p) {
				return
			}
			v, e := (&rewards.Service{Store: s.Store}).Exchange(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), p.Coins)
			s.reply(w, r, 201, v, e)
		})
		r.Get("/gift-redemptions", s.redemptionList(false))
		r.Post("/gift-redemptions", func(w http.ResponseWriter, r *http.Request) {
			var p struct {
				GiftID         string `json:"giftId"`
				ExpectedCostXu *int64 `json:"expectedCostXu"`
			}
			if !s.body(w, r, &p) {
				return
			}
			v, e := (&rewards.Service{Store: s.Store}).RedeemQuoted(r.Context(), user(r).ID, r.Header.Get("Idempotency-Key"), p.GiftID, p.ExpectedCostXu)
			s.reply(w, r, 201, v, e)
		})
		r.Post("/deals", func(w http.ResponseWriter, r *http.Request) {
			var p struct {
				Channel string `json:"channel"`
				Body    string `json:"body"`
			}
			if !s.body(w, r, &p) {
				return
			}
			e := s.Store.Limit(r.Context(), "deals:"+user(r).ID, 5)
			var v any
			if e == nil {
				v, e = (&community.Service{Store: s.Store}).Post(r.Context(), user(r).ID, p.Channel, p.Body)
			}
			s.reply(w, r, 201, v, e)
		})
		r.Put("/deals/{id}/likes/me", s.like(true))
		r.Delete("/deals/{id}/likes/me", s.like(false))
		r.Get("/notifications", func(w http.ResponseWriter, r *http.Request) {
			l, o := page(r)
			s.list(w, r, `SELECT jsonb_build_object('id',n.id,'title',n.title,'body',n.body,'createdAt',n.created_at,'read',EXISTS(SELECT 1 FROM notification_receipts WHERE notification_id=n.id AND user_id=$1)) FROM notifications n WHERE NOT deleted AND (recipient_id IS NULL OR recipient_id=$1) ORDER BY created_at DESC,id DESC LIMIT $2 OFFSET $3`, user(r).ID, l, o)
		})
		r.Put("/notifications/{id}/read-receipt", func(w http.ResponseWriter, r *http.Request) {
			_, e := s.Store.Pool.Exec(r.Context(), `INSERT INTO notification_receipts(notification_id,user_id) SELECT id,$2 FROM notifications WHERE id=$1 AND NOT deleted AND (recipient_id IS NULL OR recipient_id=$2) ON CONFLICT DO NOTHING`, chi.URLParam(r, "id"), user(r).ID)
			s.reply(w, r, 200, map[string]bool{"read": true}, e)
		})
		r.Post("/notification-read-batches", func(w http.ResponseWriter, r *http.Request) {
			_, e := s.Store.Pool.Exec(r.Context(), `INSERT INTO notification_receipts(notification_id,user_id) SELECT id,$1 FROM notifications WHERE NOT deleted AND (recipient_id IS NULL OR recipient_id=$1) ON CONFLICT DO NOTHING`, user(r).ID)
			s.reply(w, r, 200, map[string]bool{"read": true}, e)
		})
	})
}

const orderSQL = `SELECT jsonb_build_object('id',o.id,'userId',o.user_id,'name',u.name,'channel',o.channel,'productName',o.product_name,'value',o.value,'commission',o.commission,'cashback',o.cashback,'status',o.status,'sourceStatus',o.source_status,'internallyRejected',o.internally_rejected,'orderedAt',o.ordered_at,'externalId',o.external_id,'lineId',o.line_id,'publisher',o.publisher,'policyId',o.policy_id,'tierCode',o.tier_code,'sharePercent',o.share_bps::numeric/100) FROM orders o JOIN users u ON u.id=o.user_id`

func (s *Server) like(like bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e := (&community.Service{Store: s.Store}).Like(r.Context(), user(r).ID, chi.URLParam(r, "id"), like)
		s.reply(w, r, 200, map[string]bool{"liked": like}, e)
	}
}
func (s *Server) dashboard(w http.ResponseWriter, r *http.Request) {
	tx, e := s.Store.Pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		s.reply(w, r, 0, nil, e)
		return
	}
	defer tx.Rollback(r.Context())
	m, e := cashback.MembershipFor(r.Context(), s.Store.Queries.WithTx(tx), user(r).ID)
	if e != nil {
		s.reply(w, r, 0, nil, e)
		return
	}
	var raw []byte
	e = tx.QueryRow(r.Context(), `SELECT jsonb_build_object('pending',coalesce((SELECT sum(cashback) FROM orders WHERE user_id=$1 AND status='pending'),0),'approved',coalesce((SELECT sum(cashback) FROM orders WHERE user_id=$1 AND status='approved'),0),'available',(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),'held',(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='held'),'debt',(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='debt'),'giftHeld',(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='gift_held'),'totalOrders',(SELECT count(*) FROM orders WHERE user_id=$1),'pendingOrders',(SELECT count(*) FROM orders WHERE user_id=$1 AND status='pending'),'rejectedOrders',(SELECT count(*) FROM orders WHERE user_id=$1 AND status='rejected'),'unit','xu')`, user(r).ID).Scan(&raw)
	var v map[string]any
	if e == nil {
		e = json.Unmarshal(raw, &v)
	}
	if e == nil {
		v["approvedOrders"] = m.ApprovedOrders
		v["membership"] = m.Public()
		e = tx.Commit(r.Context())
	}
	s.reply(w, r, 200, v, e)
}
