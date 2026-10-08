package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/imports"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func verifiedLinkProduct(ctx context.Context, raw string) (any, error) {
	shop, item, _, err := affiliate.Resolve(ctx, raw)
	return map[string]any{"schemaVerified": true, "shopId": shop, "itemId": item, "productName": "Shopee product"}, err
}

func TestPurchasesFeedOwnsFiltersAndPreservesDeletedAndLegacyLinks(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	active, _ := savedFixture(t, s, customer, now.Add(-time.Hour))
	expired, late := savedFixture(t, s, customer, now.Add(-7*24*time.Hour))
	progress, row := savedFixture(t, s, customer, now.Add(-2*time.Hour))
	row.OrderID = "PROGRESS"
	row.Name = "Tracked product"
	row.LineID, _ = imports.SourceLineID(row)
	importRows(t, s, admin, []imports.Row{row})
	deleted, orphan := savedFixture(t, s, customer, now.Add(-3*time.Hour))
	removeHistoricalLinkFixture(t, s, customer, deleted)
	orphan.OrderID = "DELETED"
	orphan.LineID, _ = imports.SourceLineID(orphan)
	importRows(t, s, admin, []imports.Row{orphan})
	var legacy, other string
	if err := s.Pool.QueryRow(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id,min_share_bps,max_share_bps) SELECT $1,'shopee','https://shopee.vn/product/1/2','https://s.shopee.vn/Legacy','legacy-feed',policy_id,6500,7500 FROM affiliate_links WHERE id=$2 RETURNING id::text`, customer, active).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `INSERT INTO users(name,role) VALUES('Other','customer') RETURNING id::text`).Scan(&other); err != nil {
		t.Fatal(err)
	}
	foreign, _ := savedFixture(t, s, other, now)
	a := &auth.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.NewSession(ctx, tx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	handler := New(&Server{Store: s, Auth: a, Affiliate: &affiliate.Service{Store: s}, Origin: "http://localhost:3000"})
	read := func(query string) []map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/me/purchases?"+query, nil)
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var result struct{ Data []map[string]any }
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.Data
	}
	selecting := read("status=selecting")
	if len(selecting) != 1 || selecting[0]["link"].(map[string]any)["id"] != active {
		t.Fatal(selecting)
	}
	rejected := read("status=rejected")
	if len(rejected) != 1 || rejected[0]["link"].(map[string]any)["id"] != expired {
		t.Fatal(rejected)
	}
	pending := read("status=progress")
	if len(pending) != 2 {
		t.Fatal(pending)
	}
	all := read("status=all&perPage=20")
	if len(all) != 5 {
		t.Fatal(all)
	}
	matched, orphanFound, legacyFound := 0, false, false
	for _, record := range all {
		if record["link"] == nil {
			if record["kind"] == "order" {
				orphanFound = true
			}
			continue
		}
		link := record["link"].(map[string]any)
		if link["id"] == foreign {
			t.Fatal("foreign link leaked")
		}
		if link["id"] == progress {
			matched++
			if link["canDelete"] != true || record["kind"] != "order" || link["productName"] != "Tracked product" {
				t.Fatal(record)
			}
		}
		if link["id"] == legacy {
			legacyFound = link["legacy"] == true
		}
	}
	if matched != 1 || !orphanFound || !legacyFound {
		t.Fatal("lost or duplicated records", all)
	}
	first := read("status=all&perPage=2&page=1")
	second := read("status=all&perPage=2&page=2")
	if len(first) != 2 || len(second) != 2 || first[0]["id"] == second[0]["id"] {
		t.Fatal("pagination", first, second)
	}
	seen := make(map[any]bool)
	for _, page := range [][]map[string]any{first, second, read("status=all&perPage=2&page=3")} {
		for _, record := range page {
			if seen[record["id"]] {
				t.Fatal("pagination repeated a record", record)
			}
			seen[record["id"]] = true
		}
	}
	if len(seen) != len(all) {
		t.Fatal("pagination lost history", len(seen), len(all))
	}
	boundary, _ := json.Marshal(map[string]any{"time": first[1]["sortAt"], "id": first[1]["id"]})
	next := read("status=all&perPage=2&cursor=" + base64.RawURLEncoding.EncodeToString(boundary))
	if len(next) != len(second) || next[0]["id"] != second[0]["id"] || next[1]["id"] != second[1]["id"] {
		t.Fatal("cursor and offset history differ", next, second)
	}
	late.OrderID = "LATE"
	late.LineID, _ = imports.SourceLineID(late)
	importRows(t, s, admin, []imports.Row{late})
	if records := read("status=rejected"); len(records) != 0 {
		t.Fatal("late valid order remained rejected", records)
	}
	if records := read("status=progress"); len(records) != 3 {
		t.Fatal(records)
	}
	if _, err := s.Pool.Exec(ctx, `INSERT INTO orders(user_id,link_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,share_bps,ordered_at) SELECT $1,$2,policy_id,'shopee','legacy','OLD-NAMED','OLD-NAMED','Legacy product name',100000,10000,6000,6000,now() FROM affiliate_links WHERE id=$2`, customer, legacy); err != nil {
		t.Fatal(err)
	}
	legacyMatches := 0
	for _, record := range read("status=all&perPage=20") {
		if link, ok := record["link"].(map[string]any); ok && link["id"] == legacy {
			legacyMatches++
			if record["kind"] != "order" || link["productName"] != "Legacy product name" || link["canDelete"] != false {
				t.Fatal("legacy order was not merged with its named read-only link", record)
			}
		}
	}
	if legacyMatches != 1 {
		t.Fatal("duplicate or missing legacy link", legacyMatches)
	}
}

func TestCreatedLinkIncludesProductName(t *testing.T) {
	s, user, _ := testStore(t)
	configureLinkPolicy(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"123456789"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	svc := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, ProductLookup: verifiedLinkProduct, LinkGenerator: &offerLinkFixture{}}
	value, err := svc.CreateLink(ctx, user, "https://shopee.vn/product/1/2")
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["productName"] != "Shopee product" {
		t.Fatal("missing server product name", value)
	}
	var stored string
	if err := s.Pool.QueryRow(ctx, `SELECT product_name FROM affiliate_links WHERE id=$1`, value.(map[string]any)["id"]).Scan(&stored); err != nil || stored != "Shopee product" {
		t.Fatal("product name was not persisted", stored, err)
	}
	if err := svc.DeleteLink(ctx, user, value.(map[string]any)["id"].(string)); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []map[string]any{
		{"schemaVerified": false, "shopId": "1", "itemId": "2", "productName": "Unverified"},
		{"schemaVerified": true, "shopId": "1", "itemId": "3", "productName": "Different product"},
		{"schemaVerified": true, "shopId": "1", "itemId": "2", "productName": "   "},
	} {
		generator := &offerLinkFixture{}
		svc.LinkGenerator = generator
		svc.ProductLookup = func(context.Context, string) (any, error) { return invalid, nil }
		if _, err := svc.CreateLink(ctx, user, "https://shopee.vn/product/1/2"); err == nil || generator.tracking != "" {
			t.Fatal("invalid verified name reached link generator", invalid, err)
		}
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links WHERE user_id=$1`, user).Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid product created a link", count, err)
	}
}
