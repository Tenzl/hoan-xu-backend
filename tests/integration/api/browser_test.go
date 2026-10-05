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

func TestShopeeCookieImportIsRetiredAndDoesNotSaveOrStartChrome(t *testing.T) {
	store, customer, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	if _, e := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin); e != nil {
		t.Fatal(e)
	}
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
	customerToken, cu := session(customer)
	adminToken, au := session(admin)
	manager := browser.NewManual("missing-test-chromium-executable", t.TempDir())
	srv := New(&Server{Store: store, Auth: a, Affiliate: &affiliate.Service{Store: store, Browser: manager}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	request := func(method, path, body, token, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	valid := `{"cookie":"SPC_EC=fixture-secret-never-echo"}`
	for _, tc := range []struct {
		token, csrf, body string
		status            int
	}{
		{"", "", valid, 401}, {customerToken, cu.CSRF, valid, 403}, {adminToken, "", valid, 403},
		{adminToken, au.CSRF, `{"cookie":"invalid"}`, 410},
		{adminToken, au.CSRF, `{"cookie":"[{\"name\":\"token\",\"value\":\"fixture-secret-never-echo\",\"domain\":\"google.com\"}]"}`, 410},
		{adminToken, au.CSRF, valid, 410},
	} {
		w := request("PUT", "/admin/browser/cookies", tc.body, tc.token, tc.csrf)
		if w.Code != tc.status || strings.Contains(w.Body.String(), "fixture-secret") {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	w := request("GET", "/admin/browser", "", adminToken, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || strings.Contains(w.Body.String(), "SPC_EC") {
		t.Fatal("status exposed cookies")
	}
	var n int
	if e := store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='shopee_cookie_imported'`).Scan(&n); e != nil || n != 0 {
		t.Fatal("failed apply recorded as successful", n, e)
	}
	if e := store.Pool.QueryRow(ctx, `SELECT count(*) FROM browser_credentials`).Scan(&n); e != nil || n != 0 || manager.Status()["starts"] != 0 {
		t.Fatal("retired import stored cookies or launched Chrome", n, e)
	}
}
