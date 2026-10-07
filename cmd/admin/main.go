package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"hoanxu/internal/auth"
	"hoanxu/internal/envguard"
	"hoanxu/internal/platform"
	"os"
	"path/filepath"
)

func main() {
	envFile := os.Getenv("ENV_FILE")
	if envFile == "" {
		envFile = ".env"
		if _, err := os.Stat(".env.local"); err == nil {
			envFile = ".env.local"
		}
	}
	_ = godotenv.Load(envFile)
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return errors.New("commands: migrate, create, reset, shopee-login, import-shopee-settings, verify-shopee-settings, key, import-legacy")
	}
	command := os.Args[1]
	if command == "clone-local" {
		return cloneLocal(os.Args[2:])
	}
	if command == "verify-local" {
		return verifyLocal(os.Args[2:])
	}
	if command == "check-local-history" {
		return checkLocalHistory(os.Args[2:])
	}
	if command == "verify-shopee-settings" {
		return verifyShopeeSettings(os.Args[2:])
	}
	if command == "import-shopee-settings" {
		return importShopeeSettings()
	}
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
		fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
		allowProduction := fs.Bool("allow-production", false, "explicit production migration authorization")
		if e := fs.Parse(os.Args[2:]); e != nil {
			return e
		}
		root, e := filepath.Abs("database/migrations")
		if e != nil {
			return e
		}
		source := "file://" + filepath.ToSlash(root)
		connection := os.Getenv("MIGRATION_DATABASE_URL")
		if connection == "" {
			connection = os.Getenv("DATABASE_URL")
		}
		if e := envguard.Validate(connection, os.Getenv("APP_ENV")); e != nil {
			return e
		}
		if os.Getenv("APP_ENV") != "development" && os.Getenv("APP_ENV") != "test" && !*allowProduction {
			return errors.New("production migration requires an explicit ENV_FILE and --allow-production")
		}
		if *allowProduction && os.Getenv("ENV_FILE") == "" {
			return errors.New("set ENV_FILE explicitly for a production migration")
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
		return loginShopee()
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
