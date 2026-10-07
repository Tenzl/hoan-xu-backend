package envguard

import (
	"fmt"
	"github.com/jackc/pgx/v5"
	"net"
	"strings"
)

func Validate(connection, mode string) error {
	cfg, err := pgx.ParseConfig(connection)
	if err != nil {
		return fmt.Errorf("invalid database connection")
	}
	ip := net.ParseIP(cfg.Host)
	local := cfg.Host == "localhost" || ip != nil && ip.IsLoopback()
	switch mode {
	case "development":
		if !local {
			return fmt.Errorf("development requires a loopback database; use the verified local clone")
		}
	case "test":
		if !local || !strings.HasSuffix(cfg.Database, "_test") {
			return fmt.Errorf("test requires a loopback database ending in _test")
		}
	case "production", "":
	default:
		return fmt.Errorf("APP_ENV must be development, test or production")
	}
	return nil
}

func SeedAllowed(connection, mode string) error {
	if mode != "test" {
		return fmt.Errorf("generated data is allowed only with APP_ENV=test on an isolated _test database")
	}
	return Validate(connection, mode)
}
