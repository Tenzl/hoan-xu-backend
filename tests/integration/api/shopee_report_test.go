package api

import (
	"context"
	"encoding/json"
	"errors"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"hoanxu/internal/tracking"
	"testing"
	"time"
)

func signedReportFixture(t *testing.T, s *platform.Store, customer string) imports.Row {
	t.Helper()
	configureLinkPolicy(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET settings='{"publisher":"123456789"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	var code string
	var version uint32
	if err := s.Pool.QueryRow(ctx, `SELECT tracking_code FROM users WHERE id=$1`, customer).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT tracking_version FROM cashback_policies WHERE mode='tiered' AND EXISTS(SELECT 1 FROM cashback_tiers t WHERE t.policy_id=cashback_policies.id AND t.tier_code='bronze') ORDER BY created_at DESC LIMIT 1`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	claims := tracking.Claims{CreatedAt: created, Shop: 83496725, Item: 6939920023, Policy: version, Tier: "bronze", Bps: 6600}
	ids, err := tracking.Issue(claims, code, "123456789", "0.63", s.SignTracking)
	if err != nil {
		t.Fatal(err)
	}
	return imports.Row{NativeShopee: true, Channel: "shopee", OrderID: "REPORT1", LineID: "report-line", Tracking: ids[2], SubIDs: ids, ShopID: "83496725", ItemID: "6939920023", Name: "Report product", Value: 463832, Commission: 20872, Status: "pending", Date: created.Add(time.Minute), ReportMetadata: imports.ReportMetadata{ReportChannel: "Zalo", ShopeeOrderStatus: "Pending", AffiliateItemStatus: "Pending", ReportedValue: "463832", ReportedCommission: "20872.44"}}
}

func TestShopeeReportApprovalCompetesWithManualReview(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	row := signedReportFixture(t, s, customer)
	batch := importRows(t, s, admin, []imports.Row{row})
	var id string
	// This is the source-approved, still-pending state inside the import transaction.
	if err := s.Pool.QueryRow(ctx, `UPDATE orders SET source_status='approved' WHERE line_id=$1 RETURNING id::text`, row.LineID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	service := &orders.Service{Store: s}
	start := make(chan struct{})
	results := make(chan error, 2)
	go func() {
		<-start
		_, err := service.Event(ctx, admin, id, "concurrent-manual-review", orders.Event{Action: "approved"})
		results <- err
	}()
	go func() {
		<-start
		tx, err := s.Pool.Begin(ctx)
		if err == nil {
			_, err = service.EventTx(ctx, tx, admin, id, orders.Event{Action: "approved"}, map[string]any{"origin": "shopee_report", "batchId": batch, "rowNumber": 1})
			if err == nil {
				err = tx.Commit(ctx)
			} else {
				_ = tx.Rollback(ctx)
			}
		}
		results <- err
	}()
	close(start)
	winners := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			winners++
			continue
		}
		var failure *platform.Error
		if !errors.As(err, &failure) || failure.Code != "INVALID_TRANSITION" {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatal("expected one approval", winners)
	}
	var balance int64
	var credits, events int
	if err := s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT count(*) FROM wallet_transactions WHERE reference=$2),(SELECT count(*) FROM order_events WHERE order_id=$3 AND action='approved')`, customer, "order_credit:"+id, id).Scan(&balance, &credits, &events); err != nil || balance != 13150 || credits != 1 || events != 1 {
		t.Fatal(balance, credits, events, err)
	}
}

