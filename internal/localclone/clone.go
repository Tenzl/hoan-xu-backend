package localclone

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"hoanxu/internal/platform"
)

type Table struct {
	Rows   int64  `json:"rows"`
	Digest string `json:"digest"`
}
type Manifest struct {
	Version                int              `json:"version"`
	Dirty                  bool             `json:"dirty"`
	Schema                 string           `json:"schemaDigest"`
	Tables                 map[string]Table `json:"tables"`
	LedgerMismatches       int              `json:"ledgerMismatches"`
	UnbalancedTransactions int              `json:"unbalancedTransactions"`
}

// Inspect runs on the same exported read-only snapshot as pg_dump.
func Inspect(ctx context.Context, tx pgx.Tx) (Manifest, error) {
	m := Manifest{Tables: map[string]Table{}}
	if _, err := tx.Exec(ctx, `SET LOCAL timezone='UTC'; SET LOCAL search_path=hoanxu,extensions,public`); err != nil {
		return m, err
	}
	if err := tx.QueryRow(ctx, `SELECT version,dirty FROM hoanxu.schema_migrations`).Scan(&m.Version, &m.Dirty); err != nil {
		return m, err
	}
	names, err := tx.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname='hoanxu' ORDER BY tablename`)
	if err != nil {
		return m, err
	}
	var tables []string
	for names.Next() {
		var name string
		if err = names.Scan(&name); err != nil {
			names.Close()
			return m, err
		}
		tables = append(tables, name)
	}
	err = names.Err()
	names.Close()
	if err != nil {
		return m, err
	}
	for _, name := range tables {
		rows, e := tx.Query(ctx, `SELECT to_jsonb(t)::text FROM `+pgx.Identifier{"hoanxu", name}.Sanitize()+` t ORDER BY to_jsonb(t)::text COLLATE "C"`)
		if e != nil {
			return m, e
		}
		h := sha256.New()
		var count int64
		for rows.Next() {
			var row string
			if e = rows.Scan(&row); e != nil {
				rows.Close()
				return m, e
			}
			h.Write([]byte(row))
			h.Write([]byte{0})
			count++
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return m, e
		}
		m.Tables[name] = Table{count, hex.EncodeToString(h.Sum(nil))}
	}
	// Compare definitions rather than owner/ACL/OIDs, which intentionally differ locally.
	err = tx.QueryRow(ctx, `SELECT md5(coalesce(string_agg(signature,E'\n' ORDER BY signature COLLATE "C"),'')) FROM (
 SELECT 'column:'||c.relname||':'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull||':'||coalesce(pg_get_expr(d.adbin,d.adrelid),'') signature FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum WHERE n.nspname='hoanxu' AND c.relkind IN ('r','p')
 UNION ALL SELECT 'constraint:'||c.relname||':'||k.conname||':'||CASE WHEN c.relname='affiliate_links' AND k.conname='link_share_range' AND pg_get_constraintdef(k.oid) !~ '\m(OR|NOT)\M|[+*/]' THEN regexp_replace(pg_get_constraintdef(k.oid),'[()]','','g') ELSE pg_get_constraintdef(k.oid) END FROM pg_constraint k JOIN pg_class c ON c.oid=k.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='hoanxu' AND k.contype<>'n'
 UNION ALL SELECT 'index:'||indexname||':'||indexdef FROM pg_indexes WHERE schemaname='hoanxu'
 UNION ALL SELECT 'rls:'||c.relname||':'||c.relrowsecurity||':'||c.relforcerowsecurity FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='hoanxu' AND c.relkind IN ('r','p')
 UNION ALL SELECT 'trigger:'||pg_get_triggerdef(t.oid) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='hoanxu' AND NOT t.tgisinternal
 UNION ALL SELECT 'function:'||pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='hoanxu' AND p.prokind='f'
 ) definitions`).Scan(&m.Schema)
	if err != nil {
		return m, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT a.id FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0)) x`).Scan(&m.LedgerMismatches)
	if err != nil {
		return m, err
	}
	err = tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT transaction_id FROM wallet_entries GROUP BY transaction_id HAVING sum(amount)<>0) x`).Scan(&m.UnbalancedTransactions)
	return m, err
}

func command(ctx context.Context, bin, tool string, cfg *pgx.ConnConfig, args ...string) error {
	cmd := exec.CommandContext(ctx, filepath.Join(bin, tool), args...)
	// Credentials are passed only in the child environment, never argv or logs.
	cmd.Env = append(os.Environ(), "PGHOST="+cfg.Host, "PGPORT="+strconv.Itoa(int(cfg.Port)), "PGUSER="+cfg.User, "PGPASSWORD="+cfg.Password, "PGDATABASE="+cfg.Database, "PGOPTIONS=", "PGSSLMODE="+func() string {
		if cfg.TLSConfig != nil {
			return "require"
		}
		return "disable"
	}())
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s failed: %w: %s", tool, err, strings.TrimSpace(string(output)))
	}
	return nil
}

func Run(ctx context.Context, sourceURL, targetURL, bin, destination string) (Manifest, error) {
	var empty Manifest
	if err := ValidateTarget(targetURL); err != nil {
		return empty, err
	}
	sourceCfg, err := pgx.ParseConfig(sourceURL)
	if err != nil {
		return empty, err
	}
	targetCfg, err := pgx.ParseConfig(targetURL)
	if err != nil {
		return empty, err
	}
	targetName := targetCfg.Database
	if sourceCfg.Host == targetCfg.Host && sourceCfg.Port == targetCfg.Port && sourceCfg.Database == targetName {
		return empty, fmt.Errorf("source and target must differ")
	}
	source, err := pgx.ConnectConfig(ctx, sourceCfg)
	if err != nil {
		return empty, err
	}
	defer source.Close(context.Background())
	tx, err := source.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return empty, err
	}
	defer tx.Rollback(context.Background())
	baseline, err := Inspect(ctx, tx)
	if err != nil {
		return empty, err
	}
	if baseline.Dirty {
		return empty, fmt.Errorf("production snapshot is dirty; refusing restore")
	}
	matches, err := filepath.Glob(filepath.Join("database", "migrations", fmt.Sprintf("%06d_*.up.sql", baseline.Version)))
	if err != nil || len(matches) != 1 {
		return empty, fmt.Errorf("missing local migration for snapshot version %d", baseline.Version)
	}
	if baseline.LedgerMismatches != 0 || baseline.UnbalancedTransactions != 0 {
		return empty, fmt.Errorf("source financial reconciliation failed")
	}
	var snapshot string
	if err = tx.QueryRow(ctx, `SELECT pg_export_snapshot()`).Scan(&snapshot); err != nil {
		return empty, err
	}
	if err = os.Mkdir(destination, 0700); err != nil {
		return empty, fmt.Errorf("create new backup directory: %w", err)
	}
	dump := filepath.Join(destination, "production.dump")
	if err = command(ctx, bin, "pg_dump", sourceCfg, "--format=custom", "--no-owner", "--no-acl", "--schema=hoanxu", "--snapshot="+snapshot, "--file="+dump); err != nil {
		return empty, err
	}
	raw, _ := json.MarshalIndent(baseline, "", "  ")
	if err = os.WriteFile(filepath.Join(destination, "source-manifest.json"), raw, 0600); err != nil {
		return empty, err
	}
	// Refuse overwrite even if an old local clone exists.
	adminCfg := targetCfg.Copy()
	adminCfg.Database = "postgres"
	admin, err := pgx.ConnectConfig(ctx, adminCfg)
	if err != nil {
		return empty, err
	}
	defer admin.Close(context.Background())
	var exists bool
	if err = admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname=$1)`, targetName).Scan(&exists); err != nil {
		return empty, err
	}
	if exists {
		return empty, fmt.Errorf("target database already exists; use a fresh hoanxu_local_<snapshot> name")
	}
	role, password := targetName+"_app", platform.Token()
	if _, err = admin.Exec(ctx, `CREATE ROLE `+pgx.Identifier{role}.Sanitize()+` LOGIN PASSWORD '`+password+`'`); err != nil {
		return empty, err
	}
	if _, err = admin.Exec(ctx, `CREATE DATABASE `+pgx.Identifier{targetName}.Sanitize()+` OWNER `+pgx.Identifier{role}.Sanitize()); err != nil {
		return empty, err
	}
	target, err := pgx.ConnectConfig(ctx, targetCfg)
	if err != nil {
		return empty, err
	}
	defer target.Close(context.Background())
	if _, err = target.Exec(ctx, `CREATE SCHEMA extensions; CREATE EXTENSION IF NOT EXISTS pgcrypto WITH SCHEMA extensions`); err != nil {
		return empty, err
	}
	if _, err = target.Exec(ctx, `GRANT USAGE ON SCHEMA extensions TO `+pgx.Identifier{role}.Sanitize()); err != nil {
		return empty, err
	}
	if err = command(ctx, bin, "pg_restore", targetCfg, "--exit-on-error", "--no-owner", "--no-acl", "--role="+role, "--dbname="+targetName, dump); err != nil {
		return empty, err
	}
	check, err := target.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return empty, err
	}
	restored, err := Inspect(ctx, check)
	check.Rollback(context.Background())
	if err != nil {
		return empty, err
	}
	raw, _ = json.MarshalIndent(restored, "", "  ")
	if err = os.WriteFile(filepath.Join(destination, "restored-manifest.json"), raw, 0600); err != nil {
		return empty, err
	}
	if !reflect.DeepEqual(baseline, restored) {
		return empty, fmt.Errorf("clone differs from production snapshot; preserve backup for investigation")
	}
	localURL, _ := url.Parse(targetURL)
	localURL.User = url.UserPassword(role, password)
	q := localURL.Query()
	q.Set("search_path", "hoanxu,extensions,public")
	localURL.RawQuery = q.Encode()
	if err = os.WriteFile(filepath.Join(destination, "local-runtime.env"), []byte("DATABASE_URL="+localURL.String()+"\nMIGRATION_DATABASE_URL="+localURL.String()+"\nAPP_ENV=development\n"), 0600); err != nil {
		return empty, err
	}
	raw, _ = json.MarshalIndent(restored, "", "  ")
	err = os.WriteFile(filepath.Join(destination, "verified-manifest.json"), raw, 0600)
	return restored, err
}

func BackupName() string { return "production-local-" + time.Now().UTC().Format("20060102T150405Z") }
