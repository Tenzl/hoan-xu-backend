package main

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/api"
	"hoanxu/internal/auth"
	"hoanxu/internal/browser"
	"hoanxu/internal/imports"
	"hoanxu/internal/platform"
	"hoanxu/internal/remotebrowser"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func main() {
	_ = godotenv.Load(env("ENV_FILE", ".env"))
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if e := run(); e != nil {
		slog.Error("startup_failed", "error", e)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	databaseURL := strings.TrimSpace(os.Getenv("DATABASE_URL"))
	if databaseURL == "" {
		return fmt.Errorf("DATABASE_URL is required; set it in the service environment before starting the API")
	}
	pool, e := pgxpool.New(ctx, databaseURL)
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
	enabled := os.Getenv("SHOPEE_ENABLED") == "true"
	mode := env("BROWSER_MODE", "local")
	var b *browser.Manager
	switch mode {
	case "remote":
		b, e = browser.NewRemote(env("CHROME_REMOTE_URL", "http://127.0.0.1:9222"))
		if e != nil {
			return e
		}
	case "local":
		profile, err := filepath.Abs(env("CHROME_PROFILE", "private-data/chrome-profile"))
		if err != nil {
			return err
		}
		b = browser.NewManual(os.Getenv("CHROME_PATH"), profile, browser.WithHeadless(env("CHROME_HEADLESS", "false") != "false"))
	default:
		return fmt.Errorf("BROWSER_MODE must be local or remote")
	}
	browserDone := make(chan struct{})
	go func() { b.Run(ctx); close(browserDone) }()
	defer func() { stop(); <-browserDone }()
	scale, _ := strconv.ParseInt(os.Getenv("SHOPEE_PRICE_SCALE"), 10, 64)
	aff := &affiliate.Service{Store: store, Browser: b, Enabled: enabled, TrackingVerified: os.Getenv("SHOPEE_TRACKING_VERIFIED") == "true", SchemaVerified: os.Getenv("SHOPEE_SCHEMA_VERIFIED") == "true", PriceScale: scale}
	private, e := filepath.Abs(env("PRIVATE_DIR", "private-data/files"))
	if e != nil {
		return e
	}
	server := &api.Server{Store: store, Auth: a, Affiliate: aff, Origin: env("APP_ORIGIN", "http://localhost:3000"), Secure: os.Getenv("COOKIE_SECURE") == "true", PrivateDir: private}
	if os.Getenv("REMOTE_BROWSER_ENABLED") == "true" {
		if mode == "local" && (env("CHROME_HEADLESS", "true") != "false" || os.Getenv("DISPLAY") == "") {
			return fmt.Errorf("remote browser requires CHROME_HEADLESS=false and DISPLAY")
		}
		bridgePassword := os.Getenv("REMOTE_BROWSER_BRIDGE_PASSWORD")
		if bridgePassword == "" {
			return fmt.Errorf("REMOTE_BROWSER_BRIDGE_PASSWORD is required")
		}
		server.RemoteBrowser, e = remotebrowser.New(a, os.Getenv("REMOTE_BROWSER_ORIGIN"), remotebrowser.WithBridgePassword(bridgePassword), remotebrowser.WithUpstream(env("REMOTE_BROWSER_UPSTREAM", "http://127.0.0.1:6080")))
		if e != nil {
			return e
		}
	}
	go (&imports.Service{Store: store}).Run(ctx)
	go maintenance(ctx, store)
	// A native Chrome window is available only on a loopback development server.
	server.LocalBrowser = mode == "local" && server.RemoteBrowser == nil && env("HOST", "127.0.0.1") == "127.0.0.1"
	srv := &http.Server{Addr: env("HOST", "127.0.0.1") + ":" + env("PORT", "8080"), Handler: api.New(server), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
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
	// Also catch links that expired while the backend was stopped.
	cancelExpired := func() {
		c, done := context.WithTimeout(ctx, 30*time.Second)
		defer done()
		if err := (&affiliate.Service{Store: s}).CancelExpired(c, time.Now().UTC()); err != nil {
			slog.Error("link_cancellation_failed", "error", err)
		}
	}
	cancelExpired()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cancelExpired()
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
