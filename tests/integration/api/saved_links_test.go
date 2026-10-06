package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"hoanxu/internal/tracking"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func savedFixture(t *testing.T, s *platform.Store, user string, created time.Time) (string, imports.Row) {
	t.Helper()
	ctx := context.Background()
	configureLinkPolicy(t, s)
	if _, e := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET settings='{"publisher":"123456789"}' WHERE id='shopee'`); e != nil {
		t.Fatal(e)
	}
	var customer, policy string
	var version uint32
	if e := s.Pool.QueryRow(ctx, `SELECT tracking_code FROM users WHERE id=$1`, user).Scan(&customer); e != nil {
		t.Fatal(e)
	}
	if e := s.Pool.QueryRow(ctx, `SELECT id::text,tracking_version FROM cashback_policies WHERE mode='tiered' ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&policy, &version); e != nil {
		t.Fatal(e)
	}
	claims := tracking.Claims{CreatedAt: created, Shop: 83496725, Item: 6939920023, Policy: version, Tier: "bronze", Bps: 6600}
	ids, e := tracking.Issue(claims, customer, "123456789", "0.63", s.SignTracking)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := json.Marshal(ids)
	var id string
	e = s.Pool.QueryRow(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id,item_id,created_at,tier_code,min_share_bps,max_share_bps,tracking_sub_ids,expires_at,payout_factor,effective_share_bps,lifecycle_status) VALUES($1,'shopee','https://shopee.vn/product/83496725/6939920023','https://s.shopee.vn/Test',$2,$3,'6939920023',$4,'bronze',6500,7500,$5,$6,0.63,6300,'active') RETURNING id::text`, user, ids[2], policy, created, raw, claims.ExpiresAt()).Scan(&id)
	if e != nil {
		t.Fatal(e)
	}
	row := imports.Row{NativeShopee: true, Channel: "shopee", Publisher: "123456789", OrderID: "ORDER1", ConversionID: "123", ShopID: "83496725", ItemID: "6939920023", ModelID: "456", PromotionID: "0", Tracking: ids[2], SubIDs: ids, Name: "Product", Value: 100000, Commission: 10001, Status: "pending", Date: created.Add(time.Hour)}
	row.LineID, e = imports.SourceLineID(row)
	if e != nil {
		t.Fatal(e)
	}
	return id, row
}

func linkState(t *testing.T, s *platform.Store, id string) map[string]any {
	t.Helper()
	var raw []byte
	if e := s.Pool.QueryRow(context.Background(), affiliate.LinksSQL+` WHERE l.id=$1`, id).Scan(&raw); e != nil {
		t.Fatal(e)
	}
	var got map[string]any
	if e := json.Unmarshal(raw, &got); e != nil {
		t.Fatal(e)
	}
	return got
}

func TestSavedLinksCancelOnceLateReportRevivesAndLocksDeletion(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id, row := savedFixture(t, s, user, now.Add(-7*24*time.Hour))
	svc := &affiliate.Service{Store: s}
	before := linkState(t, s, id)
	if before["status"] != "cancelled" || before["canDelete"] != true {
		t.Fatal(before)
	}
	for i := 0; i < 2; i++ {
		if e := svc.CancelExpired(ctx, now); e != nil {
			t.Fatal(e)
		}
	}
	var n int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1`, user).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	importRows(t, s, admin, []imports.Row{row})
	got := linkState(t, s, id)
	if got["status"] != "progress" || got["canDelete"] != false {
		t.Fatal(got)
	}
	if e := svc.DeleteLink(ctx, user, id); e == nil {
		t.Fatal("deleted progress link")
	}
	row.Status = "approved"
	importRows(t, s, admin, []imports.Row{row})
	var order string
	if e := s.Pool.QueryRow(ctx, `SELECT id::text FROM orders WHERE tracking_code=$1`, row.Tracking).Scan(&order); e != nil {
		t.Fatal(e)
	}
	if _, e := (&orders.Service{Store: s}).Event(ctx, admin, order, "approve-saved", orders.Event{Action: "approved"}); e != nil {
		t.Fatal(e)
	}
	got = linkState(t, s, id)
	if got["status"] != "completed" || got["canDelete"] != false {
		t.Fatal(got)
	}
	if e := svc.DeleteLink(ctx, user, id); e == nil {
		t.Fatal("deleted completed link")
	}
	if e := svc.CancelExpired(ctx, now.Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1`, user).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestDeletedLinkStillAcceptsTimelyLateOrderAndRejectsOutOfWindow(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	id, row := savedFixture(t, s, user, now.Add(-30*24*time.Hour))
	svc := &affiliate.Service{Store: s}
	if e := svc.DeleteLink(ctx, user, id); e != nil {
		t.Fatal(e)
	}
	bad := row
	bad.OrderID = "ORDER2"
	bad.LineID, _ = imports.SourceLineID(bad)
	bad.Date = row.Date.Add(6 * 24 * time.Hour)
	importRows(t, s, admin, []imports.Row{row, row, bad})
	var links, count int
	var cash int64
	var link *string
	if e := s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM affiliate_links),(SELECT count(*) FROM orders)`).Scan(&links, &count); e != nil || links != 0 || count != 1 {
		t.Fatal(links, count, e)
	}
	if e := s.Pool.QueryRow(ctx, `SELECT cashback,link_id::text FROM orders`).Scan(&cash, &link); e != nil || cash != 6301 || link != nil {
		t.Fatal(cash, link, e)
	}
}

