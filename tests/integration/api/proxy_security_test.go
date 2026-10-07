package api

import (
	"context"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSameProxySeparatesAuthenticatedUsersAndDoesNotReadBank(t *testing.T) {
	store, user, other := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	session := func(id string) string {
		tx, err := store.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		token, err := a.NewSession(ctx, tx, id)
		if err == nil {
			err = tx.Commit(ctx)
		}
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	first, second := session(user), session(other)
	if _, err := store.Pool.Exec(ctx, `UPDATE users SET bank_details='invalid-cipher' WHERE id=$1`, user); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO rate_limit_buckets(key,window_id,count,expires_at) VALUES($1,floor(extract(epoch FROM now())/60)::bigint,240,now()+interval '1 minute')`, "api:user:"+user); err != nil {
		t.Fatal(err)
	}
	server := New(&Server{Store: store, Auth: a, Affiliate: &affiliate.Service{Store: store}, Origin: "http://localhost:3000"})
	call := func(token string) int {
		r := httptest.NewRequest("GET", "/api/v1/me", nil)
		r.RemoteAddr = "127.0.0.1:4321"
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		return w.Code
	}
	if status := call(first); status != 429 {
		t.Fatal("limit not applied", status)
	}
	if status := call(second); status != 200 {
		t.Fatal("proxy users share bucket", status)
	}
	if _, err := store.Pool.Exec(ctx, `DELETE FROM rate_limit_buckets WHERE key=$1`, "api:user:"+user); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", "/api/v1/wallet", nil)
	r.AddCookie(&http.Cookie{Name: "hx_session", Value: first})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatal("bank decrypted outside /me", w.Code, w.Body.String())
	}
	if status := call(first); status != 500 {
		t.Fatal("profile did not load sensitive data", status)
	}
}

func TestProxyProofCannotSelectAnonymousBucketWithoutSignature(t *testing.T) {
	store, _, _ := testStore(t)
	key := strings.Repeat("a", 64)
	ctx := context.Background()
	if _, err := store.Pool.Exec(ctx, `INSERT INTO rate_limit_buckets(key,window_id,count,expires_at) VALUES('api:ip:127.0.0.1',floor(extract(epoch FROM now())/60)::bigint,240,now()+interval '1 minute')`); err != nil {
		t.Fatal(err)
	}
	server := New(&Server{Store: store, Auth: &auth.Service{Store: store}, Affiliate: &affiliate.Service{Store: store}, Origin: "http://localhost:3000", ProxySigningKey: key})
	r := httptest.NewRequest("GET", "/api/v1/config", nil)
	r.RemoteAddr = "127.0.0.1:4321"
	r.Header.Set("X-HX-Client-IP", "203.0.113.7")
	r.Header.Set("X-Forwarded-For", "203.0.113.7")
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != 429 {
		t.Fatal("spoofed identity", w.Code, w.Body.String())
	}
}
