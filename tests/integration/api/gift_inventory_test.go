package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hoanxu/internal/auth"
	"hoanxu/internal/platform"
	"hoanxu/internal/rewards"
	"hoanxu/internal/wallet"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestGiftInventoryAndOutOfStock(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	svc := &rewards.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.CreditGreen(ctx, tx, customer, "inventory-fund", "Test", 100000); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	created, err := svc.CreateGift(ctx, admin, "create-gift-0001", rewards.GiftInput{Name: "Tai nghe", Channel: "shopee", CostXu: 12000, Stock: 3, Active: true, Icon: "headphones"})
	if err != nil {
		t.Fatal(err)
	}
	gift := created.(rewards.Gift)
	if gift.ID == "" || gift.Icon != "headphones" {
		t.Fatal(created)
	}
	if _, err = svc.CreateGift(ctx, admin, "create-gift-0001", rewards.GiftInput{Name: "Tai nghe", Channel: "shopee", CostXu: 12000, Stock: 3, Active: true, Icon: "headphones"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM gift_catalog WHERE name='Tai nghe'`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	v, err := svc.Redeem(ctx, customer, "inventory-redeem", gift.ID)
	if err != nil {
		t.Fatal(err)
	}
	redemption := v.(map[string]any)["id"].(string)
	stock, expected := 8, 3
	_, err = svc.UpdateGift(ctx, admin, gift.ID, "stale-stock-0001", rewards.GiftPatch{Stock: &stock, ExpectedStock: &expected})
	var pe *platform.Error
	if !errors.As(err, &pe) || pe.Code != "GIFT_STOCK_CHANGED" {
		t.Fatal(err)
	}
	price := int64(25000)
	if _, err = svc.UpdateGift(ctx, admin, gift.ID, "price-only-0001", rewards.GiftPatch{CostXu: &price}); err != nil {
		t.Fatal(err)
	}
	// Contending bulk actions, including different keys, must only refund once.
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := svc.OutOfStock(ctx, admin, gift.ID, fmt.Sprintf("out-of-stock-%d", i))
			if e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	var available, held int64
	var status, reason, body string
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_gift_held'),(SELECT stock FROM gift_catalog WHERE id=$2),status,reason,(SELECT body FROM notifications WHERE recipient_id=$1 ORDER BY created_at DESC LIMIT 1) FROM gift_redemptions WHERE id=$3`, customer, gift.ID, redemption).Scan(&available, &held, &stock, &status, &reason, &body)
	if err != nil || available != 100000 || held != 0 || stock != 0 || status != "rejected" || reason != "Hàng đã hết" || !strings.Contains(body, "Chào Customer") || !strings.Contains(body, "12.000 Xu") {
		t.Fatal(available, held, stock, status, reason, body, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1`, customer).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	stock, expected = 4, 0
	if _, err = svc.UpdateGift(ctx, admin, gift.ID, "restock-gift-001", rewards.GiftPatch{Stock: &stock, ExpectedStock: &expected}); err != nil {
		t.Fatal(err)
	}
	// A replay after replenishing must not empty the stock again.
	if _, err = svc.OutOfStock(ctx, admin, gift.ID, "out-of-stock-0"); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT stock FROM gift_catalog WHERE id=$1`, gift.ID).Scan(&stock); err != nil || stock != 4 {
		t.Fatal(stock, err)
	}
	assertLedger(t, s)
}

func TestGiftImageDescriptionWithoutChannel(t *testing.T) {
	s, _, admin := testStore(t)
	ctx := context.Background()
	svc := &rewards.Service{Store: s}
	input := rewards.GiftInput{Name: "Phiếu ăn Jollibee", CostXu: 25000, Stock: 4, Active: true, ImageURL: "https://example.com/jollibee.png", Description: "Phiếu ăn 100.000đ\nÁp dụng tại cửa hàng."}
	created, err := svc.CreateGift(ctx, admin, "gift-image-create", input)
	if err != nil {
		t.Fatal(err)
	}
	gift := created.(rewards.Gift)
	var channelNull bool
	var description, image string
	var stock int
	if err = s.Pool.QueryRow(ctx, `SELECT channel IS NULL,description,image_url,stock FROM gift_catalog WHERE id=$1`, gift.ID).Scan(&channelNull, &description, &image, &stock); err != nil || !channelNull || description != input.Description || image != input.ImageURL || stock != 4 {
		t.Fatal(channelNull, description, image, stock, err)
	}
	for i, url := range []string{"http://example.com/a.png", "javascript:alert(1)", "https://", "https://user:pass@example.com/a.png", "https://example.com/a b.png"} {
		input.ImageURL = url
		if _, err = svc.CreateGift(ctx, admin, fmt.Sprintf("invalid-image-%d", i), input); err == nil {
			t.Fatal("accepted invalid image", url)
		}
	}
	description = strings.Repeat("a", 2001)
	if _, err = svc.UpdateGift(ctx, admin, gift.ID, "long-description", rewards.GiftPatch{Description: &description}); err == nil {
		t.Fatal("accepted oversized description")
	}
	description = "<script>alert(1)</script>\nĐiều kiện sử dụng"
	image = ""
	if _, err = svc.UpdateGift(ctx, admin, gift.ID, "edit-description", rewards.GiftPatch{Description: &description, ImageURL: &image}); err != nil {
		t.Fatal(err)
	}
	var cost int64
	if err = s.Pool.QueryRow(ctx, `SELECT description,image_url,stock,cost FROM gift_catalog WHERE id=$1`, gift.ID).Scan(&description, &image, &stock, &cost); err != nil || image != "" || stock != 4 || cost != 25000 {
		t.Fatal(description, image, stock, cost, err)
	}
	description = ""
	if _, err = svc.UpdateGift(ctx, admin, gift.ID, "clear-description", rewards.GiftPatch{Description: &description}); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT description FROM gift_catalog WHERE id=$1`, gift.ID).Scan(&description); err != nil || description != "" {
		t.Fatal(description, err)
	}
	input.ImageURL = ""
	input.Description = ""
	if _, err = svc.CreateGift(ctx, admin, "gift-no-image", input); err != nil {
		t.Fatal(err)
	}
}

func TestGiftCompletionAndOutOfStockRace(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	svc := &rewards.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.CreditGreen(ctx, tx, customer, "gift-race-funds", "Test", 10000); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	expectedAvailable := int64(10000)
	for i := 0; i < 5; i++ {
		created, err := svc.CreateGift(ctx, admin, fmt.Sprintf("race-create-%d", i), rewards.GiftInput{Name: "Quà chạy đồng thời", Channel: "shopee", CostXu: 1000, Stock: 1, Active: true, Icon: "gift"})
		if err != nil {
			t.Fatal(err)
		}
		gift := created.(rewards.Gift)
		v, err := svc.Redeem(ctx, customer, fmt.Sprintf("race-redeem-%d", i), gift.ID)
		if err != nil {
			t.Fatal(err)
		}
		id := v.(map[string]any)["id"].(string)
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() {
			<-start
			_, e := svc.GiftEvent(ctx, admin, id, fmt.Sprintf("race-complete-%d", i), "completed", "SECRET-RACE", "")
			errs <- e
		}()
		go func() {
			<-start
			_, e := svc.OutOfStock(ctx, admin, gift.ID, fmt.Sprintf("race-empty-%d", i))
			errs <- e
		}()
		close(start)
		for j := 0; j < 2; j++ {
			e := <-errs
			var pe *platform.Error
			if e != nil && (!errors.As(e, &pe) || pe.Code != "INVALID_TRANSITION") {
				t.Fatal(e)
			}
		}
		var status string
		var held, available int64
		var stock, notices int
		err = s.Pool.QueryRow(ctx, `SELECT status,(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_gift_held'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available'),(SELECT stock FROM gift_catalog WHERE id=$2),(SELECT count(*) FROM notifications WHERE recipient_id=$1) FROM gift_redemptions WHERE id=$3`, customer, gift.ID, id).Scan(&status, &held, &available, &stock, &notices)
		if status == "completed" {
			expectedAvailable -= 1000
		} else if status != "rejected" {
			t.Fatal(status)
		}
		if err != nil || held != 0 || available != expectedAvailable || stock != 0 || notices != i+1 {
			t.Fatal(status, held, available, stock, notices, err)
		}
	}
	assertLedger(t, s)
}

func TestGiftIconMigrationPreservesInventoryAndLedger(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	digest := `SELECT md5(string_agg((to_jsonb(g)-'icon')::text,E'\n' ORDER BY id)) FROM gift_catalog g`
	var before, after string
	if err := s.Pool.QueryRow(ctx, digest).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for _, direction := range []string{"down", "up"} {
		raw, err := os.ReadFile("../../database/migrations/000022_gift_icons." + direction + ".sql")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Pool.Exec(ctx, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Pool.QueryRow(ctx, digest).Scan(&after); err != nil || before != after {
		t.Fatal("inventory changed", before, after, err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM gift_catalog WHERE icon<>'gift'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("wrong default icon", count, err)
	}
	assertLedger(t, s)
}

func TestGiftReserveAndOutOfStockRace(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	svc := &rewards.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.CreditGreen(ctx, tx, customer, "reserve-race-funds", "Test", 10000); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		created, err := svc.CreateGift(ctx, admin, fmt.Sprintf("reserve-create-%d", i), rewards.GiftInput{Name: "Quà giữ đồng thời", Channel: "shopee", CostXu: 1000, Stock: 1, Active: true, Icon: "gift"})
		if err != nil {
			t.Fatal(err)
		}
		gift := created.(rewards.Gift)
		start := make(chan struct{})
		errs := make(chan error, 2)
		go func() {
			<-start
			_, e := svc.Redeem(ctx, customer, fmt.Sprintf("reserve-race-%d", i), gift.ID)
			errs <- e
		}()
		go func() {
			<-start
			_, e := svc.OutOfStock(ctx, admin, gift.ID, fmt.Sprintf("empty-race-%d", i))
			errs <- e
		}()
		close(start)
		for j := 0; j < 2; j++ {
			e := <-errs
			var pe *platform.Error
			if e != nil && (!errors.As(e, &pe) || pe.Code != "OUT_OF_STOCK") {
				t.Fatal(e)
			}
		}
		var available, held int64
		var stock, pending int
		err = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_gift_held'),stock,(SELECT count(*) FROM gift_redemptions WHERE gift_id=$2 AND status='pending') FROM gift_catalog WHERE id=$2`, customer, gift.ID).Scan(&available, &held, &stock, &pending)
		if err != nil || available != 10000 || held != 0 || stock != 0 || pending != 0 {
			t.Fatal(available, held, stock, pending, err)
		}
	}
	assertLedger(t, s)
}

