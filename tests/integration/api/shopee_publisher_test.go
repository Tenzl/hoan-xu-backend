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
	for _, body := range []string{`{}`, `{"publisher":null}`, `{"publisher":"abc"}`, `{"publisher":"12.3"}`, `{"publisher":"` + strings.Repeat("1", 33) + `"}`} {
		w := request("PUT", "/admin/browser/publisher", at, au.CSRF, body)
		if w.Code != 422 {
			t.Fatal("invalid affiliate ID accepted", w.Code, w.Body.String())
		}
	}
	if _, err := store.Pool.Exec(ctx, `UPDATE affiliate_channels SET settings='{"template":"https://s.shopee.vn/an_redir"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	w := request("PUT", "/admin/browser/publisher", at, au.CSRF, `{"publisher":" 123456789 "}`)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var template, publisher, status string
	if err := store.Pool.QueryRow(ctx, `SELECT settings->>'template',settings->>'publisher',status FROM affiliate_channels WHERE id='shopee'`).Scan(&template, &publisher, &status); err != nil || template != "https://s.shopee.vn/an_redir" || publisher != "123456789" || status == "available" || aff.TrackingVerified {
		t.Fatal("saving publisher changed tracking/template", err)
	}
	w = request("GET", "/admin/browser", at, "", "")
	var response struct {
		Data struct {
			Publisher string `json:"publisher"`
		} `json:"data"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &response) != nil || response.Data.Publisher != publisher {
		t.Fatal("publisher not restored in admin", w.Code, w.Body.String())
	}
	w = request("PUT", "/admin/browser/publisher", at, au.CSRF, `{"publisher":""}`)
	if w.Code != 200 {
		t.Fatal("could not clear publisher", w.Code)
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
