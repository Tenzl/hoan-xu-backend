package api

import (
	"context"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"hoanxu/internal/tracking"
	"testing"
	"time"
)

func TestHistoricalVersionsRetainMetadataWithoutBlockingAfterRetention(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	created := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Second)
	_, row := savedFixture(t, s, user, created)
	claims, e := tracking.Verify(row.SubIDs, row.Publisher, s.SignTracking)
	if e != nil {
		t.Fatal(e)
	}
	newer := row
	claims.Version = 2
	newer.SubIDs, e = tracking.Issue(claims, row.SubIDs[0], row.Publisher, "0.63", s.SignTracking)
	if e != nil {
		t.Fatal(e)
	}
	newer.Tracking = newer.SubIDs[2]
	newer.OrderID = "NEWV2"
	newer.LineID, _ = imports.SourceLineID(newer)
	newer.Date = created.Add(6 * 24 * time.Hour)
	claims.Version = 1
	ids, e := tracking.Issue(claims, row.SubIDs[0], row.Publisher, "0.63", s.SignTracking)
	if e != nil {
		t.Fatal(e)
	}
	row.SubIDs = ids
	row.Tracking = ids[2]
	row.Date = created.Add(7*24*time.Hour - time.Second)
	importRows(t, s, admin, []imports.Row{row, newer})
	var id string
	var n int
	var expiry time.Time
	if e = s.Pool.QueryRow(ctx, `SELECT id::text,link_expires_at FROM orders WHERE tracking_code=$1`, row.Tracking).Scan(&id, &expiry); e != nil || !expiry.Equal(created.Add(7*24*time.Hour)) {
		t.Fatal(expiry, e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders`).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	row.Status = "approved"
	importRows(t, s, admin, []imports.Row{row})
	if _, e = (&orders.Service{Store: s}).Event(ctx, admin, id, "legacy-signed-approve", orders.Event{Action: "approved"}); e != nil {
		t.Fatal(e)
	}
}

func TestRejectedOnlyLinksRemainWithoutRemovingOrdersOrNotifyingProgress(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	created := time.Now().UTC().Add(-7 * 24 * time.Hour).Truncate(time.Second)
	id, row := savedFixture(t, s, user, created)
	svc := &affiliate.Service{Store: s}
	importRows(t, s, admin, []imports.Row{row})
	if _, e := svc.PurgeExpired(ctx, time.Now()); e != nil {
		t.Fatal(e)
	}
	var n int
	if e := s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE recipient_id=$1`, user).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	row.Status = "rejected"
	importRows(t, s, admin, []imports.Row{row})
	var links int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links WHERE id=$1`, id).Scan(&links); err != nil || links != 0 {
		t.Fatal(links, err)
	}
	var status string
	var cashback int64
	if e := s.Pool.QueryRow(ctx, `SELECT status,cashback FROM orders`).Scan(&status, &cashback); e != nil || status != "rejected" || cashback != 0 {
		t.Fatal(status, cashback, e)
	}
}

func TestGenerationDoesNotReturnSuccessIfSavingLinkFails(t *testing.T) {
	s, user, _ := testStore(t)
	ctx := context.Background()
	configureLinkPolicy(t, s)
	if _, e := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET settings='{"publisher":"123456789"}',status='available' WHERE id='shopee'; CREATE FUNCTION reject_new_link() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'test insert failure'; END $$; CREATE TRIGGER reject_new_link BEFORE INSERT ON affiliate_links FOR EACH ROW EXECUTE FUNCTION reject_new_link();`); e != nil {
		t.Fatal(e)
	}
	svc := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, LinkGenerator: &offerLinkFixture{}}
	result, e := svc.CreateLink(ctx, user, "https://shopee.vn/product/83496725/6939920023")
	if e == nil || result != nil {
		t.Fatal(result, e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links`).Scan(&n); e != nil || n != 0 {
		t.Fatal(n, e)
	}
}

func TestMixedOrderStatusesAllowHidingLinkWithoutRemovingOrders(t *testing.T) {
	s, user, admin := testStore(t)
	id, row := savedFixture(t, s, user, time.Now().UTC().Add(-24*time.Hour).Truncate(time.Second))
	second := row
	second.OrderID = "ORDER2"
	second.LineID, _ = imports.SourceLineID(second)
	second.Status = "rejected"
	importRows(t, s, admin, []imports.Row{row, second})
	got := linkState(t, s, id)
	if got["status"] != "active" || got["canDelete"] != true {
		t.Fatal(got)
	}
	if e := (&affiliate.Service{Store: s}).DeleteLink(context.Background(), user, id); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM orders WHERE tracking_code=$1`, row.Tracking).Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
}
