package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"hoanxu/internal/affiliate"
	"hoanxu/internal/imports"
	"hoanxu/internal/platform"
	"hoanxu/internal/shopeeconfig"
	"hoanxu/internal/tracking"
)

func TestV4MissingKeyRollsBackPreviewAndKeepsWorkerRowsRetryable(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	_, row := savedFixture(t, s, user, time.Now().UTC().Add(-time.Hour).Truncate(time.Second))
	row.Status = "approved"
	missing := *s
	var err error
	missing.Tracking, err = tracking.NewCodec(`{"2":"`+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32)))+`"}`, "2")
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&imports.Service{Store: &missing}).Preview(ctx, admin, "missing.csv", platform.Hash("missing-key"), nil, []imports.Row{row})
	var config *platform.Error
	if !errors.As(err, &config) || config.Code != "TRACKING_KEY_UNAVAILABLE" || config.Status != 503 {
		t.Fatal("key error classified as attribution", err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM import_batches`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial preview committed", count, err)
	}
	preview, err := (&imports.Service{Store: s}).Preview(ctx, admin, "retry.csv", platform.Hash("retry-key"), nil, []imports.Row{row})
	if err != nil {
		t.Fatal(err)
	}
	id := preview.(map[string]any)["id"].(string)
	if _, err = (&imports.Service{Store: s}).Commit(ctx, admin, "missing-key-worker", id); err != nil {
		t.Fatal(err)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); (&imports.Service{Store: &missing}).Run(workerCtx) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		if err = s.Pool.QueryRow(ctx, `SELECT status FROM import_batches WHERE id=$1`, id).Scan(&status); err != nil {
			cancel()
			<-done
			t.Fatal(err)
		}
		if status == "failed" {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("worker did not preserve configuration failure")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
	var status string
	if err = s.Pool.QueryRow(ctx, `SELECT status FROM import_rows WHERE batch_id=$1`, id).Scan(&status); err != nil || status != "valid" {
		t.Fatal("retryable row was discarded", status, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	row.ReportMetadata.ShopeeOrderStatus = "Completed"
	row.ReportMetadata.AffiliateItemStatus = "Approved"
	importRows(t, s, admin, []imports.Row{row})
	importRows(t, s, admin, []imports.Row{row, row})
	var balance int64
	if err = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, user).Scan(&balance); err != nil || balance != 6301 {
		t.Fatal("restored key did not reconcile once", balance, err)
	}
}

func TestV4SnapshotDoesNotReadArchivedFinancialFields(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	link, row := savedFixture(t, s, user, time.Now().UTC().Add(-30*24*time.Hour).Truncate(time.Second))
	claims, err := s.Tracking.Decode(row.SubIDs)
	if err != nil {
		t.Fatal(err)
	}
	if err = (&affiliate.Service{Store: s}).DeleteLink(ctx, user, link); err != nil {
		t.Fatal(err)
	}
	// Deliberately alter the archived record in the isolated test schema. Only
	// its UUID/version may be consulted, never these financial fields.
	if _, err = s.Pool.Exec(ctx, `UPDATE cashback_policies SET tax_bps=10000 WHERE tracking_version=$1`, claims.Policy); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE cashback_tiers SET min_share_bps=0,max_share_bps=1000 WHERE policy_id=(SELECT id FROM cashback_policies WHERE tracking_version=$1)`, claims.Policy); err != nil {
		t.Fatal(err)
	}
	row.Status = "approved"
	row.Date = claims.CreatedAt.Add(10 * 24 * time.Hour)
	row.ReportMetadata.ShopeeOrderStatus = "Completed"
	row.ReportMetadata.AffiliateItemStatus = "Approved"
	importRows(t, s, admin, []imports.Row{row})
	importRows(t, s, admin, []imports.Row{row})
	var cash int64
	var bps int
	var code string
	if err = s.Pool.QueryRow(ctx, `SELECT cashback,share_bps,tracking_code FROM orders`).Scan(&cash, &bps, &code); err != nil || cash != 6301 || bps != 6300 || code != fixtureCode(row.SubIDs) {
		t.Fatal("snapshot was reinterpreted", cash, bps, err)
	}
	var balance int64
	if err = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, user).Scan(&balance); err != nil || balance != 6301 {
		t.Fatal(balance, err)
	}
}

func TestV4InvalidatesPreviousVerificationProof(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	origin := "http://localhost:3000"
	cfg := shopeeconfig.Default()
	cfg.Publisher = "123456789"
	cfg.Enabled = true
	now := time.Now().UTC()
	cfg.VerifiedAt = &now
	cfg.TrackingVerified = true
	cfg.SchemaVerified = true
	fields := cfg.Fields
	fields.Enabled = false
	fields.RemoteURL = ""
	raw, _ := json.Marshal(fields)
	cfg.VerifiedFingerprint = platform.Hash(string(raw))
	settings := map[string]any{"publisher": cfg.Publisher, "runtimeConfigs": map[string]any{shopeeconfig.Key(origin): cfg}}
	raw, _ = json.Marshal(settings)
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET settings=$1 WHERE id='shopee'`, raw); err != nil {
		t.Fatal(err)
	}
	loaded, err := shopeeconfig.Load(ctx, s, origin)
	if err != nil || loaded.Enabled || loaded.TrackingVerified || loaded.VerifiedAt != nil {
		t.Fatal("old proof enabled v4", loaded, err)
	}
}
