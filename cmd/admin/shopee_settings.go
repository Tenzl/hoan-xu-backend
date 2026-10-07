package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/platform"
	"hoanxu/internal/shopeeconfig"
	"os"
	"strconv"
	"time"
)

// One-time explicit import. The API never reads the retired Chrome/Shopee envs.
func importShopeeSettings() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()
	store, err := platform.New(pool, os.Getenv("DATA_ENCRYPTION_KEY"))
	if err != nil {
		return err
	}
	origin := os.Getenv("APP_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3000"
	}
	var exists bool
	if err = pool.QueryRow(ctx, `SELECT coalesce(settings->'runtimeConfigs' ? $1,false) FROM affiliate_channels WHERE id='shopee'`, shopeeconfig.Key(origin)).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return fmt.Errorf("Shopee settings already exist for this origin; update them in admin")
	}
	cfg := shopeeconfig.Default()
	cfg.Enabled = false // New configuration must pass native verification first.
	cfg.TrackingVerified = os.Getenv("SHOPEE_TRACKING_VERIFIED") == "true"
	cfg.SchemaVerified = os.Getenv("SHOPEE_SCHEMA_VERIFIED") == "true"
	if value := os.Getenv("SHOPEE_PRICE_SCALE"); value != "" && value != "0" {
		cfg.PriceScale, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return err
		}
	}
	if value := os.Getenv("BROWSER_MODE"); value != "" {
		cfg.Mode = value
	}
	cfg.ExecutablePath = os.Getenv("CHROME_PATH")
	if value := os.Getenv("CHROME_PROFILE"); value != "" {
		cfg.ProfilePath = value
	}
	if value := os.Getenv("CHROME_REMOTE_URL"); value != "" {
		cfg.RemoteURL = value
	}
	old, err := shopeeconfig.Load(ctx, store, origin)
	if err != nil {
		return err
	}
	cfg.Publisher = old.Publisher
	if _, err = shopeeconfig.Save(ctx, store, origin, "", shopeeconfig.Input{Fields: cfg.Fields, Version: old.Version}); err != nil {
		return err
	}
	fmt.Println("Shopee/Chrome settings imported into the private channel configuration.")
	return nil
}

func operatorStore(ctx context.Context) (*platform.Store, string, func(), error) {
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return nil, "", nil, err
	}
	store, err := platform.New(pool, os.Getenv("DATA_ENCRYPTION_KEY"))
	if err != nil {
		pool.Close()
		return nil, "", nil, err
	}
	origin := os.Getenv("APP_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3000"
	}
	return store, origin, pool.Close, nil
}
func loginShopee() error {
	ctx := context.Background()
	store, origin, closeStore, err := operatorStore(ctx)
	if err != nil {
		return err
	}
	defer closeStore()
	cfg, err := shopeeconfig.Load(ctx, store, origin)
	if err != nil {
		return err
	}
	manager, err := cfg.NewBrowser()
	if err != nil {
		return err
	}
	defer manager.Close()
	if err = manager.OpenInteractive(); err != nil {
		return err
	}
	fmt.Println("Đăng nhập thủ công. Nhấn Enter khi dashboard sẵn sàng. Chrome dùng cấu hình database; dừng backend browser trước khi dùng lệnh này.")
	_, err = bufio.NewReader(os.Stdin).ReadString('\n')
	return err
}

// Operator acceptance uses the same verification and proof flow as the admin API.
func verifyShopeeSettings(args []string) error {
	fs := flag.NewFlagSet("verify-shopee-settings", flag.ContinueOnError)
	product := fs.String("product-url", "", "Shopee product URL for native acceptance")
	enable := fs.Bool("enable", false, "enable customer links only after successful native acceptance")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *product == "" {
		return fmt.Errorf("--product-url is required")
	}
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, origin, closeStore, err := operatorStore(life)
	if err != nil {
		return err
	}
	defer closeStore()
	cfg, err := shopeeconfig.Load(life, store, origin)
	if err != nil {
		return err
	}
	v, cfg, err := shopeeconfig.StartVerification(life, store, origin, "", cfg.Version, *product)
	if err != nil {
		return err
	}
	ctx, stop := context.WithDeadline(life, v.ExpiresAt)
	defer stop()
	manager, err := cfg.NewBrowser()
	if err == nil {
		err = manager.OpenInteractive()
	}
	if err == nil {
		done := make(chan struct{})
		go func() { manager.Run(life); close(done) }()
		err = affiliate.VerifyIntegration(ctx, store, manager, cfg, *product, func(stage string) {
			_, _ = store.Pool.Exec(ctx, `UPDATE shopee_verifications SET status='running',stage=$2 WHERE id=$1`, v.ID, stage)
			fmt.Println("Checking:", stage)
		})
		cancel()
		<-done
	} else if manager != nil {
		manager.Close()
	}
	finish, finished := context.WithTimeout(context.Background(), 10*time.Second)
	defer finished()
	if e := shopeeconfig.FinishVerification(finish, store, origin, v.ID, err); e != nil {
		return e
	}
	if err != nil {
		return err
	}
	result, e := shopeeconfig.GetVerification(finish, store, origin, v.ID)
	if e != nil {
		return e
	}
	if result.Status != "succeeded" {
		return fmt.Errorf("verification did not succeed: %s", result.Status)
	}
	if *enable {
		current, e := shopeeconfig.Load(finish, store, origin)
		if e != nil {
			return e
		}
		current.Enabled = true
		if _, e = shopeeconfig.Save(finish, store, origin, "", shopeeconfig.Input{Fields: current.Fields, Version: current.Version}); e != nil {
			return e
		}
	}
	fmt.Println("Native Shopee product and signed GQL tracking verified; no customer links, orders or wallet entries created.")
	return nil
}
