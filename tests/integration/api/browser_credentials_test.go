package api

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hoanxu/internal/browser"
	"hoanxu/internal/platform"
)

func TestDatabaseCookiesPersistEncryptedWithoutProfile(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	cookies, err := browser.NewDatabaseCookieStore(ctx, store.Pool, store, filepath.Join(t.TempDir(), "missing.enc"))
	if err != nil || cookies.Configured() {
		t.Fatal("empty store should be usable without a profile", err)
	}
	if err = cookies.SaveContext(ctx, "SPC_EC=fixture-database-first"); err != nil {
		t.Fatal(err)
	}
	var cipher string
	if err = store.Pool.QueryRow(ctx, `SELECT cookie_cipher FROM browser_credentials WHERE provider='shopee'`).Scan(&cipher); err != nil || strings.Contains(cipher, "fixture-database-first") || strings.Contains(cipher, "SPC_EC") {
		t.Fatal("cookie was not encrypted", err)
	}
	plain, err := store.Decrypt(cipher)
	if err != nil || plain != "SPC_EC=fixture-database-first" {
		t.Fatal("database encryption did not round trip", err)
	}
	// Simulate another backend process with a fresh, empty Chrome directory.
	restarted, err := browser.NewDatabaseCookieStore(ctx, store.Pool, store, filepath.Join(t.TempDir(), "missing.enc"))
	if err != nil || !restarted.Configured() {
		t.Fatal("saved cookies were not discovered after restart", err)
	}
	loaded, err := restarted.LoadContext(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].Value != "fixture-database-first" {
		t.Fatal("cookies not restored from database", err)
	}
	if err = restarted.SaveContext(ctx, "SPC_EC=fixture-database-updated"); err != nil {
		t.Fatal(err)
	}
	loaded, err = cookies.LoadContext(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].Value != "fixture-database-updated" {
		t.Fatal("running store reloaded stale cookies", err)
	}
	var count int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM browser_credentials`).Scan(&count); err != nil || count != 1 {
		t.Fatal("expected one replaceable Shopee credential", err)
	}
	var rls bool
	if err = store.Pool.QueryRow(ctx, `SELECT relrowsecurity FROM pg_class WHERE oid='browser_credentials'::regclass`).Scan(&rls); err != nil || !rls {
		t.Fatal("credential table must have RLS", err)
	}
	var publicAccess bool
	if err = store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_class c, LATERAL aclexplode(coalesce(c.relacl,acldefault('r',c.relowner))) a WHERE c.oid='browser_credentials'::regclass AND a.grantee=0)`).Scan(&publicAccess); err != nil || publicAccess {
		t.Fatal("credential table granted PUBLIC access", err)
	}
}

func TestDatabaseCookieLegacyImportNeverOverwritesDatabase(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shopee-cookies.enc")
	legacy := &browser.CookieStore{Path: path, Codec: store}
	if err := legacy.Save("SPC_EC=fixture-legacy"); err != nil {
		t.Fatal(err)
	}
	cookies, err := browser.NewDatabaseCookieStore(ctx, store.Pool, store, path)
	if err != nil || !cookies.Configured() {
		t.Fatal("legacy encrypted cookies were not imported", err)
	}
	loaded, err := cookies.LoadContext(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].Value != "fixture-legacy" {
		t.Fatal("legacy import changed cookie", err)
	}
	if err = cookies.SaveContext(ctx, "SPC_EC=fixture-current-database"); err != nil {
		t.Fatal(err)
	}
	// A stale or corrupt local file must never replace the database row.
	if err = os.WriteFile(path, []byte("corrupt-local-file"), 0600); err != nil {
		t.Fatal(err)
	}
	restarted, err := browser.NewDatabaseCookieStore(ctx, store.Pool, store, path)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err = restarted.LoadContext(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].Value != "fixture-current-database" {
		t.Fatal("database did not take precedence", err)
	}
}

func TestDatabaseCookieValidationAndFailuresPreserveLastSave(t *testing.T) {
	store, _, _ := testStore(t)
	ctx := context.Background()
	cookies, err := browser.NewDatabaseCookieStore(ctx, store.Pool, store, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = cookies.SaveContext(ctx, "SPC_EC=fixture-keep"); err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"invalid", strings.Repeat("x", browser.MaxCookieBytes+1), `[{"name":"session","value":"fixture","domain":"evil.example"}]`} {
		if err = cookies.SaveContext(ctx, raw); !errors.Is(err, browser.ErrInvalidCookies) {
			t.Fatal("invalid cookies accepted or echoed", err)
		}
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err = cookies.SaveContext(cancelled, "SPC_EC=fixture-cancelled"); !errors.Is(err, browser.ErrCookieStorage) {
		t.Fatal("cancelled database write reported success", err)
	}
	loaded, err := cookies.LoadContext(ctx)
	if err != nil || len(loaded) != 1 || loaded[0].Value != "fixture-keep" {
		t.Fatal("failed save replaced valid credentials", err)
	}
	wrongKey := make([]byte, 32)
	wrongKey[0] = 1
	wrongCodec, err := platform.New(nil, base64.StdEncoding.EncodeToString(wrongKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = browser.NewDatabaseCookieStore(ctx, store.Pool, wrongCodec, ""); !errors.Is(err, browser.ErrCookieStorage) {
		t.Fatal("wrong encryption key was silently accepted", err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE browser_credentials SET cookie_cipher='corrupted-database-value'`); err != nil {
		t.Fatal(err)
	}
	if _, err = cookies.LoadContext(ctx); !errors.Is(err, browser.ErrCookieStorage) || strings.Contains(err.Error(), "corrupted-database-value") {
		t.Fatal("corrupt credentials accepted or echoed", err)
	}
}
