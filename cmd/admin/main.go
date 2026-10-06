package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"github.com/chromedp/chromedp"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"hoanxu/internal/auth"
	"hoanxu/internal/platform"
	"os"
	"path/filepath"
)

func main() {
	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = ".env"
	}
	_ = godotenv.Load(envFile)
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("commands: migrate, create, reset, shopee-login, key, import-legacy")
	}
	command := os.Args[1]
	if command == "import-legacy" {
		return importLegacy(os.Args[2:])
	}
	if command == "key" {
		b := make([]byte, 32)
		if _, e := rand.Read(b); e != nil {
			return e
		}
		fmt.Println(base64.StdEncoding.EncodeToString(b))
		return nil
	}
	if command == "migrate" {
		root, e := filepath.Abs("database/migrations")
		if e != nil {
			return e
		}
		source := "file://" + filepath.ToSlash(root)
		connection := os.Getenv("MIGRATION_DATABASE_URL")
		if connection == "" {
			connection = os.Getenv("DATABASE_URL")
		}
		m, e := migrate.New(source, connection)
		if e != nil {
			return e
		}
		defer m.Close()
		e = m.Up()
		if e == migrate.ErrNoChange {
			return nil
		}
		return e
	}
	if command == "shopee-login" {
		root, e := filepath.Abs(os.Getenv("CHROME_PROFILE"))
		if e != nil {
			return e
		}
		opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
		opts = append(opts, chromedp.UserDataDir(root), chromedp.Flag("headless", false))
		if p := os.Getenv("CHROME_PATH"); p != "" {
			opts = append(opts, chromedp.ExecPath(p))
		}
		ctx, c := chromedp.NewExecAllocator(context.Background(), opts...)
		defer c()
		ctx, close := chromedp.NewContext(ctx)
		defer close()
		if e = chromedp.Run(ctx, chromedp.Navigate("https://affiliate.shopee.vn/dashboard")); e != nil {
			return e
		}
		fmt.Println("Đăng nhập thủ công. Nhấn Enter sau khi dashboard sẵn sàng. Dừng backend browser trước khi dùng lệnh này.")
		_, e = bufio.NewReader(os.Stdin).ReadString('\n')
		return e
	}
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	username := fs.String("username", "", "username")
	name := fs.String("name", "Quản trị", "display name")
	role := fs.String("role", "admin", "admin or staff")
	id := fs.String("id", "", "user ID for reset")
	if e := fs.Parse(os.Args[2:]); e != nil {
		return e
	}
	password := os.Getenv("ADMIN_PASSWORD")
	if password == "" {
		return errors.New("Set ADMIN_PASSWORD environment variable; do not pass passwords as command arguments")
	}
	ctx := context.Background()
	pool, e := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if e != nil {
		return e
	}
	defer pool.Close()
	s, e := platform.New(pool, os.Getenv("DATA_ENCRYPTION_KEY"))
	if e != nil {
		return e
	}
	switch command {
	case "create":
		uid, e := auth.CreateInternal(ctx, s, "", *username, *name, password, *role, nil)
		if e != nil {
			return e
		}
		fmt.Println("Created internal account:", uid)
	case "reset":
		if !platform.ID(*id) {
			return errors.New("valid --id required")
		}
		a := &auth.Service{Store: s}
		if e = a.ResetInternal(ctx, "", *id, password, nil, false); e != nil {
			return e
		}
		fmt.Println("Password reset; sessions revoked.")
	default:
		return errors.New("unknown command")
	}
	return nil
}
