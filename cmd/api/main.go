package main

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/api"
	"hoanxu/internal/auth"
	"hoanxu/internal/browser"
	"hoanxu/internal/imports"
	"hoanxu/internal/platform"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

func main() {
	_ = godotenv.Load()
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if e := run(); e != nil {
		slog.Error("startup_failed", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer pool.Close()
	if e = pool.Ping(ctx); e != nil {
		return e
	}
	store, e := platform.New(pool, os.Getenv("DATA_ENCRYPTION_KEY"))
	if e != nil {
		return e
	}
	a := &auth.Service{Store: store}
	googleCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	e = a.ConfigureGoogle(googleCtx, os.Getenv("GOOGLE_CLIENT_ID"), os.Getenv("GOOGLE_CLIENT_SECRET"), env("GOOGLE_CALLBACK_URL", "http://localhost:3000/api/v1/auth/google/callback"))
	cancel()
	if e != nil {
		slog.Warn("google_unavailable", "error", e)
	}
	profile, e := filepath.Abs(env("CHROME_PROFILE", "private-data/chrome-profile"))
	if e != nil {
		return e
	}
	enabled := os.Getenv("SHOPEE_ENABLED") == "true"
	b := browser.NewManaged(os.Getenv("CHROME_PATH"), profile, &browser.CookieStore{Path: filepath.Join(filepath.Dir(profile), "shopee-cookies.enc"), Codec: store}, enabled, browser.WithHeadless(env("CHROME_HEADLESS", "true") != "false"))
	go b.Run(ctx)
	scale, _ := strconv.ParseInt(os.Getenv("SHOPEE_PRICE_SCALE"), 10, 64)
	aff := &affiliate.Service{Store: store, Browser: b, Enabled: enabled, TrackingVerified: os.Getenv("SHOPEE_TRACKING_VERIFIED") == "true", Publisher: os.Getenv("SHOPEE_PUBLISHER"), SchemaVerified: os.Getenv("SHOPEE_SCHEMA_VERIFIED") == "true", PriceScale: scale}
	private, e := filepath.Abs(env("PRIVATE_DIR", "private-data/files"))
	if e != nil {
		return e
	}
	server := &api.Server{Store: store, Auth: a, Affiliate: aff, Origin: env("APP_ORIGIN", "http://localhost:3000"), Secure: os.Getenv("COOKIE_SECURE") == "true", PrivateDir: private}
	go (&imports.Service{Store: store}).Run(ctx)
	go maintenance(ctx, store)
	srv := &http.Server{Addr: "127.0.0.1:" + env("PORT", "8080"), Handler: api.New(server), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		c, close := context.WithTimeout(context.Background(), 10*time.Second)
		defer close()
		_ = srv.Shutdown(c)
	}()
	slog.Info("api_started", "address", srv.Addr)
	e = srv.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
func maintenance(ctx context.Context, s *platform.Store) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			for _, q := range []string{`DELETE FROM rate_limit_buckets WHERE expires_at<now()`, `DELETE FROM oauth_requests WHERE expires_at<now()`, `DELETE FROM sessions WHERE expires_at<now()`} {
				if _, e := s.Pool.Exec(ctx, q); e != nil {
					slog.Error("maintenance_failed", "error", e)
				}
			}
			var n int
			e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT a.id FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0)) x`).Scan(&n)
			if e == nil && n > 0 {
				slog.Error("ledger_mismatch", "accounts", n)
			}
		}
	}
}
