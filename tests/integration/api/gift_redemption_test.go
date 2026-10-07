package api

import (
	"context"
	"encoding/json"
	"errors"
	"hoanxu/internal/auth"
	"hoanxu/internal/platform"
	"hoanxu/internal/rewards"
	"hoanxu/internal/wallet"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestGiftPriceChangeDoesNotChargeCustomer(t *testing.T) {
	s, customer, _ := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = wallet.CreditGreen(ctx, tx, customer, "gift-price-fund", "Test", 30000); err != nil {
		t.Fatal(err)
	}
	token, err := a.NewSession(ctx, tx, customer)
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
	if _, err = s.Pool.Exec(ctx, `UPDATE gift_catalog SET cost=12000,stock=1 WHERE id='g1'`); err != nil {
		t.Fatal(err)
	}
	h := New(&Server{Store: s, Auth: a, Origin: "http://localhost:3000"})
	r := httptest.NewRequest("POST", "/api/v1/gift-redemptions", strings.NewReader(`{"giftId":"g1","expectedCostXu":10500}`))
	r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
	r.Header.Set("Origin", "http://localhost:3000")
	r.Header.Set("X-CSRF-Token", u.CSRF)
	r.Header.Set("Idempotency-Key", "gift-price-request")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "GIFT_PRICE_CHANGED") {
		t.Fatal(w.Code, w.Body.String())
	}
	var available, held int64
	var stock, count int
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_gift_held'),(SELECT stock FROM gift_catalog WHERE id='g1'),(SELECT count(*) FROM gift_redemptions WHERE user_id=$1)`, customer).Scan(&available, &held, &stock, &count)
	if err != nil || available != 30000 || held != 0 || stock != 1 || count != 0 {
		t.Fatal(available, held, stock, count, err)
	}
	assertLedger(t, s)
}

func TestGiftInsufficientBalanceRollsBackReservation(t *testing.T) {
	s, customer, _ := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `UPDATE gift_catalog SET stock=1 WHERE id='g1'`); err != nil {
		t.Fatal(err)
	}
	_, err := (&rewards.Service{Store: s}).Redeem(ctx, customer, "gift-poor-request", "g1")
	var problem *platform.Error
	if !errors.As(err, &problem) || problem.Code != "INSUFFICIENT_BALANCE" {
		t.Fatal("unexpected balance error", err)
	}
	var stock, count int
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT stock FROM gift_catalog WHERE id='g1'),(SELECT count(*) FROM gift_redemptions WHERE user_id=$1)`, customer).Scan(&stock, &count)
	if err != nil || stock != 1 || count != 0 {
		t.Fatal(stock, count, err)
	}
	assertLedger(t, s)
}

func TestGiftConcurrentRetriesHoldXuOnlyOnce(t *testing.T) {
	s, customer, _ := testStore(t)
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = wallet.CreditGreen(ctx, tx, customer, "gift-retry-fund", "Test", 30000); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE gift_catalog SET cost=10500,stock=2 WHERE id='g1'`); err != nil {
		t.Fatal(err)
	}
	service := &rewards.Service{Store: s}
	quote := int64(10500)
	var wg sync.WaitGroup
	results := make(chan any, 2)
	errors := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := service.RedeemQuoted(ctx, customer, "gift-retry-request", "g1", &quote)
			results <- v
			errors <- e
		}()
	}
	wg.Wait()
	close(results)
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	var id string
	for value := range results {
		b, _ := json.Marshal(value)
		var row map[string]any
		if err = json.Unmarshal(b, &row); err != nil {
			t.Fatal(err)
		}
		next := row["id"].(string)
		if id != "" && id != next {
			t.Fatal("retry made two requests")
		}
		id = next
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE gift_catalog SET cost=12000 WHERE id='g1'`); err != nil {
		t.Fatal(err)
	}
	replay, err := service.RedeemQuoted(ctx, customer, "gift-retry-request", "g1", &quote)
	if err != nil {
		t.Fatal("price changed after a successful request must not prevent replay", err)
	}
	b, _ := json.Marshal(replay)
	if !strings.Contains(string(b), id) {
		t.Fatal("replay created a different redemption")
	}
	var available, held int64
	var stock, count int
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_gift_held'),(SELECT stock FROM gift_catalog WHERE id='g1'),(SELECT count(*) FROM gift_redemptions WHERE user_id=$1)`, customer).Scan(&available, &held, &stock, &count)
	if err != nil || available != 19500 || held != 10500 || stock != 1 || count != 1 {
		t.Fatal(available, held, stock, count, err)
	}
	assertLedger(t, s)
}

func TestGiftIssuedCodeIsEncryptedAndVisibleOnlyToOwner(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = wallet.CreditGreen(ctx, tx, customer, "gift-code-fund", "Test", 30000); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Pool.Exec(ctx, `UPDATE gift_catalog SET cost=10500,stock=1 WHERE id='g1'`); err != nil {
		t.Fatal(err)
	}
	service := &rewards.Service{Store: s}
	v, err := service.Redeem(ctx, customer, "gift-code-request", "g1")
	if err != nil {
		t.Fatal(err)
	}
	id := v.(map[string]any)["id"].(string)
	if _, err = service.GiftEvent(ctx, admin, id, "gift-code-complete", "completed", "SHOPEE-VOUCHER-SECRET", ""); err != nil {
		t.Fatal(err)
	}
	if _, err = service.GiftEvent(ctx, admin, id, "gift-code-complete", "completed", "SHOPEE-VOUCHER-SECRET", ""); err != nil {
		t.Fatal("completion replay", err)
	}
	var cipher string
	var held int64
	var notifications int
	err = s.Pool.QueryRow(ctx, `SELECT voucher_cipher,(SELECT balance FROM wallet_accounts WHERE user_id=$2 AND kind='green_gift_held'),(SELECT count(*) FROM notifications WHERE recipient_id=$2 AND title='Voucher đã sẵn sàng') FROM gift_redemptions WHERE id=$1`, id, customer).Scan(&cipher, &held, &notifications)
	if err != nil || strings.Contains(cipher, "SHOPEE-VOUCHER-SECRET") || held != 0 || notifications != 1 {
		t.Fatal("issuance state", held, notifications, err)
	}
	a := &auth.Service{Store: s}
	h := New(&Server{Store: s, Auth: a, Origin: "http://localhost:3000"})
	for _, actor := range []string{customer, admin} {
		tx, err := s.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		token, err := a.NewSession(ctx, tx, actor)
		if err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if _, err = tx.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, actor); err != nil {
			tx.Rollback(ctx)
			t.Fatal(err)
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		path := "/api/v1/gift-redemptions"
		if actor == admin {
			path = "/api/v1/admin/gift-redemptions"
		}
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		exposed := strings.Contains(w.Body.String(), "SHOPEE-VOUCHER-SECRET")
		if exposed != (actor == customer) || strings.Contains(w.Body.String(), cipher) {
			t.Fatal("voucher exposure", actor, w.Body.String())
		}
	}
	assertLedger(t, s)
}