func TestDeleteLinkHTTPChecksOwnershipCSRFAndLegacy(t *testing.T) {
	s, user, _ := testStore(t)
	ctx := context.Background()
	id, _ := savedFixture(t, s, user, time.Now().UTC().Truncate(time.Second))
	var other string
	if e := s.Pool.QueryRow(ctx, `INSERT INTO users(name,role) VALUES('Other','customer') RETURNING id::text`).Scan(&other); e != nil {
		t.Fatal(e)
	}
	a := &auth.Service{Store: s}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	token, e := a.NewSession(ctx, tx, user)
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
	handler := New(&Server{Store: s, Auth: a, Affiliate: &affiliate.Service{Store: s}, Origin: "http://localhost:3000"})
	request := func(id, csrf string) int {
		r := httptest.NewRequest("DELETE", "/api/v1/affiliate-links/"+id, nil)
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrf)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Code
	}
	if got := request(id, ""); got != 403 {
		t.Fatal("csrf", got)
	}
	otherID, _ := savedFixture(t, s, other, time.Now().UTC().Truncate(time.Second))
	if got := request(otherID, u.CSRF); got != 404 {
		t.Fatal("ownership", got)
	}
	if got := request(id, u.CSRF); got != 204 {
		t.Fatal("delete", got)
	}
	if got := request(id, u.CSRF); got != 404 {
		t.Fatal("repeat", got)
	}
	legacy, _ := savedFixture(t, s, user, time.Now().UTC().Truncate(time.Second))
	if _, e = s.Pool.Exec(ctx, `UPDATE affiliate_links SET tracking_sub_ids=NULL,expires_at=NULL,lifecycle_status=NULL WHERE id=$1`, legacy); e != nil {
		t.Fatal(e)
	}
	if got := request(legacy, u.CSRF); got != 409 {
		t.Fatal("legacy", got)
	}
}

func TestDeletionWaitsForConcurrentImportThenRefuses(t *testing.T) {
	s, user, _ := testStore(t)
	ctx := context.Background()
	id, row := savedFixture(t, s, user, time.Now().UTC().Truncate(time.Second))
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = platform.LockTracking(ctx, tx, user, row.Tracking); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() { result <- (&affiliate.Service{Store: s}).DeleteLink(ctx, user, id) }()
	select {
	case err := <-result:
		t.Fatal("deletion ignored import lock", err)
	case <-time.After(100 * time.Millisecond):
	}
	if _, _, e = imports.InsertSignedOrder(ctx, tx, s, &row); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("deleted after import")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("delete blocked")
	}
}
