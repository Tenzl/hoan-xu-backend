package api

import (
	"context"
	"encoding/json"
	"fmt"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/cashback"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"hoanxu/internal/tracking"
	"testing"
	"time"
)

func importRows(t *testing.T, s *platform.Store, admin string, rows []imports.Row) string {
	t.Helper()
	ctx := context.Background()
	svc := &imports.Service{Store: s}
	key := platform.Token()
	preview, e := svc.Preview(ctx, admin, "native.csv", platform.Hash(key), nil, rows)
	if e != nil {
		t.Fatal(e)
	}
	id := preview.(map[string]any)["id"].(string)
	if _, e = svc.Commit(ctx, admin, key, id); e != nil {
		t.Fatal(e)
	}
	work, cancel := context.WithCancel(ctx)
	defer cancel()
	go svc.Run(work)
	deadline := time.Now().Add(6 * time.Second)
	for {
		var status string
		if e = s.Pool.QueryRow(ctx, `SELECT status FROM import_batches WHERE id=$1`, id).Scan(&status); e != nil {
			t.Fatal(e)
		}
		if status == "completed" {
			return id
		}
		if status == "failed" || time.Now().After(deadline) {
			t.Fatal("batch", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
func TestSignedCSVExpiryLateImportSnapshotDuplicateAndCancellation(t *testing.T) {
	s, customer, admin := testStore(t)
	configureLinkPolicy(t, s)
	ctx := context.Background()
	if _, e := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET settings='{"publisher":"123456789"}' WHERE id='shopee'`); e != nil {
		t.Fatal(e)
	}
	var code string
	var version uint32
	if e := s.Pool.QueryRow(ctx, `SELECT tracking_code FROM users WHERE id=$1`, customer).Scan(&code); e != nil {
		t.Fatal(e)
	}
	if e := s.Pool.QueryRow(ctx, `SELECT tracking_version FROM cashback_policies WHERE mode='tiered' AND EXISTS(SELECT 1 FROM cashback_tiers t WHERE t.policy_id=cashback_policies.id AND t.tier_code='bronze') ORDER BY created_at DESC LIMIT 1`).Scan(&version); e != nil {
		t.Fatal(e)
	}
	created := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	claims := tracking.Claims{CreatedAt: created, Shop: 83496725, Item: 6939920023, Policy: version, Tier: "bronze", Bps: 6600}
	ids, e := tracking.Issue(claims, code, "123456789", "0.63", s.SignTracking)
	if e != nil {
		t.Fatal(e)
	}
	// A later publisher change must not invalidate the historical signed promise.
	if e = (&affiliate.Service{Store: s}).SavePublisher(ctx, admin, "987654321"); e != nil {
		t.Fatal(e)
	}
	base := imports.Row{NativeShopee: true, Channel: "shopee", OrderID: "ORDER1", LineID: "line-one", Tracking: ids[2], SubIDs: ids, ShopID: "83496725", ItemID: "6939920023", Name: "Product", Value: 100000, Commission: 5001, Status: "pending", Date: created.Add(time.Hour)}
	rows := []imports.Row{base, base}
	rejected := base
	rejected.LineID = "cancelled"
	rejected.Status = "rejected"
	rows = append(rows, rejected)
	last := base
	last.LineID = "last-second"
	last.Date = claims.ExpiresAt().Add(-time.Second)
	last.Status = "approved"
	rows = append(rows, last)
	for i := 0; i < 8; i++ {
		bad := base
		bad.LineID = fmt.Sprintf("invalid-%d", i)
		switch i {
		case 0:
			bad.Date = created.Add(-time.Second)
		case 1:
			bad.Date = claims.ExpiresAt()
		case 2:
			bad.Date = claims.ExpiresAt().Add(time.Second)
		case 3:
			bad.SubIDs[0] = "other"
		case 4:
			bad.ItemID = "9"
		case 5:
			bad.SubIDs[3] = "0p99"
		case 6:
			bad.SubIDs[2] = bad.SubIDs[2][:48] + "z"
		case 7:
			bad.SubIDs[1] = "other-campaign"
		}
		rows = append(rows, bad)
	}
	batch := importRows(t, s, admin, rows)
	var n, ignored int
	var id, status, source string
	var cash int64
	var bps int
	var link *string
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n); e != nil || n != 5 {
		t.Fatal(n, e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM import_rows WHERE batch_id=$1 AND status='ignored' AND error IS NOT NULL`, batch).Scan(&ignored); e != nil || ignored != 6 {
		t.Fatal(ignored, e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT id::text,status,source_status,cashback,share_bps,link_id::text FROM orders WHERE line_id='line-one'`).Scan(&id, &status, &source, &cash, &bps, &link); e != nil || status != "pending" || source != "pending" || cash != 3151 || bps != 6300 || link != nil {
		t.Fatal(id, status, source, cash, bps, link, e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT status,cashback FROM orders WHERE line_id='cancelled'`).Scan(&status, &cash); e != nil || status != "rejected" || cash != 0 {
		t.Fatal(status, cash, e)
	}
	events := &orders.Service{Store: s}
	if _, e = events.Event(ctx, admin, id, "pending-approval", orders.Event{Action: "approved"}); e == nil {
		t.Fatal("pending source credited")
	}
	// Changing the active policy cannot reinterpret the archived link.
	policy, e := (&cashback.Service{Store: s}).Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	input := periodFixtureInput(policy.ID, []cashback.Tier{{Code: "bronze", Min: 8000, Max: 9000}, {Code: "platinum", MinGold: 30, Min: 8000, Max: 9000}, {Code: "diamond", MinGold: 100, Min: 8000, Max: 9000}}, 9000)
	if _, e = (&cashback.Service{Store: s}).Create(ctx, admin, "changed-link-policy", input); e != nil {
		t.Fatal(e)
	}
	base.Commission = 10001
	base.Status = "approved"
	importRows(t, s, admin, []imports.Row{base})
	if e = s.Pool.QueryRow(ctx, `SELECT cashback,share_bps FROM orders WHERE id=$1`, id).Scan(&cash, &bps); e != nil || cash != 6301 || bps != 6300 {
		t.Fatal(cash, bps, e)
	}
	// Approval uses order time, even though this report arrives weeks after expiry.
	for i := 0; i < 2; i++ {
		if _, e = events.Event(ctx, admin, id, "once-approval", orders.Event{Action: "approved"}); e != nil {
			t.Fatal(e)
		}
	}
	importRows(t, s, admin, []imports.Row{base, base})
	var balance int64
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, customer).Scan(&balance); e != nil || balance != 6301 {
		t.Fatal(balance, e)
	}
	// An approved signed order keeps its snapshot and rejects subsequent adjustments.
	if _, e = events.Event(ctx, admin, id, "ceil-adjustment", orders.Event{Action: "adjustment", Reason: "Updated actual commission"}); e == nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, customer).Scan(&balance); e != nil || balance != 6301 {
		t.Fatal(balance, e)
	}

	base.Status = "rejected"
	base.Commission = 0
	importRows(t, s, admin, []imports.Row{base})
	if _, e = events.Event(ctx, admin, id, "cancel-adjustment", orders.Event{Action: "adjustment", Reason: "Shopee cancelled order"}); e == nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, customer).Scan(&balance); e != nil || balance != 6301 {
		t.Fatal(balance, e)
	}
	// Retention expiry does not block approval; a date before issuance still does.
	if _, e = s.Pool.Exec(ctx, `UPDATE orders SET ordered_at=link_created_at-interval '1 second' WHERE line_id='last-second'`); e != nil {
		t.Fatal(e)
	}
	var lastID string
	if e = s.Pool.QueryRow(ctx, `SELECT id::text FROM orders WHERE line_id='last-second'`).Scan(&lastID); e != nil {
		t.Fatal(e)
	}
	if _, e = events.Event(ctx, admin, lastID, "invalid-time-approval", orders.Event{Action: "approved"}); e == nil {
		t.Fatal("out-of-window stored order credited")
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links`).Scan(&n); e != nil || n != 0 {
		t.Fatal("created affiliate link rows", n, e)
	}
	var raw []byte
	if e = s.Pool.QueryRow(ctx, `SELECT tracking_sub_ids FROM orders WHERE id=$1`, id).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var stored [5]string
	if json.Unmarshal(raw, &stored) != nil || stored != ids {
		t.Fatal("tracking not preserved")
	}
}
