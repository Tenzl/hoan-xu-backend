package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/auth"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestAdminFiltersRunBeforePaginationAndQueuesIncludeLegacy(t *testing.T) {
	store, customer, admin := testStore(t)
	ctx := context.Background()
	var legacy string
	if err := store.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Legacy recipient','','customer') RETURNING id::text`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	bank := store.Encrypt(`{"account":"0123456789","holder":"TEST CUSTOMER"}`)
	for _, id := range []string{customer, legacy} {
		if _, err := store.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,share_bps,ordered_at) SELECT $1,id,'shopee','fixture','MATCH-'||$1::uuid::text,'1','Needle product',10000,1000,500,'pending','approved',5000,now() FROM cashback_policies WHERE mode='fixed'`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Pool.Exec(ctx, `INSERT INTO withdrawals(user_id,amount,bank,bank_details,status) SELECT $1,50000,'Bank',$2,s FROM (VALUES ('pending'),('processing'),('paid'),('rejected')) statuses(s)`, id, bank); err != nil {
			t.Fatal(err)
		}
	}
	// A newer unrelated order must not hide older matching rows.
	if _, err := store.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,share_bps,ordered_at) SELECT $1,id,'shopee','fixture','OTHER','1','Unrelated',10000,1000,500,'rejected','approved',5000,now() FROM cashback_policies WHERE mode='fixed'`, customer); err != nil {
		t.Fatal(err)
	}
	a := &auth.Service{Store: store}
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
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	srv := New(&Server{Store: store, Auth: a})
	get := func(path string, want int) map[string]any {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1"+path, nil)
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		var result map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	first := get("/admin/orders?q=Needle&status=pending&perPage=1&page=1", 200)
	second := get("/admin/orders?q=Needle&status=pending&perPage=1&page=2", 200)
	rows1, rows2 := first["data"].([]any), second["data"].([]any)
	if len(rows1) != 1 || len(rows2) != 1 || rows1[0].(map[string]any)["id"] == rows2[0].(map[string]any)["id"] {
		t.Fatal("search did not paginate matching rows", first, second)
	}
	if first["meta"].(map[string]any)["hasNext"] != true || second["meta"].(map[string]any)["hasNext"] != false {
		t.Fatal("incorrect filtered page metadata", first, second)
	}
	for _, q := range []string{"MATCH-", "Legacy recipient"} {
		rows := get("/admin/orders?q="+url.QueryEscape(q), 200)["data"].([]any)
		if len(rows) == 0 {
			t.Fatal("order search failed", q)
		}
	}
	for _, status := range []string{"pending", "processing", "paid", "rejected"} {
		result := get("/admin/withdrawals?status="+status+"&perPage=1&page=2", 200)
		rows := result["data"].([]any)
		if len(rows) != 1 || rows[0].(map[string]any)["status"] != status || result["meta"].(map[string]any)["hasNext"] != false {
			t.Fatal("withdrawal filter ran after pagination", status, result)
		}
	}
	queue := get("/admin/work-queues", 200)["data"].(map[string]any)
	if queue["pendingOrders"] != float64(2) || queue["pendingWithdrawals"] != float64(2) || queue["processingWithdrawals"] != float64(2) {
		t.Fatal("queues omitted legacy customers", queue)
	}
	recipients := get("/admin/notification-recipients?q=Legacy", 200)["data"].([]any)
	if len(recipients) != 1 {
		t.Fatal(recipients)
	}
	recipient := recipients[0].(map[string]any)
	if len(recipient) != 3 || recipient["id"] != legacy || recipient["name"] != "Legacy recipient" || recipient["email"] != "" {
		t.Fatal("recipient lookup exposed extra fields", recipient)
	}
	get("/admin/orders?status=paid", 422)
	get("/admin/withdrawals?status=approved", 422)
}
