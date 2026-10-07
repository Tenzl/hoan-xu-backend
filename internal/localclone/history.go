package localclone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"path/filepath"
)

// VerifyHistory checks unchanged business rows after additive migrations.
func VerifyHistory(ctx context.Context, connection, directory, private string) (int, int, error) {
	if err := ValidateTarget(connection); err != nil {
		return 0, 0, err
	}
	raw, err := os.ReadFile(filepath.Join(directory, "source-manifest.json"))
	if err != nil {
		return 0, 0, err
	}
	var expected Manifest
	if err = json.Unmarshal(raw, &expected); err != nil {
		return 0, 0, err
	}
	conn, err := pgx.Connect(ctx, connection)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close(context.Background())
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, `SET LOCAL timezone='UTC';SET LOCAL search_path=hoanxu,extensions,public`); err != nil {
		return 0, 0, err
	}
	tables := []string{"users", "auth_identities", "internal_credentials", "user_permissions", "cashback_policies", "cashback_tiers", "orders", "order_items", "order_events", "wallet_accounts", "wallet_transactions", "wallet_entries", "coin_accounts", "coin_transactions", "checkins", "withdrawals", "withdrawal_events", "gift_catalog", "gift_redemptions", "audit_logs", "notifications", "notification_receipts", "affiliate_links"}
	actual := map[string]Table{}
	for _, name := range tables {
		want, exists := expected.Tables[name]
		if !exists {
			continue
		}
		expr := "to_jsonb(t)"
		if name == "orders" {
			expr += "-'internally_rejected'"
		}
		if name == "affiliate_links" {
			expr += "-'product_name'"
		}
		rows, e := tx.Query(ctx, `SELECT (`+expr+`)::text FROM `+pgx.Identifier{"hoanxu", name}.Sanitize()+` t ORDER BY (`+expr+`)::text COLLATE "C"`)
		if e != nil {
			return 0, 0, e
		}
		h := sha256.New()
		var count int64
		for rows.Next() {
			var value string
			if e = rows.Scan(&value); e != nil {
				rows.Close()
				return 0, 0, e
			}
			h.Write([]byte(value))
			h.Write([]byte{0})
			count++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return 0, 0, e
		}
		got := Table{Rows: count, Digest: hex.EncodeToString(h.Sum(nil))}
		actual[name] = got
		if got != want {
			return 0, 0, fmt.Errorf("historical data changed: %s; preserve snapshot and investigate", name)
		}
	}
	var ledger int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT a.id FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0)) x`).Scan(&ledger); err != nil {
		return 0, 0, err
	}
	if ledger != 0 {
		return 0, 0, fmt.Errorf("historical ledger mismatch")
	}
	rows, err := tx.Query(ctx, `SELECT id::text,path FROM private_files`)
	if err != nil {
		return 0, 0, err
	}
	missing := []string{}
	for rows.Next() {
		var id, path string
		if err = rows.Scan(&id, &path); err != nil {
			rows.Close()
			return 0, 0, err
		}
		if path == "" || filepath.Base(path) != path {
			missing = append(missing, id)
			continue
		}
		if _, err = os.Stat(filepath.Join(private, path)); os.IsNotExist(err) {
			missing = append(missing, id)
		} else if err != nil {
			rows.Close()
			return 0, 0, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, 0, err
	}
	report := map[string]any{"verifiedTables": actual, "ledgerMismatches": ledger, "missingPrivateFileIds": missing}
	raw, err = json.MarshalIndent(report, "", "  ")
	if err != nil {
		return 0, 0, err
	}
	if err = os.WriteFile(filepath.Join(directory, "post-migration-history.json"), raw, 0600); err != nil {
		return 0, 0, err
	}
	return len(actual), len(missing), nil
}
