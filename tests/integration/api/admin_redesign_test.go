package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/auth"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAdminAccountActionsAreIndependent(t *testing.T) {
	store, _, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	srv := New(&Server{Store: store, Auth: a, Origin: "http://localhost:3000"})
	staff, err := auth.CreateInternal(ctx, store, admin, "redesign-staff", "Staff", "original-password-123", "staff", []string{"orders"})
	if err != nil {
		t.Fatal(err)
	}
	session := func(id string) (string, string) {
		t.Helper()
		if _, e := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, id); e != nil {
			t.Fatal(e)
		}
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
		if _, e = store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, u.SessionID); e != nil {
			t.Fatal(e)
		}
		return token, u.CSRF
	}
	adminToken, csrf := session(admin)
	staffToken, staffCSRF := session(staff)
	request := func(method, path, body, token, csrf string, want int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: got %d want %d: %s", path, w.Code, want, w.Body.String())
		}
		return w
	}
	var original, hash string
	var blocked, mustChange bool
	if e := store.Pool.QueryRow(ctx, `SELECT password_hash FROM internal_credentials WHERE user_id=$1`, staff).Scan(&original); e != nil {
		t.Fatal(e)
	}
	base := "/admin/internal-accounts/" + staff
	if _, e := store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now()-interval '1 day' WHERE user_id=$1`, admin); e != nil {
		t.Fatal(e)
	}
	request("PUT", base+"/permissions", `{"permissions":[]}`, adminToken, csrf, 403)
	if _, e := store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE user_id=$1`, admin); e != nil {
		t.Fatal(e)
	}
	request("PUT", base+"/permissions", `{"permissions":["users"]}`, staffToken, staffCSRF, 403)
	request("PUT", base+"/permissions", `{"permissions":["users"]}`, adminToken, "bad", 403)
	request("PUT", "/admin/internal-accounts/"+admin+"/permissions", `{"permissions":[]}`, adminToken, csrf, 409)
	request("PUT", base+"/permissions", `{}`, adminToken, csrf, 422)
	request("PUT", base+"/permissions", `{"permissions":["internal"]}`, adminToken, csrf, 422)
	request("PUT", base+"/permissions", `{"permissions":["users","notifications"]}`, adminToken, csrf, 200)
	if _, e := a.Session(ctx, staffToken); e == nil {
		t.Fatal("old staff session survived permission change")
	}
	if e := store.Pool.QueryRow(ctx, `SELECT password_hash,must_change FROM internal_credentials WHERE user_id=$1`, staff).Scan(&hash, &mustChange); e != nil || mustChange {
		t.Fatal(e, mustChange)
	}
	if hash != original {
		t.Fatal("editing permissions changed password")
	}
	targetSession, _ := session(staff)
	request("PATCH", base+"/status", `{"blocked":true}`, adminToken, csrf, 200)
	if _, e := a.Session(ctx, targetSession); e == nil {
		t.Fatal("blocking did not revoke sessions")
	}
	request("PATCH", base+"/status", `{"blocked":false}`, adminToken, csrf, 200)
	targetSession, _ = session(staff)
	request("POST", base+"/reset-password", `{"password":"short"}`, adminToken, csrf, 422)
	request("POST", base+"/reset-password", `{"password":"new-password-12345"}`, adminToken, csrf, 200)
	if e := store.Pool.QueryRow(ctx, `SELECT c.password_hash,c.must_change,u.blocked FROM internal_credentials c JOIN users u ON u.id=c.user_id WHERE u.id=$1`, staff).Scan(&hash, &mustChange, &blocked); e != nil {
		t.Fatal(e)
	}
	if _, e := a.Session(ctx, targetSession); e == nil {
		t.Fatal("password reset did not revoke sessions")
	}
	if hash == original || !mustChange || blocked {
		t.Fatal("reset changed unrelated fields or failed", mustChange, blocked)
	}
	var perms []string
	if e := store.Pool.QueryRow(ctx, `SELECT ARRAY(SELECT permission FROM user_permissions WHERE user_id=$1 ORDER BY permission)`, staff).Scan(&perms); e != nil {
		t.Fatal(e)
	}
	if strings.Join(perms, ",") != "notifications,users" {
		t.Fatal("permissions overwritten", perms)
	}
	request("PUT", base+"/permissions", `{"permissions":[]}`, adminToken, csrf, 200)
	if e := store.Pool.QueryRow(ctx, `SELECT ARRAY(SELECT permission FROM user_permissions WHERE user_id=$1)`, staff).Scan(&perms); e != nil || len(perms) != 0 {
		t.Fatal("all permissions were not removed", e, perms)
	}
	var audit int
	if e := store.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE resource=$1 AND action IN ('internal_permissions_updated','internal_status_updated','internal_password_reset')`, staff).Scan(&audit); e != nil || audit != 5 {
		t.Fatal(e, audit)
	}
}

func TestAdminQueuesAndRecipientLookupRespectPermissions(t *testing.T) {
	store, _, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	srv := New(&Server{Store: store, Auth: a})
	for _, permissions := range [][]string{{"orders"}, {"notifications"}, {}} {
		staff, e := auth.CreateInternal(ctx, store, admin, "queue-"+strings.Join(permissions, "-")+"staff", "Staff", "test-password-123", "staff", permissions)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, staff); e != nil {
			t.Fatal(e)
		}
		tx, e := store.Pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		token, e := a.NewSession(ctx, tx, staff)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		get := func(path string) *httptest.ResponseRecorder {
			r := httptest.NewRequest("GET", "/api/v1"+path, nil)
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			return w
		}
		w := get("/admin/work-queues")
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var body struct{ Data map[string]int }
		if e = json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		_, orders := body.Data["pendingOrders"]
		if orders != (len(permissions) > 0 && permissions[0] == "orders") {
			t.Fatal(body.Data)
		}
		if w := get("/admin/internal-accounts/" + admin + "/status"); w.Code != 405 && w.Code != 403 {
			t.Fatal("staff account administration was exposed", w.Code)
		}
		if w := get("/admin/dashboard"); w.Code != 403 {
			t.Fatal("financial dashboard leaked", w.Code)
		}
		if _, ok := body.Data["pendingWithdrawals"]; ok {
			t.Fatal("withdrawal data leaked")
		}
		want := 403
		if len(permissions) > 0 && permissions[0] == "notifications" {
			want = 200
		}
		if w = get("/admin/notification-recipients?q=customer"); w.Code != want {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
