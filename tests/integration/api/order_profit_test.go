package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"hoanxu/internal/auth"
	"hoanxu/internal/orders"
)

func TestAdminOrderProfit(t *testing.T) {
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
	get := func(path, token string, code int) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1"+path, nil)
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != code {
			t.Fatalf("%s: %d: %s", path, w.Code, w.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	insert := func(external, publisher, status string, commission, cash int64) string {
		t.Helper()
		var id string
		err := store.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,share_bps,ordered_at,approved_at) SELECT $1,id,'shopee',$2,$3,'1','Profit product',100000,$4,$5,$6,'approved',5000,now(),CASE WHEN $6='approved' THEN now() END FROM cashback_policies WHERE mode='fixed' RETURNING orders.id::text`, customer, publisher, external, commission, cash, status).Scan(&id)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	check := func(row map[string]any, fee, profit any, state string) {
		t.Helper()
		if row["taxAmount"] != fee || row["projectedProfit"] != profit || row["profitStatus"] != state {
			t.Fatalf("wrong profit: %#v", row)
		}
	}
	pending := insert("PENDING", "publisher", "pending", 10019, 5009)
	negative := insert("LOSS", "publisher", "approved", 10000, 9800)
	rejected := insert("REJECTED", "publisher", "rejected", 20000, 10000)
	manual := insert("MANUAL", "admin-legacy", "approved", 12000, 12000)
	legacy := insert("LEGACY", "legacy-server", "approved", 12000, 12000)
	zero := insert("ZERO", "publisher", "approved", 0, 0)
	tiny := insert("TINY", "publisher", "approved", 19, 10)
	for _, entry := range []struct {
		id          string
		fee, profit any
		state       string
	}{
		{pending, float64(500), float64(4510), "estimated"}, {negative, float64(500), float64(-300), "projected"},
		{rejected, float64(0), float64(0), "excluded"}, {manual, nil, nil, "unavailable"}, {legacy, nil, nil, "unavailable"},
		{zero, float64(0), float64(0), "projected"}, {tiny, float64(0), float64(9), "projected"},
	} {
		check(get("/admin/orders/"+entry.id, adminToken, 200)["data"].(map[string]any), entry.fee, entry.profit, entry.state)
		check(get("/admin/users/"+customer+"/orders/"+entry.id, adminToken, 200)["data"].(map[string]any), entry.fee, entry.profit, entry.state)
	}
	for _, path := range []string{"/admin/orders", "/admin/users/" + customer + "/orders"} {
		rows := get(path, adminToken, 200)["data"].([]any)
		if len(rows) != 7 {
			t.Fatal("missing rows")
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			if _, ok := row["profitStatus"]; !ok {
				t.Fatal("list lacks financial information", row)
			}
		}
	}
	// Profit reporting must remain private and read-only.
	for _, path := range []string{"/orders", "/orders/" + pending} {
		data := get(path, customerToken, 200)["data"]
		rows, ok := data.([]any)
		if !ok {
			rows = []any{data}
		}
		for _, raw := range rows {
			row := raw.(map[string]any)
			for _, key := range []string{"projectedProfit", "taxAmount", "profitStatus"} {
				if _, ok := row[key]; ok {
					t.Fatal("customer response leaks profit", key)
				}
			}
		}
	}
	get("/admin/orders/"+pending, customerToken, 403)
	get("/admin/orders/"+pending, "", 401)
	get("/admin/orders/invalid", adminToken, 422)
	get("/admin/orders/00000000-0000-0000-0000-000000000000", adminToken, 404)
	var transactions int
	if err := store.Pool.QueryRow(ctx, `SELECT count(*) FROM wallet_transactions`).Scan(&transactions); err != nil {
		t.Fatal(err)
	}
	if transactions != 0 {
		t.Fatal("reads wrote ledger", transactions)
	}
	dashboard := get("/admin/dashboard", adminToken, 200)["data"].(map[string]any)
	if dashboard["taxAmount"] != nil || dashboard["projectedProfit"] != nil || dashboard["cashProfit"] != nil || dashboard["profitUnavailableOrders"] != float64(2) {
		t.Fatalf("missing commission must suppress totals: %#v", dashboard)
	}
	// Moving incomplete history outside the dashboard population leaves only known orders.
	var oldCustomer string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Old customer','','customer') RETURNING id::text`).Scan(&oldCustomer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `UPDATE orders SET user_id=$1 WHERE id IN ($2,$3)`, oldCustomer, manual, legacy); err != nil {
		t.Fatal(err)
	}
	// Actual cash totals include only paid withdrawals from the same customer group.
	if _, err := store.Pool.Exec(ctx, `INSERT INTO withdrawals(user_id,amount,bank,bank_details,status) SELECT $1,50000,'Bank','fixture',s FROM (VALUES ('pending'),('processing'),('paid'),('rejected')) statuses(s)`, customer); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Pool.Exec(ctx, `INSERT INTO withdrawals(user_id,amount,bank,bank_details,status) VALUES($1,100000,'Bank','fixture','paid')`, oldCustomer); err != nil {
		t.Fatal(err)
	}
	dashboard = get("/admin/dashboard", adminToken, 200)["data"].(map[string]any)
	if dashboard["taxAmount"] != float64(500) || dashboard["projectedProfit"] != float64(-291) || dashboard["cashProfit"] != float64(-40481) || dashboard["paid"] != float64(50000) || dashboard["profitUnavailableOrders"] != float64(0) {
		t.Fatalf("wrong recognized totals: %#v", dashboard)
	}
	svc := &orders.Service{Store: store}
	if _, err := svc.Event(ctx, admin, pending, "profit-approve", orders.Event{Action: "approved"}); err != nil {
		t.Fatal(err)
	}
	check(get("/admin/orders/"+pending, adminToken, 200)["data"].(map[string]any), float64(500), float64(4510), "projected")
 if _,err:=svc.Event(ctx,admin,pending,"profit-adjust",orders.Event{Action:"adjustment",Reason:"Report correction"});err==nil {t.Fatal("approved order adjusted")}
 check(get("/admin/orders/"+pending,adminToken,200)["data"].(map[string]any),float64(500),float64(4510),"projected")
	staff, err := auth.CreateInternal(ctx, store, admin, "profit-reader", "Reader", "test-staff-password", "staff", []string{"orders"})
	if err != nil {
		t.Fatal(err)
	}
	staffToken := session(staff)
	get("/admin/orders/"+pending, staffToken, 200)
	get("/admin/dashboard", staffToken, 403)
	get("/admin/users/"+customer+"/orders/"+pending, staffToken, 403)
	if _, err := store.Pool.Exec(ctx, `DELETE FROM user_permissions WHERE user_id=$1`, staff); err != nil {
		t.Fatal(err)
	}
	get("/admin/orders/"+pending, staffToken, 403)
}
