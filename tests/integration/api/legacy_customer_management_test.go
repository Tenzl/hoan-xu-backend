package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"hoanxu/internal/auth"
	"hoanxu/internal/wallet"
)

func TestLegacyCustomerManagement(t *testing.T) {
	store, fresh, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	srv := New(&Server{Store: store, Auth: a, Origin: "http://localhost:3000"})
	session := func(id string, recent bool) (string, string) {
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
		u, err := a.Session(ctx, token)
		if err != nil {
			t.Fatal(err)
		}
		if recent {
			if _, err := store.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, u.SessionID); err != nil {
				t.Fatal(err)
			}
		}
		return token, u.CSRF
	}
	token, csrf := session(admin, true)
	var legacy, whitespace string
	for _, entry := range []struct {
		email string
		id    *string
	}{{"", &legacy}, {" \t\n ", &whitespace}} {
		if err := store.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Legacy',$1,'customer') RETURNING id::text`, entry.email).Scan(entry.id); err != nil {
			t.Fatal(err)
		}
	}
	request := func(method, path, body, key, authToken, csrfToken string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if authToken != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: authToken})
		}
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrfToken)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	check := func(t *testing.T, method, path, body, key string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := request(method, path, body, key, token, csrf)
		if w.Code != status {
			t.Fatalf("%s %s: %d, expected %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	base := "/admin/users/" + legacy
	order := `{"productName":"Historical product","orderedAt":"2026-01-01T10:00:00+07:00","cashback":12000,"note":"Manual history"}`
	t.Run("lists split by email and search", func(t *testing.T) {
		for _, entry := range []struct {
			kind  string
			count int
		}{{"new", 1}, {"legacy", 2}, {"", 3}} {
			w := check(t, "GET", "/admin/users?kind="+entry.kind, "", "", 200)
			var body struct{ Data []struct{ ID, Kind string } }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Data) != entry.count {
				t.Fatal(w.Body.String())
			}
			for _, row := range body.Data {
				if entry.kind != "" && row.Kind != entry.kind {
					t.Fatal(w.Body.String())
				}
			}
		}
		check(t, "GET", "/admin/users?kind=unknown", "", "", 422)
		w := check(t, "GET", "/admin/users?kind=legacy&q=Legacy&perPage=1", "", "", 200)
		var body struct {
			Data []any
			Meta struct {
				HasNext bool `json:"hasNext"`
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Data) != 1 || !body.Meta.HasNext {
			t.Fatal(w.Body.String())
		}
	})
	t.Run("new customers are read only", func(t *testing.T) {
		path := "/admin/users/" + fresh
		check(t, "PATCH", path+"/name", `{"name":"Changed"}`, "", 403)
		check(t, "POST", path+"/orders", order, "new-user-order", 403)
		check(t, "PATCH", path, `{"blocked":true,"reason":"Read only test"}`, "", 403)
		var name string
		var blocked bool
		if err := store.Pool.QueryRow(ctx, `SELECT name,blocked FROM users WHERE id=$1`, fresh).Scan(&name, &blocked); err != nil {
			t.Fatal(err)
		}
		if name != "Customer" || blocked {
			t.Fatal(name, blocked)
		}
	})
	t.Run("rename trims and audits without changing email", func(t *testing.T) {
		check(t, "PATCH", base+"/name", `{"name":"  Renamed customer  "}`, "", 200)
		var name, email string
		var audits int
		if err := store.Pool.QueryRow(ctx, `SELECT name,email,(SELECT count(*) FROM audit_logs WHERE resource=$1 AND action='legacy_customer_renamed') FROM users WHERE id=$1::uuid`, legacy).Scan(&name, &email, &audits); err != nil {
			t.Fatal(err)
		}
		if name != "Renamed customer" || email != "" || audits != 1 {
			t.Fatal(name, email, audits)
		}
		for _, value := range []string{"", "   ", strings.Repeat("a", 81)} {
			body, _ := json.Marshal(map[string]string{"name": value})
			check(t, "PATCH", base+"/name", string(body), "", 422)
		}
	})
	t.Run("manual order credits once with concurrent retries", func(t *testing.T) {
		before := time.Now().Add(-time.Second)
		var wg sync.WaitGroup
		responses := make(chan *httptest.ResponseRecorder, 6)
		for i := 0; i < 6; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				responses <- request("POST", base+"/orders", order, "legacy-concurrent", token, csrf)
			}()
		}
		wg.Wait()
		close(responses)
		var id string
		for w := range responses {
			if w.Code != 201 {
				t.Fatal(w.Code, w.Body.String())
			}
			var body struct{ Data struct{ ID string } }
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if id != "" && body.Data.ID != id {
				t.Fatal("retry created a second order")
			}
			id = body.Data.ID
		}
		var cash, commission, value, balance int64
		var share, credits, audits, events int
		var status, publisher string
		var approved time.Time
		err := store.Pool.QueryRow(ctx, `SELECT o.cashback,o.commission,o.value,o.share_bps,o.status,o.publisher,o.approved_at,(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT count(*) FROM wallet_transactions WHERE reference='order_credit:'||o.id::text),(SELECT count(*) FROM audit_logs WHERE resource=o.id::text AND action='legacy_manual_order'),(SELECT count(*) FROM order_events WHERE order_id=o.id) FROM orders o WHERE o.id=$2`, legacy, id).Scan(&cash, &commission, &value, &share, &status, &publisher, &approved, &balance, &credits, &audits, &events)
		if err != nil {
			t.Fatal(err)
		}
		if cash != 12000 || commission != cash || value != 0 || share != 10000 || status != "approved" || publisher != "admin-legacy" || approved.Before(before) || balance != cash || credits != 1 || audits != 1 || events != 1 {
			t.Fatal(cash, commission, value, share, status, publisher, approved, balance, credits, audits, events)
		}
		if invalid, err := wallet.FullReconcile(ctx, store); err != nil || invalid != 0 {
			t.Fatal(invalid, err)
		}
		w := check(t, "GET", base+"/orders/"+id, "", "", 200)
		var detail struct {
			Data struct {
				IsManual bool `json:"isManual"`
				Note     string
			}
		}
		if err := json.Unmarshal(w.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		if !detail.Data.IsManual || detail.Data.Note != "Manual history" {
			t.Fatal(w.Body.String())
		}
		check(t, "POST", base+"/orders", strings.Replace(order, "12000", "13000", 1), "legacy-concurrent", 409)
		var currentMode string
		if err := store.Pool.QueryRow(ctx, `SELECT mode FROM cashback_policies WHERE mode='tiered' ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&currentMode); err != nil || currentMode != "tiered" {
			t.Fatal(currentMode, err)
		}
	})
	t.Run("invalid input and transaction rollback", func(t *testing.T) {
		for i, body := range []string{strings.Replace(order, "12000", "0", 1), strings.Replace(order, "12000", "-1", 1), strings.Replace(order, "12000", "1000000000001", 1), strings.Replace(order, "Historical product", "", 1), strings.Replace(order, "2026-01-01T10:00:00+07:00", "2099-01-01T10:00:00+07:00", 1), strings.Replace(order, "Manual history", strings.Repeat("a", 501), 1)} {
			check(t, "POST", base+"/orders", body, fmt.Sprintf("invalid-order-%d", i), 422)
		}
		check(t, "POST", base+"/orders", order, "", 422)
		// Force failure after inserting the order: the whole transaction must roll back.
		if _, err := store.Pool.Exec(ctx, `CREATE FUNCTION reject_manual_credit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.reference LIKE 'order_credit:%' THEN RAISE EXCEPTION 'fixture rollback'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_manual_credit BEFORE INSERT ON wallet_transactions FOR EACH ROW EXECUTE FUNCTION reject_manual_credit();`); err != nil {
			t.Fatal(err)
		}
		check(t, "POST", "/admin/users/"+whitespace+"/orders", order, "rollback-order", 500)
		var count int
		if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id=$1`, whitespace).Scan(&count); err != nil || count != 0 {
			t.Fatal(count, err)
		}
		if _, err := store.Pool.Exec(ctx, `DROP TRIGGER reject_manual_credit ON wallet_transactions; DROP FUNCTION reject_manual_credit();`); err != nil {
			t.Fatal(err)
		}
		check(t, "POST", "/admin/users/"+whitespace+"/orders", order, "rollback-order", 201)
	})
	t.Run("mutations require permissions csrf and recent authentication", func(t *testing.T) {
		for _, permissions := range [][]string{{"users"}, {"orders"}, {"users", "orders"}} {
			id, err := auth.CreateInternal(ctx, store, admin, fmt.Sprintf("staff-%d", len(permissions))+strings.Join(permissions, "-"), "Staff", "test-staff-password", "staff", permissions)
			if err != nil {
				t.Fatal(err)
			}
			staffToken, staffCSRF := session(id, true)
			for _, op := range []struct{ method, path, body string }{{"PATCH", base + "/name", `{"name":"Staff renamed"}`}, {"POST", base + "/orders", order}} {
				w := request(op.method, op.path, op.body, "staff-write-key", staffToken, staffCSRF)
				want := 403
				if len(permissions) == 2 {
					want = 200
					if op.method == "POST" {
						want = 201
					}
				}
				if w.Code != want {
					t.Fatal(w.Code, w.Body.String())
				}
			}
		}
		staleToken, staleCSRF := session(admin, false)
		for _, op := range []struct{ method, path, body string }{{"PATCH", base + "/name", `{"name":"Forbidden"}`}, {"POST", base + "/orders", order}} {
			for _, credentials := range []struct {
				token, csrf string
				status      int
			}{{token, "", 403}, {staleToken, staleCSRF, 403}, {"", "", 401}} {
				w := request(op.method, op.path, op.body, "unauthorized-key", credentials.token, credentials.csrf)
				if w.Code != credentials.status {
					t.Fatal(w.Code, w.Body.String())
				}
			}
		}
	})
}

func TestAdminDashboardOnlyNewCustomers(t *testing.T) {
	store, fresh, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	srv := New(&Server{Store: store, Auth: a})
	if _, err := store.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin); err != nil {
		t.Fatal(err)
	}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	token, err := a.NewSession(ctx, tx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var legacy string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Legacy',E' \t ','customer') RETURNING id::text`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct {
		id         string
		multiplier int
	}{{fresh, 1}, {legacy, 10}} {
		if _, err := store.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at) SELECT $1::uuid,id,'shopee','fixture',s||$1::text,'line','Product',0,1000*$2::bigint,400*$2::bigint,s,s,now(),CASE WHEN s='approved' THEN now() END FROM cashback_policies CROSS JOIN (VALUES ('approved'),('pending'),('rejected')) statuses(s) WHERE mode='fixed'`, entry.id, entry.multiplier); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id) SELECT $1::uuid,'shopee','link','link',$1::text,id FROM cashback_policies WHERE mode='fixed'`, entry.id); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO withdrawals(user_id,amount,bank,bank_details,status) SELECT $1,50000*$2::bigint,'Bank','details',s FROM (VALUES ('pending'),('processing'),('paid')) statuses(s)`, entry.id, entry.multiplier); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO gift_redemptions(user_id,gift_id,cost,cost_xu,status) VALUES($1,'g1',50000,50000,'pending')`, entry.id); err != nil {
			t.Fatal(err)
		}
	}
	dashboard := func() map[string]int64 {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/admin/dashboard", nil)
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var body struct{ Data map[string]int64 }
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Data
	}
	expected := map[string]int64{"commission": 1000, "cashback": 400, "retained": 600, "pendingCommission": 1000, "pendingOrders": 1, "users": 1, "links": 1, "pendingWithdrawals": 2, "pendingGifts": 1, "paid": 50000}
	for key, want := range expected {
		if got := dashboard()[key]; got != want {
			t.Fatalf("%s=%d, want %d", key, got, want)
		}
	}
	if _, err := store.Pool.Exec(ctx, `UPDATE users SET email='' WHERE id=$1`, fresh); err != nil {
		t.Fatal(err)
	}
	for key, value := range dashboard() {
		if value != 0 {
			t.Fatalf("legacy-only dashboard %s=%d", key, value)
		}
	}
}
