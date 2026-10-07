package localclone

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"

	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
)

// VerifyExisting resumes a restored clone without touching or reconnecting to production.
func VerifyExisting(ctx context.Context, targetURL, directory string) (Manifest, error) {
	var expected Manifest
	if err := ValidateTarget(targetURL); err != nil {
		return expected, err
	}
	raw, err := os.ReadFile(filepath.Join(directory, "source-manifest.json"))
	if err != nil {
		return expected, err
	}
	if err = json.Unmarshal(raw, &expected); err != nil {
		return expected, err
	}
	conn, err := pgx.Connect(ctx, targetURL)
	if err != nil {
		return expected, err
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return expected, err
	}
	actual, err := Inspect(ctx, tx)
	tx.Rollback(context.Background())
	if err != nil {
		return actual, err
	}
	if !reflect.DeepEqual(expected, actual) {
		return actual, fmt.Errorf("clone verification mismatch")
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		return actual, err
	}
	role := conn.Config().Database + "_app"
	password := platform.Token()
	if _, err = conn.Exec(ctx, `ALTER ROLE `+pgx.Identifier{role}.Sanitize()+` PASSWORD '`+password+`'`); err != nil {
		return actual, err
	}
	u.User = url.UserPassword(role, password)
	q := u.Query()
	q.Set("search_path", "hoanxu,extensions,public")
	u.RawQuery = q.Encode()
	if err = os.WriteFile(filepath.Join(directory, "local-runtime.env"), []byte("DATABASE_URL="+u.String()+"\nMIGRATION_DATABASE_URL="+u.String()+"\nAPP_ENV=development\n"), 0600); err != nil {
		return actual, err
	}
	raw, _ = json.MarshalIndent(actual, "", "  ")
	err = os.WriteFile(filepath.Join(directory, "verified-manifest.json"), raw, 0600)
	return actual, err
}
