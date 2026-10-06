package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/platform"
)

type offerLinkFixture struct {
	tracking, shop, item string
	customerTracking     string
	err                  error
	ids                  [5]string
}

func (f *offerLinkFixture) CreateOfferLink(_ context.Context, shop, item string, ids [5]string) (string, error) {
	f.shop, f.item, f.tracking = shop, item, ids[2]
	f.ids = ids
	f.customerTracking = ids[0]
	if f.err != nil {
		return "", f.err
	}
	return "https://s.shopee.vn/3B7ybQjO2E", nil
}

func TestCustomerTrackingIsStableAcrossShopeeLinksAndIsolatedBetweenCustomers(t *testing.T) {
	s, customer, _ := testStore(t)
	ctx := context.Background()
	configureLinkPolicy(t, s)
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"123456789"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	var customerCode, other, otherCode string
	if err := s.Pool.QueryRow(ctx, `SELECT tracking_code FROM users WHERE id=$1`, customer).Scan(&customerCode); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool.QueryRow(ctx, `INSERT INTO users(name,role) VALUES('Other Customer','customer') RETURNING id::text,tracking_code`).Scan(&other, &otherCode); err != nil {
		t.Fatal(err)
	}
	f := &offerLinkFixture{}
	aff := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, LinkGenerator: f}
	seen := map[string]bool{}
	for _, tc := range []struct{ user, code string }{{customer, customerCode}, {customer, customerCode}, {other, otherCode}} {
		result, err := aff.CreateLink(ctx, tc.user, "https://shopee.vn/product/83496725/6939920023")
		if err != nil {
			t.Fatal(err)
		}
		l := result.(map[string]any)
		tracking := l["trackingCode"].(string)
		if f.customerTracking != tc.code {
			t.Fatal("subId1 did not use the customer's permanent code")
		}
		if tracking == tc.code || tracking != f.tracking || seen[tracking] {
			t.Fatal("per-link tracking must remain distinct and unique")
		}
		seen[tracking] = true
	}
	if customerCode == otherCode {
		t.Fatal("customers share a tracking code")
	}
}

func TestCreateLinkPersistsSignedSnapshotWithoutCreatingOrders(t *testing.T) {
	s, customer, _ := testStore(t)
	ctx := context.Background()
	configureLinkPolicy(t, s)
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"123456789"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	f := &offerLinkFixture{}
	aff := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, LinkGenerator: f}
	result, err := aff.CreateLink(ctx, customer, "https://shopee.vn/product/83496725/6939920023")
	if err != nil {
		t.Fatal(err)
	}
	l := result.(map[string]any)
	var count int
	if e := s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM affiliate_links)+(SELECT count(*) FROM orders)`).Scan(&count); e != nil || count != 1 {
		t.Fatal("generation must save one link only", count, e)
	}
	if l["id"] == nil || l["status"] != "active" || l["canDelete"] != true || l["affiliateUrl"] != "https://s.shopee.vn/3B7ybQjO2E" || l["payoutFactor"] == nil || l["expiresAt"] == nil || len(f.ids[2]) != 49 || f.ids[3] == "" || len(f.ids[4]) != 32 {
		t.Fatal(l, f.ids)
	}
	f.err = errors.New("SHOPEE_UPSTREAM_FAILED")
	if _, err = aff.CreateLink(ctx, customer, "https://shopee.vn/product/83496725/6939920023"); err == nil {
		t.Fatal("failed generation returned a long URL")
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links WHERE user_id=$1`, customer).Scan(&count); err != nil || count != 1 {
		t.Fatal("failed generation saved a link", count, err)
	}
}

func verifiedProductFixture(_ context.Context, _ string) (any, error) {
	return map[string]any{"schemaVerified": true, "commission": int64(10001), "shopId": "83496725", "itemId": "6939920023"}, nil
}

func configureLinkPolicy(t *testing.T, s *platform.Store) {
	t.Helper()
	if _, e := s.Pool.Exec(context.Background(), `UPDATE cashback_tiers SET min_share_bps=6500,max_share_bps=7500`); e != nil {
		t.Fatal(e)
	}
}

func TestSavedLinkHTTPReturns200AndDatabaseID(t *testing.T) {
	s, customer, _ := testStore(t)
	configureLinkPolicy(t, s)
	ctx := context.Background()
	if _, e := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"123456789"}' WHERE id='shopee'`); e != nil {
		t.Fatal(e)
	}
	a := &auth.Service{Store: s}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	token, e := a.NewSession(ctx, tx, customer)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	u, e := a.Session(ctx, token)
	if e != nil {
		t.Fatal(e)
	}
	handler := New(&Server{Store: s, Auth: a, Affiliate: &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, LinkGenerator: &offerLinkFixture{}}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("POST", "/api/v1/affiliate-links", strings.NewReader(`{"url":"https://shopee.vn/product/83496725/6939920023"}`))
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", u.CSRF)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var response struct{ Data map[string]any }
		if json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Data["id"] == nil || response.Data["expiresAt"] == nil || response.Data["payoutFactor"] == nil {
			t.Fatal(w.Body.String())
		}
	}
	var n int
	if e = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM affiliate_links)+(SELECT count(*) FROM orders)`).Scan(&n); e != nil || n != 5 {
		t.Fatal("expected five saved links and no orders", n, e)
	}
}
