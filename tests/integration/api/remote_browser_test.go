package api

import (
	"context"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
 "hoanxu/internal/browser"
	"hoanxu/internal/remotebrowser"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRemoteBrowserAccessRequiresAdminCSRFAndReauthentication(t *testing.T) {
	store, customer, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	staff, err := auth.CreateInternal(ctx, store, "", "display-staff", "Staff", "test-staff-password", "staff", []string{"settings"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false`); err != nil {
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
	ct, cu := session(customer)
	at, au := session(admin)
	st, su := session(staff)
	remote, err := remotebrowser.New(a, "https://backend.example.com")
	if err != nil {
		t.Fatal(err)
	}
	server := &Server{Store: store, Auth: a, Affiliate: &affiliate.Service{Store: store}, RemoteBrowser: remote, Origin: "http://localhost:3000"}
 srv := New(server)
	request := func(token, csrf string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/v1/admin/browser/access", nil)
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
		if w := request(tc.token, tc.csrf); w.Code != tc.code {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=ANY($1::uuid[])`, []string{au.SessionID, su.SessionID}); err != nil {
		t.Fatal(err)
	}
	if w := request(st, su.CSRF); w.Code != 403 {
		t.Fatal("staff controlled display", w.Code, w.Body.String())
	}
	if w := request(at, au.CSRF); w.Code != 503 {
		t.Fatal("missing display should return 503", w.Code, w.Body.String())
	}
	for _, p := range []string{"/browser/screen", "/browser/view/core/rfb.js", "/browser/view/websockify"} {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 401 {
			t.Fatal(p, w.Code)
		}
	}
 // Native development windows retain the same administrator/CSRF/reauth gates.
 server.RemoteBrowser = nil
 server.LocalBrowser = true
 server.Affiliate.Browser = browser.NewManual("missing-test-chromium-executable", t.TempDir())
 for _, tc := range []struct { token, csrf string; code int }{{"", "", 401}, {ct, cu.CSRF, 403}, {st, su.CSRF, 403}, {at, "", 403}, {at, au.CSRF, 503}} {
  if w := request(tc.token, tc.csrf); w.Code != tc.code { t.Fatal("native Chrome access", w.Code, w.Body.String()) }
 }
 if _, err = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=NULL WHERE id=$1`, au.SessionID); err != nil { t.Fatal(err) }
 if w := request(at, au.CSRF); w.Code != 403 { t.Fatal("native Chrome bypassed reauthentication", w.Code) }

}
