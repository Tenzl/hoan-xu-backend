package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPhysicalLinkDeletionStillRecordsAndCreditsPurchasesAfterFiveDays(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	id, row := savedFixture(t, s, user, time.Now().UTC().Truncate(time.Second).Add(-30*24*time.Hour))
	svc := &affiliate.Service{Store: s}
	if err := svc.DeleteLink(ctx, user, id); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links WHERE id=$1`, id).Scan(&count); err != nil || count != 0 {
		t.Fatal("link must be physically deleted", count, err)
	}
	row.Date = row.Date.Add(20 * 24 * time.Hour)
	row.Status = "approved"
	importRows(t, s, admin, []imports.Row{row, row})
	var orderID string
	if err := s.Pool.QueryRow(ctx, `SELECT id::text FROM orders WHERE tracking_code=$1`, row.Tracking).Scan(&orderID); err != nil {
		t.Fatal("valid CSV after retention was ignored", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := (&orders.Service{Store: s}).Event(ctx, admin, orderID, "after-retention-approval", orders.Event{Action: "approved"}); err != nil {
			t.Fatal(err)
		}
	}
	var balance int64
	if err := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, user).Scan(&balance); err != nil || balance != 6301 {
		t.Fatal(balance, err)
	}
}

func TestSavedLinkHTTPSeparatesRetentionFromReportedOrders(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	active, row := savedFixture(t, s, user, now.Add(-time.Hour))
	expired, _ := savedFixture(t, s, user, now.Add(-6*24*time.Hour))
	importRows(t, s, admin, []imports.Row{row})
	a := &auth.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	token, err := a.NewSession(ctx, tx, user)
	if err != nil {
		tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	h := New(&Server{Store: s, Auth: a, Affiliate: &affiliate.Service{Store: s}, Origin: "http://localhost:3000"})
	get := func(path string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	w := get("/api/v1/affiliate-links")
	var result struct{ Data []map[string]any }
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data) != 1 || result.Data[0]["id"] != active || result.Data[0]["status"] != "active" || result.Data[0]["autoDeleteAt"] == nil {
		t.Fatal("reported link missing or expired link exposed", w.Code, w.Body.String())
	}
	if w = get("/api/v1/affiliate-links/" + expired); w.Code != 404 {
		t.Fatal("expired detail exposed before worker", w.Code, w.Body.String())
	}
	if w = get("/api/v1/orders?status=pending"); w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || len(result.Data) != 1 || result.Data[0]["status"] != "pending" {
		t.Fatal("pending order missing", w.Code, w.Body.String())
	}
	if w = get("/api/v1/orders?status=selecting"); w.Code != 422 {
		t.Fatal("unknown order filter accepted", w.Code)
	}
}

func TestPhysicalDeletionDetachesReferencedOrdersWithoutLosingTracking(t *testing.T) {
	s, user, _ := testStore(t)
	ctx := context.Background()
	id, row := savedFixture(t, s, user, time.Now().UTC().Truncate(time.Second))
	var order string
	if err := s.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,link_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,share_bps,ordered_at) SELECT user_id,id,policy_id,'shopee','fixture','REFERENCED','1','Product',100000,10000,6300,6300,now() FROM affiliate_links WHERE id=$1 RETURNING id::text`, id).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err := (&affiliate.Service{Store: s}).DeleteLink(ctx, user, id); err != nil {
		t.Fatal(err)
	}
	var link *string
	var code string
	var cashback int64
	if err := s.Pool.QueryRow(ctx, `SELECT link_id::text,tracking_code,cashback FROM orders WHERE id=$1`, order).Scan(&link, &code, &cashback); err != nil || link != nil || code != row.Tracking || cashback != 6300 {
		t.Fatal(link, code, cashback, err)
	}
}

func TestFiveDayCleanupDeletesLinksEvenWithPendingOrdersAndRetainsLegacy(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	expired, row := savedFixture(t, s, user, now.Add(-5*24*time.Hour))
	importRows(t, s, admin, []imports.Row{row})
	active, _ := savedFixture(t, s, user, now.Add(-5*24*time.Hour+time.Second))
	legacy, _ := savedFixture(t, s, user, now.Add(-10*24*time.Hour))
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_links SET tracking_sub_ids=NULL,expires_at=NULL,lifecycle_status=NULL WHERE id=$1`, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err := (&affiliate.Service{Store: s}).PurgeExpired(ctx, now); err != nil {
		t.Fatal(err)
	}
	var removed, kept, orderCount int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FILTER(WHERE id=$1),count(*) FILTER(WHERE id IN ($2,$3)),(SELECT count(*) FROM orders) FROM affiliate_links`, expired, active, legacy).Scan(&removed, &kept, &orderCount); err != nil || removed != 0 || kept != 2 || orderCount != 1 {
		t.Fatal(removed, kept, orderCount, err)
	}
}
