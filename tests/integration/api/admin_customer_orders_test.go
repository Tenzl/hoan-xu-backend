package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"hoanxu/internal/auth"
)

func TestAdminCustomerOrders(t *testing.T) {
	store, customer, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	srv := New(&Server{Store: store, Auth: a})
	session := func(id string) string {
		t.Helper()
		if _, err := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, id); err != nil {
			t.Fatal(err)
		}
		tx, err := store.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		token, err := a.NewSession(ctx, tx, id)
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return token
	}
	adminToken, customerToken := session(admin), session(customer)
	staff := map[string]string{}
	for _, entry := range []struct {
		name        string
		permissions []string
	}{
		{"both", []string{"users", "orders"}}, {"users", []string{"users"}},
		{"orders", []string{"orders"}}, {"neither", nil},
	} {
		id, err := auth.CreateInternal(ctx, store, admin, "staff-"+entry.name, "Staff", "test-staff-password", "staff", entry.permissions)
		if err != nil {
			t.Fatal(err)
		}
		staff[entry.name] = session(id)
	}
	var other, empty string
	for _, entry := range []struct {
		email string
		id    *string
	}{{"other@example.com", &other}, {"empty@example.com", &empty}} {
		if err := store.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Other',$1,'customer') RETURNING id::text`, entry.email).Scan(entry.id); err != nil {
			t.Fatal(err)
		}
	}
	insertOrder := func(owner, externalID, status, when string) string {
		t.Helper()
		var id string
		err := store.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at) SELECT $1,id,'shopee','publisher',$2,'line','Product',100000,10000,5000,$3,$3,$4::timestamptz,CASE WHEN $3='approved' THEN $4::timestamptz END FROM cashback_policies WHERE mode='fixed' RETURNING orders.id::text`, owner, externalID, status, when).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	old := insertOrder(customer, "OLD", "pending", "2026-10-01T12:00:00+07:00")
	newer := insertOrder(customer, "NEW", "approved", "2026-10-02T12:00:00+07:00")
	latest := insertOrder(customer, "LATEST", "rejected", "2026-10-03T12:00:00+07:00")
	foreign := insertOrder(other, "FOREIGN", "pending", "2026-10-04T12:00:00+07:00")
	base := "/admin/users/" + customer
	get := func(t *testing.T, path, token string, status int) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1"+path, nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("%s: got %d, want %d: %s", path, w.Code, status, w.Body.String())
		}
		return w
	}
	listIDs := func(t *testing.T, path string, expected []string, hasNext bool) {
		t.Helper()
		w := get(t, path, adminToken, 200)
		var body struct {
			Data []struct {
				ID     string
				UserID string `json:"userId"`
			}
			Meta struct {
				HasNext bool `json:"hasNext"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Data) != len(expected) || body.Meta.HasNext != hasNext {
			t.Fatalf("unexpected list: %s", w.Body.String())
		}
		for i, row := range body.Data {
			if row.ID != expected[i] || row.UserID != customer {
				t.Fatalf("wrong owner/order: %s", w.Body.String())
			}
		}
	}
	t.Run("customer summary and owned detail", func(t *testing.T) {
		w := get(t, base, adminToken, 200)
		var summary struct {
			Data struct {
				ID, Name, Email, Role     string
				Available, Held, GiftHeld int64
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &summary); err != nil {
			t.Fatal(err)
		}
		if summary.Data.ID != customer || summary.Data.Email != "test@example.com" || summary.Data.Role != "customer" {
			t.Fatal(w.Body.String())
		}
		w = get(t, base+"/orders/"+old, adminToken, 200)
		var detail struct {
			Data struct {
				ID                          string
				UserID                      string `json:"userId"`
				ExternalID                  string `json:"externalId"`
				Value, Commission, Cashback int64
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if detail.Data.ID != old || detail.Data.UserID != customer || detail.Data.ExternalID != "OLD" || detail.Data.Value != 100000 || detail.Data.Commission != 10000 || detail.Data.Cashback != 5000 {
			t.Fatal(w.Body.String())
		}
	})
	t.Run("filter and pagination isolate customer", func(t *testing.T) {
		listIDs(t, base+"/orders", []string{latest, newer, old}, false)
		listIDs(t, base+"/orders?perPage=2", []string{latest, newer}, true)
		listIDs(t, base+"/orders?perPage=2&page=2", []string{old}, false)
		listIDs(t, base+"/orders?perPage=2&page=3", nil, false)
		for status, id := range map[string]string{"pending": old, "approved": newer, "rejected": latest} {
			listIDs(t, base+"/orders?status="+status, []string{id}, false)
		}
		listIDs(t, "/admin/users/"+empty+"/orders", nil, false)
	})
	t.Run("requires both permissions on all reads", func(t *testing.T) {
		for _, path := range []string{base, base + "/orders", base + "/orders/" + old} {
			get(t, path, adminToken, 200)
			get(t, path, staff["both"], 200)
			for _, name := range []string{"users", "orders", "neither"} {
				get(t, path, staff[name], 403)
			}
			get(t, path, customerToken, 403)
			get(t, path, "", 401)
		}
	})
	t.Run("invalid IDs and status", func(t *testing.T) {
		for _, path := range []string{"/admin/users/invalid", "/admin/users/invalid/orders", "/admin/users/invalid/orders/" + old, base + "/orders/invalid", base + "/orders?status=unknown"} {
			get(t, path, adminToken, 422)
		}
	})
	t.Run("missing customers and foreign orders", func(t *testing.T) {
		missing := "00000000-0000-0000-0000-000000000000"
		for _, id := range []string{missing, admin} {
			get(t, "/admin/users/"+id, adminToken, 404)
			get(t, "/admin/users/"+id+"/orders", adminToken, 404)
			get(t, "/admin/users/"+id+"/orders/"+old, adminToken, 404)
		}
		get(t, base+"/orders/"+missing, adminToken, 404)
		get(t, base+"/orders/"+foreign, adminToken, 404)
	})
}
