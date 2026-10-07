package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/browser"
)

func TestShopeePublisherConfigurationRequiresSettingsCSRFAndReauthentication(t *testing.T) {
	store, customer, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	if _, err := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	session := func(id string) (string, *auth.User) {
		tx, err := store.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		token, err := a.NewSession(ctx, tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		u, err := a.Session(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		return token, u
	}
	at, au := session(admin)
	ct, cu := session(customer)
	aff := &affiliate.Service{Store: store, Browser: browser.NewManual("missing-chrome", t.TempDir())}
	srv := New(&Server{Store: store, Auth: a, Affiliate: aff, Origin: "http://localhost:3000"})
	request := func(method, path, token, csrf, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrf)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		token, csrf string
		code        int
	}{{"", "", 401}, {ct, cu.CSRF, 403}, {at, "", 403}, {at, au.CSRF, 403}} {
		w := request("PUT", "/admin/browser/publisher", tc.token, tc.csrf, `{"publisher":"123456789"}`)
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, err := store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, au.SessionID); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{`{"publisher":"abc"}`, `{"publisher":"123456789"}`, `{"publisher":""}`} {
		w := request("PUT", "/admin/browser/publisher", at, au.CSRF, body)
		if w.Code != 410 {
			t.Fatal("legacy publisher route accepted", w.Code, w.Body.String())
		}
	}
	var publisher string
	if err := store.Pool.QueryRow(ctx, `SELECT coalesce(settings->>'publisher','') FROM affiliate_channels WHERE id='shopee'`).Scan(&publisher); err != nil || publisher != "" {
		t.Fatal("retired endpoint mutated publisher", publisher, err)
	}

}

func TestAffiliateShortLinkAcceptsDatabasePublisherWithoutEnvironmentVariable(t *testing.T) {
	store, customer, admin := testStore(t)
	configureLinkPolicy(t, store)
	ctx := context.Background()
	aff := &affiliate.Service{Store: store, Enabled: true, TrackingVerified: true, LinkGenerator: &offerLinkFixture{}}
	if _, err := store.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"template":"https://s.shopee.vn/an_redir"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"123456789", "987654321"} {
		if err := aff.SavePublisher(ctx, admin, id); err != nil {
			t.Fatal(err)
		}
		result, err := aff.CreateLink(ctx, customer, "https://shopee.vn/product/1/2")
		if err != nil {
			t.Fatal(err)
		}
		if result.(map[string]any)["affiliateUrl"] != "https://s.shopee.vn/3B7ybQjO2E" {
			t.Fatal("Shopee short URL not returned")
		}
	}
}
