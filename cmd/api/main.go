package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/api"
	"hoanxu/internal/auth"
	"hoanxu/internal/envguard"
	"hoanxu/internal/imports"
	"hoanxu/internal/platform"
	"hoanxu/internal/privatefiles"
	"hoanxu/internal/remotebrowser"
	"hoanxu/internal/shopeeconfig"
	"hoanxu/internal/wallet"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	profile := os.Getenv("ENV_FILE")
	if profile == "" {
		profile = ".env"
		if _, err := os.Stat(".env.local"); err == nil {
			profile = ".env.local"
		}
	}
	_ = godotenv.Load(profile)
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
	if err := envguard.Validate(databaseURL, os.Getenv("APP_ENV")); err != nil {
		return err
	}
	connection, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return err
	}
	slog.Info("database_target", "environment", env("APP_ENV", "production"), "host", connection.ConnConfig.Host, "database", connection.ConnConfig.Database)
	proxyKey := os.Getenv("PROXY_SIGNING_KEY")
	if proxyKey != "" {
		secret, err := hex.DecodeString(proxyKey)
		if err != nil || len(secret) != 32 {
			return fmt.Errorf("PROXY_SIGNING_KEY must contain 64 hexadecimal characters")
		}
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
	origin := env("APP_ORIGIN", "http://localhost:3000")
	if e = shopeeconfig.RecoverVerifications(ctx, store, origin); e != nil {
		return e
	}
	cfg, e := shopeeconfig.Load(ctx, store, origin)
	if e != nil {
		return e
	}
	b, e := cfg.NewBrowser()
	if e != nil {
		return e
	}
	var runtimeMu sync.Mutex
	browserCtx, browserStop := context.WithCancel(ctx)
	browserDone := make(chan struct{})
	initialCtx, initialDone := browserCtx, browserDone
	go func() { b.Run(initialCtx); close(initialDone) }()
	defer func() { runtimeMu.Lock(); defer runtimeMu.Unlock(); browserStop(); <-browserDone }()
	aff := &affiliate.Service{Store: store, Browser: b, ManagedConfig: true, Enabled: cfg.Enabled, TrackingVerified: cfg.TrackingVerified, SchemaVerified: cfg.SchemaVerified, PriceScale: cfg.PriceScale}
	aff.Lifetime = ctx
	private, e := filepath.Abs(env("PRIVATE_DIR", "private-data/files"))
	if e != nil {
		return e
	}
	server := &api.Server{Store: store, Auth: a, Affiliate: aff, Origin: env("APP_ORIGIN", "http://localhost:3000"), Secure: os.Getenv("COOKIE_SECURE") == "true", PrivateDir: private}
	server.ProxySigningKey = proxyKey
	defer server.WaitVerifications()
	if os.Getenv("REMOTE_BROWSER_ENABLED") == "true" {
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
	go maintenance(ctx, store, private)
	// A native Chrome window is available only on a loopback development server.
	server.LocalBrowser = cfg.Mode == "local" && env("HOST", "127.0.0.1") == "127.0.0.1"
	server.BrowserMode = cfg.Mode
	server.ConfigVersion = cfg.Version
	server.Lifetime = ctx
	server.PrepareBrowser = func(next shopeeconfig.Config) (func(), error) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// Gate/rate changes retain the authenticated browser. Chrome connection changes
		// wait for current API operations, close owned tabs and keep the disk profile.
		same := next.Mode == cfg.Mode && next.ExecutablePath == cfg.ExecutablePath && next.ProfilePath == cfg.ProfilePath && next.RemoteURL == cfg.RemoteURL
		if same {
			return func() { runtimeMu.Lock(); defer runtimeMu.Unlock(); cfg = next }, nil
		}
		replacement, err := next.NewBrowser()
		if err != nil {
			return nil, err
		}
		return func() {
			runtimeMu.Lock()
			defer runtimeMu.Unlock()
			browserStop()
			<-browserDone
			browserCtx, browserStop = context.WithCancel(ctx)
			browserDone = make(chan struct{})
			currentCtx, currentDone := browserCtx, browserDone
			go func() { replacement.Run(currentCtx); close(currentDone) }()
			aff.Browser = replacement
			cfg = next
			server.BrowserMode = next.Mode
			server.LocalBrowser = next.Mode == "local" && env("HOST", "127.0.0.1") == "127.0.0.1"
		}, nil
	}
	srv := &http.Server{Addr: env("HOST", "127.0.0.1") + ":" + env("PORT", "8080"), Handler: api.New(server), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		c, close := context.WithTimeout(context.Background(), 10*time.Second)
		defer close()
		_ = srv.Shutdown(c)
	}()
	slog.Info("api_started", "address", srv.Addr)
	e = srv.ListenAndServe()
	if e == http.ErrServerClosed {
		<-shutdownDone
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
func maintenance(ctx context.Context, s *platform.Store, private string) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	// Also catch links that expired while the backend was stopped.
	purgeLinks := func() {
		c, done := context.WithTimeout(ctx, 30*time.Second)
		defer done()
		if count, err := (&affiliate.Service{Store: s}).PurgeExpired(c, time.Now().UTC()); err != nil {
			slog.Error("link_cleanup_failed", "error", err)
		} else if count > 0 {
			slog.Info("saved_links_deleted", "count", count)
		}
	}
	purgeLinks()
	lastFull := time.Time{}
	reconcile := func() {
		check, done := context.WithTimeout(ctx, 30*time.Second)
		defer done()
		var n int
		var err error
		if time.Since(lastFull) >= time.Hour {
			n, err = wallet.FullReconcile(check, s)
			if err == nil {
				lastFull = time.Now()
			}
		} else {
			n, err = wallet.Reconcile(check, s, 100)
		}
		if err != nil {
			slog.Error("ledger_reconciliation_failed", "error", err)
		} else if n > 0 {
			slog.Error("ledger_mismatch", "accounts", n)
		}
	}
	reconcile()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			purgeLinks()
			cleanup, done := context.WithTimeout(ctx, 30*time.Second)
			if e := privatefiles.Cleanup(cleanup, s, private); e != nil {
				slog.Error("private_file_cleanup_failed", "error", e)
			}
			done()
			if _, e := s.Pool.Exec(ctx, `UPDATE link_operations SET status='indeterminate',updated_at=now() WHERE status='running' AND created_at<now()-interval '2 minutes'`); e != nil {
				slog.Error("link_operation_recovery_failed", "error", e)
			}
			for _, q := range []string{`DELETE FROM rate_limit_buckets WHERE expires_at<now()`, `DELETE FROM oauth_requests WHERE expires_at<now()`, `DELETE FROM sessions WHERE expires_at<now()`} {
				if _, e := s.Pool.Exec(ctx, q); e != nil {
					slog.Error("maintenance_failed", "error", e)
				}
			}
			reconcile()
		}
	}
}
