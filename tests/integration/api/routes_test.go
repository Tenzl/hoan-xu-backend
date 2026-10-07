package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/settings"
	"hoanxu/internal/tracking"
	"time"
)

func TestFAQIsValidatedAndPreservedAcrossPolicyUpdates(t *testing.T) {
	s, _, admin := testStore(t)
	ctx := context.Background()
	svc := &settings.Service{Store: s}
	input := settings.Input{Brand: "Hoàn Xu"}
	if e := svc.Update(ctx, admin, input); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.Pool.QueryRow(ctx, `SELECT jsonb_array_length(settings->'faq') FROM app_settings`).Scan(&count); e != nil || count != 4 {
		t.Fatal(count, e)
	}
	input.FAQ = []settings.FAQ{{Question: "", Answer: "Answer"}}
	if e := svc.Update(ctx, admin, input); e == nil {
		t.Fatal("empty question accepted")
	}
	input.FAQ = []settings.FAQ{{Question: "Làm sao đối soát?", Answer: "Dùng báo cáo thực nhận."}}
	if e := svc.Update(ctx, admin, input); e != nil {
		t.Fatal(e)
	}
	if e := s.Pool.QueryRow(ctx, `SELECT jsonb_array_length(settings->'faq') FROM app_settings`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
}

func TestAllReadRoutesAndManualOrderAgainstPostgreSQL(t *testing.T) {
	store, customer, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	srv := New(&Server{Store: store, Auth: a, Affiliate: &affiliate.Service{Store: store}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	session := func(id string) (string, *auth.User) {
		tx, e := store.Pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		token, e := a.NewSession(ctx, tx, id)
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
		return token, u
	}
	customerToken, _ := session(customer)
	adminToken, adminUser := session(admin)
	request := func(method, path, body, token, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", "manual-order-key-1")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	// A newly provisioned account may only change its temporary password or inspect its session.
	if w := request("GET", "/admin/users", "", adminToken, ""); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, e := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1;`, admin); e != nil {
		t.Fatal(e)
	}
	if _, e := store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, adminUser.SessionID); e != nil {
		t.Fatal(e)
	}
	for _, path := range []string{"/me", "/me/sessions", "/me/dashboard", "/orders", "/wallet", "/wallet/transactions", "/coins/transactions", "/checkins", "/withdrawals", "/affiliate-links", "/notifications", "/gift-redemptions"} {
		if w := request("GET", path, "", customerToken, ""); w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	for _, path := range []string{"/admin/dashboard", "/admin/orders", "/admin/users", "/admin/internal-accounts", "/admin/withdrawals", "/admin/gifts", "/admin/gift-redemptions", "/admin/deals", "/admin/notifications", "/admin/settings", "/admin/affiliate-channels", "/admin/browser", "/admin/audit-logs", "/admin/ledger-check", "/admin/order-imports"} {
		if w := request("GET", path, "", adminToken, ""); w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
	}
	if _, e := store.Pool.Exec(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id) SELECT $1,'shopee','https://shopee.vn/product/1/2','https://s.shopee.vn/test','manualtracking',id FROM cashback_policies WHERE mode='fixed'`, customer); e != nil {
		t.Fatal(e)
	}
	configureLinkPolicy(t, store)
	if _, e := store.Pool.Exec(ctx, `UPDATE affiliate_channels SET settings='{"publisher":"123456789"}' WHERE id='shopee'`); e != nil {
		t.Fatal(e)
	}
	var code string
	var version uint32
	if e := store.Pool.QueryRow(ctx, `SELECT tracking_code FROM users WHERE id=$1`, customer).Scan(&code); e != nil {
		t.Fatal(e)
	}
	if e := store.Pool.QueryRow(ctx, `SELECT tracking_version FROM cashback_policies WHERE mode='tiered' AND EXISTS(SELECT 1 FROM cashback_tiers t WHERE t.policy_id=cashback_policies.id AND t.tier_code='bronze') ORDER BY created_at DESC LIMIT 1`).Scan(&version); e != nil {
		t.Fatal(e)
	}
	at := time.Now().UTC().Truncate(time.Second)
	ids, e := tracking.Issue(tracking.Claims{CreatedAt: at, Shop: 1, Item: 2, Policy: version, Tier: "bronze", Bps: 6600}, code, "123456789", "0.63", store.SignTracking)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(map[string]any{"trackingCode": ids[2], "channel": "shopee", "publisher": "123456789", "externalId": "MANUAL1", "lineId": "ignored", "productName": "Manual order", "value": 100000, "commission": 5000, "evidence": "Approved platform report", "subIds": ids, "shopId": "1", "itemId": "2", "conversionId": "12345", "modelId": "0", "promotionId": "0", "orderedAt": at.Add(time.Second)})
	body := string(raw)
	w := request("POST", "/admin/orders", body, adminToken, adminUser.CSRF)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result struct {
		Data struct {
			ID string `json:"id"`
		}
	}
	if e := json.Unmarshal(w.Body.Bytes(), &result); e != nil {
		t.Fatal(e)
	}
	if w = request("GET", "/orders/"+result.Data.ID, "", customerToken, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	// The customer sees the pending order but is never granted administrative approval rights.
	if w = request("POST", "/admin/orders/"+result.Data.ID+"/events", `{"action":"approved"}`, customerToken, adminUser.CSRF); w.Code != 403 {
		t.Fatal(w.Code, w.Body.String())
	}
	staff, e := auth.CreateInternal(ctx, store, admin, "staff", "Staff", "staff-test-password", "staff", []string{"orders"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, staff); e != nil {
		t.Fatal(e)
	}
	staffToken, _ := session(staff)
	if w = request("GET", "/admin/orders", "", staffToken, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	for _, path := range []string{"/admin/internal-accounts", "/admin/withdrawals", "/admin/settings", "/admin/users"} {
		if w = request("GET", path, "", staffToken, ""); w.Code != 403 {
			t.Fatal(path, w.Code, w.Body.String())
		}
	}
}
