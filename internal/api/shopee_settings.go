package api

import (
	"context"
	"github.com/go-chi/chi/v5"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/platform"
	"hoanxu/internal/shopeeconfig"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

func (s *Server) applyShopeeConfig(cfg shopeeconfig.Config, apply func()) {
	if apply != nil {
		apply()
	}
	s.Affiliate.ManagedConfig = true
	s.Affiliate.Enabled = cfg.Enabled
	s.Affiliate.TrackingVerified = cfg.TrackingVerified
	s.Affiliate.SchemaVerified = cfg.SchemaVerified
	s.Affiliate.PriceScale = cfg.PriceScale
	s.Affiliate.ClearCache()
	s.ConfigVersion = cfg.Version
}

// Reload settings on Shopee operations so another deployment changing the shared
// publisher immediately invalidates old proof. Never hold this lock for noVNC.
func (s *Server) browserConfigLock(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/") || (r.Method == "PUT" && r.URL.Path == "/api/v1/admin/browser/settings") {
			next.ServeHTTP(w, r)
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/api/v1")
		if s.PrepareBrowser != nil && (strings.HasPrefix(path, "/admin/browser") || path == "/affiliate-channels" || path == "/admin/affiliate-channels" || path == "/product-checks" || path == "/shopee/check" || path == "/affiliate-links") {
			s.browserMu.Lock()
			cfg, err := shopeeconfig.Load(r.Context(), s.Store, s.Origin)
			if err == nil && cfg.Version != s.ConfigVersion {
				var apply func()
				apply, err = s.PrepareBrowser(cfg)
				if err == nil {
					s.applyShopeeConfig(cfg, apply)
				}
			}
			s.browserMu.Unlock()
			if err != nil {
				s.reply(w, r, 0, nil, err)
				return
			}
		}
		s.browserMu.RLock()
		defer s.browserMu.RUnlock()
		next.ServeHTTP(w, r)
	})
}
func (s *Server) getShopeeSettings(w http.ResponseWriter, r *http.Request) {
	if user(r).Role != "admin" {
		s.reply(w, r, 0, nil, platform.Fail(403, "FORBIDDEN", "Chỉ quản trị viên được cấu hình Shopee/Chrome."))
		return
	}
	cfg, err := shopeeconfig.Load(r.Context(), s.Store, s.Origin)
	cfg.VerifiedFingerprint = ""
	s.reply(w, r, 200, cfg, err)
}
func (s *Server) putShopeeSettings(w http.ResponseWriter, r *http.Request) {
	if user(r).Role != "admin" {
		s.reply(w, r, 0, nil, platform.Fail(403, "FORBIDDEN", "Chỉ quản trị viên được cấu hình Shopee/Chrome."))
		return
	}
	var in shopeeconfig.Input
	if !s.body(w, r, &in) {
		return
	}
	in.Publisher = strings.TrimSpace(in.Publisher)
	if err := in.Validate(); err != nil {
		s.reply(w, r, 0, nil, err)
		return
	}
	s.browserMu.Lock()
	defer s.browserMu.Unlock()
	current, err := shopeeconfig.Load(r.Context(), s.Store, s.Origin)
	if err != nil {
		s.reply(w, r, 0, nil, err)
		return
	}
	candidate := current
	candidate.Fields = in.Fields
	var apply func()
	if s.PrepareBrowser != nil {
		apply, err = s.PrepareBrowser(candidate)
		if err != nil {
			s.reply(w, r, 0, nil, err)
			return
		}
	}
	cfg, err := shopeeconfig.Save(r.Context(), s.Store, s.Origin, user(r).ID, in)
	if err == nil {
		s.verificationMu.Lock()
		if s.cancelVerification != nil {
			s.cancelVerification()
		}
		s.verificationMu.Unlock()
		s.applyShopeeConfig(cfg, apply)
	}
	cfg.VerifiedFingerprint = ""
	s.reply(w, r, 200, cfg, err)
}
func (s *Server) startShopeeVerification(w http.ResponseWriter, r *http.Request) {
	if user(r).Role != "admin" {
		s.reply(w, r, 0, nil, platform.Fail(403, "FORBIDDEN", "Chỉ quản trị viên được cấu hình Shopee/Chrome."))
		return
	}
	if err := s.Store.Limit(r.Context(), "shopee-verification:"+user(r).ID, 6); err != nil {
		s.reply(w, r, 0, nil, err)
		return
	}
	var in struct {
		Version    string `json:"version"`
		ProductURL string `json:"productUrl"`
	}
	if !s.body(w, r, &in) {
		return
	}
	if len(in.ProductURL) > 2048 || !strings.HasPrefix(in.ProductURL, "https://") {
		s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_URL", "Link sản phẩm Shopee không hợp lệ."))
		return
	}
	// Resolve validates the allowed Shopee hosts; reject arbitrary hosts before a job.
	c, stop := context.WithTimeout(r.Context(), 8*time.Second)
	_, _, canonical, err := affiliate.Resolve(c, in.ProductURL)
	stop()
	if err != nil {
		s.reply(w, r, 0, nil, platform.Fail(422, "INVALID_URL", err.Error()))
		return
	}
	v, cfg, err := shopeeconfig.StartVerification(r.Context(), s.Store, s.Origin, user(r).ID, in.Version, canonical)
	if err != nil {
		s.reply(w, r, 0, nil, err)
		return
	}
	life := s.Lifetime
	if life == nil {
		life = context.Background()
	}
	ctx, cancel := context.WithDeadline(life, v.ExpiresAt)
	s.verificationMu.Lock()
	s.cancelVerification = cancel
	s.verificationMu.Unlock()
	verifier := s.VerifyShopee
	b := s.Affiliate.Browser
	s.verificationWG.Add(1)
	go func() {
		defer s.verificationWG.Done()
		defer cancel()
		stage := func(value string) {
			if _, e := s.Store.Pool.Exec(ctx, `UPDATE shopee_verifications SET status='running',stage=$2 WHERE id=$1 AND status IN ('queued','running')`, v.ID, value); e != nil {
				slog.Warn("shopee_verification_stage_failed", "error", e)
			}
		}
		var checkErr error
		if verifier != nil {
			stage("running")
			checkErr = verifier(ctx, cfg, canonical)
		} else {
			checkErr = affiliate.VerifyIntegration(ctx, s.Store, b, cfg, canonical, stage)
		}
		finish, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		if e := shopeeconfig.FinishVerification(finish, s.Store, s.Origin, v.ID, checkErr); e != nil {
			slog.Error("shopee_verification_finish_failed", "error", e)
		}
	}()
	s.reply(w, r, 202, v, nil)
}
func (s *Server) WaitVerifications() {
	s.verificationMu.Lock()
	if s.cancelVerification != nil {
		s.cancelVerification()
	}
	s.verificationMu.Unlock()
	s.verificationWG.Wait()
}
func (s *Server) getShopeeVerification(w http.ResponseWriter, r *http.Request) {
	if user(r).Role != "admin" {
		s.reply(w, r, 0, nil, platform.Fail(403, "FORBIDDEN", "Chỉ quản trị viên được cấu hình Shopee/Chrome."))
		return
	}
	id := chi.URLParam(r, "id")
	if !platform.ID(id) {
		s.reply(w, r, 0, nil, platform.Fail(404, "NOT_FOUND", "Không tìm thấy tác vụ kiểm tra."))
		return
	}
	v, err := shopeeconfig.GetVerification(r.Context(), s.Store, s.Origin, id)
	s.reply(w, r, 200, v, err)
}
