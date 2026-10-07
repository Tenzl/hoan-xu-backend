package localclone

import (
	"fmt"
	"net"
	"net/url"
	"strings"
)

// ValidateTarget protects the restore destination independently of APP_ENV.
func ValidateTarget(connection string) error {
	u, err := url.Parse(connection)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return fmt.Errorf("a PostgreSQL target URL is required")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("restore target must be loopback")
	}
	database := strings.TrimPrefix(u.Path, "/")
	if database != "hoanxu_local" && !strings.HasPrefix(database, "hoanxu_local_") {
		return fmt.Errorf("restore target must be hoanxu_local or hoanxu_local_<snapshot>")
	}
	for _, c := range database {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return fmt.Errorf("invalid local database name")
		}
	}
	return nil
}