func TestGiftBulkRefundAllCustomersAndRollback(t *testing.T) {
	s, _, admin := testStore(t)
	ctx := context.Background()
	svc := &rewards.Service{Store: s}
	if _, err := s.Pool.Exec(ctx, `UPDATE gift_catalog SET cost=1000,stock=40 WHERE id IN ('g1','g2')`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 25; i++ {
		var user string
		if err := s.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES($1,$2,'customer') RETURNING id::text`, fmt.Sprintf("Khách %d", i), fmt.Sprintf("bulk%d@example.com", i)).Scan(&user); err != nil {
			t.Fatal(err)
		}
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = platform.Accounts(ctx, tx, user); err != nil {
			t.Fatal(err)
		}
		if err = wallet.CreditGreen(ctx, tx, user, "bulk-fund:"+user, "Test", 5000); err != nil {
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Redeem(ctx, user, "bulk-redeem-one", "g1"); err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Redeem(ctx, user, "bulk-redeem-two", "g2"); err != nil {
			t.Fatal(err)
		}
	}
	var before string
	digest := `SELECT md5(string_agg(to_jsonb(t)::text,E'\n' ORDER BY to_jsonb(t)::text)) FROM wallet_entries t`
	if err := s.Pool.QueryRow(ctx, digest).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool.Exec(ctx, `CREATE FUNCTION fail_gift_notice() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test notification failure'; END $$; CREATE TRIGGER fail_gift_notice BEFORE INSERT ON notifications FOR EACH ROW EXECUTE FUNCTION fail_gift_notice()`); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.OutOfStock(ctx, admin, "g1", "bulk-failure-key"); err == nil {
		t.Fatal("expected rollback")
	}
	var after string
	var stock, count int
	if err := s.Pool.QueryRow(ctx, digest).Scan(&after); err != nil || before != after {
		t.Fatal("ledger changed on rollback", err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT stock,(SELECT count(*) FROM gift_redemptions WHERE gift_id='g1' AND status='pending') FROM gift_catalog WHERE id='g1'`).Scan(&stock, &count); err != nil || stock != 15 || count != 25 {
		t.Fatal(stock, count, err)
	}
	if _, err := s.Pool.Exec(ctx, `DROP TRIGGER fail_gift_notice ON notifications;DROP FUNCTION fail_gift_notice()`); err != nil {
		t.Fatal(err)
	}
	v, err := svc.OutOfStock(ctx, admin, "g1", "bulk-failure-key")
	if err != nil {
		t.Fatal(err)
	}
	result := v.(rewards.OutOfStockResult)
	if result.RefundedCount != 25 || result.RefundedXu != 25000 {
		t.Fatal(result)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM gift_redemptions WHERE gift_id='g2' AND status='pending'`).Scan(&count); err != nil || count != 25 {
		t.Fatal("other gift affected", count, err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE title='Hoàn Xu vì hết hàng'`).Scan(&count); err != nil || count != 25 {
		t.Fatal(count, err)
	}
	assertLedger(t, s)
}

func TestGiftInventoryAPIGatesAndFilters(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: s}
	srv := New(&Server{Store: s, Auth: a, Origin: "http://localhost:3000"})
	session := func(id string, recent bool) (string, string) {
		t.Helper()
		if _, err := s.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, id); err != nil {
			t.Fatal(err)
		}
		tx, err := s.Pool.Begin(ctx)
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
		if recent {
			if _, err = s.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, u.SessionID); err != nil {
				t.Fatal(err)
			}
		}
		return token, u.CSRF
	}
	token, csrf := session(admin, true)
	oldToken, oldCSRF := session(admin, false)
	customerToken, customerCSRF := session(customer, true)
	staff, err := auth.CreateInternal(ctx, s, admin, "gift-denied-staff", "Staff", "test-staff-password", "staff", nil)
	if err != nil {
		t.Fatal(err)
	}
	staffToken, staffCSRF := session(staff, true)
	request := func(method, path, body, key, token, csrf string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	create := `{"name":"Voucher mới","channel":"shopee","costXu":5000,"stock":2,"active":true,"icon":"ticket"}`
	for _, op := range []struct{ method, path, body string }{{"POST", "/admin/gifts", create}, {"PATCH", "/admin/gifts/g1", `{"costXu":12000}`}, {"POST", "/admin/gifts/g1/out-of-stock", ""}} {
		for _, gate := range []struct {
			token, csrf, key string
			status           int
		}{{"", "", "gate-key-001", 401}, {customerToken, customerCSRF, "gate-key-001", 403}, {staffToken, staffCSRF, "gate-key-001", 403}, {oldToken, oldCSRF, "gate-key-001", 403}, {token, "", "gate-key-001", 403}, {token, csrf, "", 422}} {
			w := request(op.method, op.path, op.body, gate.key, gate.token, gate.csrf)
			if w.Code != gate.status {
				t.Fatalf("%s gate %d: %d %s", op.path, gate.status, w.Code, w.Body.String())
			}
		}
	}
	w := request("POST", "/admin/gifts", create, "api-create-gift1", token, csrf)
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	var body struct{ Data rewards.Gift }
	if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	id := body.Data.ID
	for _, tc := range []struct {
		payload string
		status  int
	}{{`{"stock":3}`, 422}, {`{"stock":3,"expectedStock":1}`, 409}, {`{"costXu":1.5}`, 400}, {`{"icon":"<svg>"}`, 422}, {`{"channel":"invalid"}`, 422}, {`{"name":""}`, 422}, {`{"stock":-1,"expectedStock":2}`, 422}, {`{"stock":3,"expectedStock":2}`, 200}} {
		w = request("PATCH", "/admin/gifts/"+id, tc.payload, "patch-"+platform.Hash(tc.payload)[:12], token, csrf)
		if w.Code != tc.status {
			t.Fatal(tc, w.Code, w.Body.String())
		}
	}
	w = request("PATCH", "/admin/gifts/missing", `{"costXu":1000}`, "missing-gift-001", token, csrf)
	if w.Code != 404 {
		t.Fatal(w.Code, w.Body.String())
	}
	svc := &rewards.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.CreditGreen(ctx, tx, customer, "filter-fund", "Test", 10000); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Redeem(ctx, customer, "filter-redeem-01", id); err != nil {
		t.Fatal(err)
	}
	w = request("GET", "/admin/gift-redemptions?status=pending&giftId="+id, "", "", token, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), id) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/admin/gift-redemptions?status=completed&giftId="+id, "", "", token, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"data":[]`) {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request("GET", "/admin/gifts", "", "", token, csrf)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"pendingCount":1`) || !strings.Contains(w.Body.String(), `"pendingXu":5000`) {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestGiftRefundSingleAndCompletion(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	svc := &rewards.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = wallet.CreditGreen(ctx, tx, customer, "single-fund", "Test", 60000); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE gift_catalog SET cost=12000,stock=1 WHERE id IN ('g1','g2')`); err != nil {
		t.Fatal(err)
	}
	a, err := svc.Redeem(ctx, customer, "single-redeem-a", "g1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := svc.Redeem(ctx, customer, "single-redeem-b", "g2")
	if err != nil {
		t.Fatal(err)
	}
	id := a.(map[string]any)["id"].(string)
	if _, err = svc.GiftEvent(ctx, admin, id, "single-refund-01", "refund_out_of_stock", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.GiftEvent(ctx, admin, id, "single-refund-01", "refund_out_of_stock", "", ""); err != nil {
		t.Fatal(err)
	}
	var stock int
	if err = s.Pool.QueryRow(ctx, `SELECT stock FROM gift_catalog WHERE id='g1'`).Scan(&stock); err != nil || stock != 0 {
		t.Fatal(stock, err)
	}
	completed := b.(map[string]any)["id"].(string)
	if _, err = svc.GiftEvent(ctx, admin, completed, "complete-single", "completed", "SECRET-VOUCHER", ""); err != nil {
		t.Fatal(err)
	}
	result, err := svc.OutOfStock(ctx, admin, "g2", "completed-out-01")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result)
	if !strings.Contains(string(raw), `"refundedCount":0`) {
		t.Fatal(string(raw))
	}
	var body string
	if err = s.Pool.QueryRow(ctx, `SELECT body FROM notifications WHERE title='Voucher đã sẵn sàng' AND recipient_id=$1`, customer).Scan(&body); err != nil || !strings.Contains(body, "Customer") || strings.Contains(body, "SECRET-VOUCHER") {
		t.Fatal(body, err)
	}
	assertLedger(t, s)
}
