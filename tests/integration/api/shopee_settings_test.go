package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/browser"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestShopeeSettingsAreAdminOnlyPersistedAndExplicitlyDisableManualBrowser(t *testing.T) {
	store, customer, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	_, err := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin)
	if err != nil {
		t.Fatal(err)
	}
	session := func(id string) (string, *auth.User) {
		tx, e := store.Pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
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
	at, au := session(admin)
	ct, cu := session(customer)
	aff := &affiliate.Service{Store: store, Browser: browser.NewManual("missing-chrome", t.TempDir())}
	server := &Server{Store: store, Auth: a, Affiliate: aff, Origin: "http://localhost:3000", LocalBrowser: true}
	handler := New(server)
	request := func(method, token, csrf, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1/admin/browser/settings", strings.NewReader(body))
		r.Header.Set("Origin", server.Origin)
		r.Header.Set("X-CSRF-Token", csrf)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	body := `{"enabled":false,"publisher":"123456789","version":"0:0","priceScale":100000,"mode":"local","executablePath":"","profilePath":"private-data/chrome-profile","headless":false,"remoteUrl":"http://127.0.0.1:9222"}`
	for _, tc := range []struct {
		token, csrf string
		code        int
	}{{"", "", 401}, {ct, cu.CSRF, 403}, {at, "", 403}, {at, au.CSRF, 403}} {
		w := request("PUT", tc.token, tc.csrf, body)
		if w.Code != tc.code {
			t.Fatalf("settings auth: %d %s", w.Code, w.Body.String())
		}
	}
	_, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, au.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if w := request("PUT", at, au.CSRF, body); w.Code != 200 {
		t.Fatalf("save: %d %s", w.Code, w.Body.String())
	}
	if aff.CheckEnabled() {
		t.Fatal("manual Chrome bypassed disabled setting")
	}
	w := request("GET", at, "", "")
	var result struct {
		Data struct {
			Enabled    bool
			PriceScale int64
			Mode       string
		}
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.Data.Enabled || result.Data.PriceScale != 100000 || result.Data.Mode != "local" {
		t.Fatal(w.Code, w.Body.String())
	}
	if w:=request("PUT",at,au.CSRF,body);w.Code!=409 {t.Fatal("stale edit accepted",w.Code)}
	for _, invalid := range []string{strings.Replace(body, `"mode":"local"`, `"mode":"other"`, 1), strings.Replace(body, `100000`, `0`, 1), strings.Replace(body, `http://127.0.0.1:9222`, `http://169.254.169.254`, 1)} {
		if w := request("PUT", at, au.CSRF, invalid); w.Code != 422 {
			t.Fatal("invalid configuration accepted", w.Code, w.Body.String())
		}
	}
	var n int
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_channels WHERE id='shopee' AND settings ? 'runtimeConfigs' AND settings->>'publisher'='123456789'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("not persisted", n, err)
	}
	if err = store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='shopee_settings_updated'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("audit", n, err)
	}
}