func TestShopeeReportAutoApprovalOnceAndPreservesSource(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	row := signedReportFixture(t, s, customer)
	svc := &imports.Service{Store: s}
	preview, err := svc.Preview(ctx, admin, "report.csv", "preview-source", nil, []imports.Row{row})
	if err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&count); err != nil || count != 0 {
		t.Fatal("preview created orders", count, err)
	}
	if preview.(map[string]any)["counts"].(map[string]int)["valid"] != 1 {
		t.Fatal(preview)
	}
	importRows(t, s, admin, []imports.Row{row})
	var id, status string
	var balance int64
	if err := s.Pool.QueryRow(ctx, `SELECT id::text,status FROM orders WHERE line_id=$1`, row.LineID).Scan(&id, &status); err != nil || status != "pending" {
		t.Fatal(status, err)
	}
	row.ShopeeOrderStatus = "Completed"
	row.ReportChannel = ""
	importRows(t, s, admin, []imports.Row{row})
	var clearedChannel string
	if err := s.Pool.QueryRow(ctx, `SELECT coalesce(source_report->>'reportChannel','') FROM orders WHERE id=$1`, id).Scan(&clearedChannel); err != nil || clearedChannel != "" {
		t.Fatal("stale source channel", clearedChannel, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, customer).Scan(&balance); err != nil || balance != 0 {
		t.Fatal("affiliate pending credited", balance, err)
	}
	row.AffiliateItemStatus = "Completed"
	row.ReportChannel = "Zalo"
	row.Status = "approved"
	batch := importRows(t, s, admin, []imports.Row{row, row})
	importRows(t, s, admin, []imports.Row{row})
	var raw []byte
	var approved bool
	if err := s.Pool.QueryRow(ctx, `SELECT status,source_report,approved_at IS NOT NULL FROM orders WHERE id=$1`, id).Scan(&status, &raw, &approved); err != nil || status != "approved" || !approved {
		t.Fatal("completed report was not approved", status, approved, err)
	}
	var metadata imports.ReportMetadata
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata.ReportChannel != "Zalo" || metadata.ReportedCommission != "20872.44" {
		t.Fatal(metadata, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, customer).Scan(&balance); err != nil || balance != 13150 {
		t.Fatal(balance, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM wallet_transactions WHERE reference=$1`, "order_credit:"+id).Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate credit", count, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE order_id=$1 AND actor_id=$2 AND payload->>'batchId'=$3 AND payload->>'origin'='shopee_report'`, id, admin, batch).Scan(&count); err != nil || count != 1 {
		t.Fatal("missing source audit", count, err)
	}
	row.Status = "rejected"
	row.ShopeeOrderStatus = "Cancelled"
	row.AffiliateItemStatus = "Cancelled"
	row.Commission = 0
	importRows(t, s, admin, []imports.Row{row})
	if err := s.Pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, id).Scan(&status); err != nil || status != "approved" {
		t.Fatal("approved order changed", status, err)
	}
}

func TestShopeeCompletedReportKeepsInternalRejection(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	row := signedReportFixture(t, s, customer)
	importRows(t, s, admin, []imports.Row{row})
	var id, status string
	if err := s.Pool.QueryRow(ctx, `SELECT id::text FROM orders WHERE line_id=$1`, row.LineID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := (&orders.Service{Store: s}).Event(ctx, admin, id, "reject-report", orders.Event{Action: "rejected", Reason: "Review rejected this order"}); err != nil {
		t.Fatal(err)
	}
	row.Status = "approved"
	row.ShopeeOrderStatus = "Completed"
	row.AffiliateItemStatus = "Approved"
	importRows(t, s, admin, []imports.Row{row})
	if err := s.Pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, id).Scan(&status); err != nil || status != "rejected" {
		t.Fatal(status, err)
	}
	var balance int64
	if err := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, customer).Scan(&balance); err != nil || balance != 0 {
		t.Fatal(balance, err)
	}
}

func waitReportBatch(t *testing.T, svc *imports.Service, batch, target string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan struct{})
	go func() { defer close(finished); svc.Run(ctx) }()
	defer func() { cancel(); <-finished }()
	deadline := time.Now().Add(6 * time.Second)
	for {
		var status string
		if err := svc.Store.Pool.QueryRow(ctx, `SELECT status FROM import_batches WHERE id=$1`, batch).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == target {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("batch state %s, expected %s", status, target)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestShopeeReportRollbackRetryAndCommitActor(t *testing.T) {
	s, customer, uploader := testStore(t)
	ctx := context.Background()
	row := signedReportFixture(t, s, customer)
	row.Status = "approved"
	row.ShopeeOrderStatus = "Completed"
	row.AffiliateItemStatus = "Approved"
	var actor string
	if err := s.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Report confirmer','confirmer@example.com','admin') RETURNING id::text`).Scan(&actor); err != nil {
		t.Fatal(err)
	}
	svc := &imports.Service{Store: s}
	result, err := svc.Preview(ctx, uploader, "report.csv", "rollback-source", nil, []imports.Row{row})
	if err != nil {
		t.Fatal(err)
	}
	batch := result.(map[string]any)["id"].(string)
	if _, err := svc.Commit(ctx, actor, "commit-rollback", batch); err != nil {
		t.Fatal(err)
	}
	// Fail after the credit attempt. The order, ledger and applied marker must all roll back.
	if _, err := s.Pool.Exec(ctx, `CREATE FUNCTION reject_report_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture event failure'; END $$; CREATE TRIGGER reject_report_event BEFORE INSERT ON order_events FOR EACH ROW EXECUTE FUNCTION reject_report_event()`); err != nil {
		t.Fatal(err)
	}
	waitReportBatch(t, svc, batch, "failed")
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM orders)+(SELECT count(*) FROM wallet_transactions)+(SELECT count(*) FROM order_events)`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial financial commit", count, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM import_rows WHERE batch_id=$1 AND status='valid'`, batch).Scan(&count); err != nil || count != 1 {
		t.Fatal("lost retry row", count, err)
	}
	if _, err := s.Pool.Exec(ctx, `DROP TRIGGER reject_report_event ON order_events; DROP FUNCTION reject_report_event()`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `UPDATE import_batches SET status='queued',error=NULL WHERE id=$1`, batch); err != nil {
		t.Fatal(err)
	}
	waitReportBatch(t, svc, batch, "completed")
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM order_events WHERE actor_id=$1 AND payload->>'batchId'=$2`, actor, batch).Scan(&count); err != nil || count != 1 {
		t.Fatal("wrong committing actor", count, err)
	}
	var balance int64
	if err := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, customer).Scan(&balance); err != nil || balance != 13150 {
		t.Fatal(balance, err)
	}
}
