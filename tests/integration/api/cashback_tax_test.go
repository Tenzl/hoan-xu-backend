package api

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"hoanxu/internal/affiliate"
	"hoanxu/internal/cashback"
	"hoanxu/internal/imports"
	"hoanxu/internal/platform"
	"hoanxu/internal/tracking"
)

func TestTaxSnapshotUsesArchivedPolicyAndActualCommission(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	policies := &cashback.Service{Store: s}
	current, err := policies.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if current.Tax != 500 {
		t.Fatalf("default tax: %v", current.Tax)
	}
	tiers := []cashback.Tier{{Code: "bronze", MinOrders: 0, Min: 6500, Max: 7500}, {Code: "platinum", MinOrders: 30, Min: 7500, Max: 8500}, {Code: "diamond", MinOrders: 100, Min: 8500, Max: 9500}}
	if _, err = policies.Create(ctx, admin, "tax-policy-first", cashback.Input{CurrentVersionID: current.ID, Tax: 500, Tiers: tiers}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"fixture"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	fixture := &offerLinkFixture{}
	service := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, LinkGenerator: fixture, ProductLookup: verifiedLinkProduct}
	raw, err := service.CreateLink(ctx, customer, "https://shopee.vn/product/1/2")
	if err != nil {
		t.Fatal(err)
	}
	result := raw.(map[string]any)
	ids := fixture.ids
	claims, err := tracking.Verify(ids, "fixture", s.SignTracking)
	if err != nil {
		t.Fatal(err)
	}
	effective, err := cashback.EffectiveRate(claims.Bps, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !tracking.MatchesFactor(ids[3], (cashback.LinkRate{EffectiveBps: effective}).Factor()) || result["payoutFactor"] != (cashback.LinkRate{EffectiveBps: effective}).Factor() {
		t.Fatal(result, ids)
	}
	for _, field := range []string{"taxPercent", "taxBps", "fixedCashback", "promisedCashback"} {
		if _, ok := result[field]; ok {
			t.Fatalf("internal field leaked: %s", field)
		}
	}
	current, err = policies.Current(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = policies.Create(ctx, admin, "tax-policy-second", cashback.Input{CurrentVersionID: current.ID, Tax: 10000, Tiers: tiers}); err != nil {
		t.Fatal(err)
	}
	if _, err = service.CreateLink(ctx, customer, "https://shopee.vn/product/1/2"); err != nil {
		t.Fatal(err)
	}
	if fixture.ids[3] != "0p00" {
		t.Fatalf("new tax did not apply: %v", fixture.ids)
	}
	membership, err := cashback.MembershipFor(ctx, s.Queries, customer)
	if err != nil {
		t.Fatal(err)
	}
	public, err := json.Marshal(membership.Public())
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err = json.Unmarshal(public, &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["taxPercent"]; ok {
		t.Fatal("tax in customer membership")
	}
	// Two different orders use the same signed factor, even after tax changes.
	rows := []imports.Row{}
	for i, commission := range []int64{10001, 20001} {
		rows = append(rows, imports.Row{Channel: "shopee", Publisher: "fixture", OrderID: fmt.Sprintf("tax-order-%d", i), LineID: "line", Tracking: ids[2], NativeShopee: true, SubIDs: ids, ShopID: "1", ItemID: "2", Date: claims.CreatedAt.Add(time.Hour), Name: "Product", Value: 100000, Commission: commission, Status: "approved"})
	}
	importer := &imports.Service{Store: s}
	batch, err := importer.Preview(ctx, admin, "tax.csv", platform.Hash("tax-snapshot-actual"), nil, rows)
	if err != nil {
		t.Fatal(err)
	}
	id := batch.(map[string]any)["id"].(string)
	if _, err = importer.Commit(ctx, admin, "commit-tax-orders", id); err != nil {
		t.Fatal(err)
	}
	work, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); importer.Run(work) }()
	defer func() { cancel(); <-done }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var status string
		if err = s.Pool.QueryRow(ctx, `SELECT status FROM import_batches WHERE id=$1`, id).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "completed" {
			break
		}
		if status == "failed" || time.Now().After(deadline) {
			t.Fatalf("import %s", status)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for i, commission := range []int64{10001, 20001} {
		var bps int
		var cash int64
		var link *string
		if err = s.Pool.QueryRow(ctx, `SELECT share_bps,cashback,link_id::text FROM orders WHERE external_id=$1`, fmt.Sprintf("tax-order-%d", i)).Scan(&bps, &cash, &link); err != nil {
			t.Fatal(err)
		}
		expected := (commission*int64(effective) + 9999) / 10000
		if bps != effective || cash != expected || link != nil {
			t.Fatalf("order %d: rate %d cash %d want %d link %v", i, bps, cash, expected, link)
		}
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("saved links: %d %v", count, err)
	}
}
