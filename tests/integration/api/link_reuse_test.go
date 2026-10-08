package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type countedOffer struct{ calls atomic.Int32 }

func (f *countedOffer) CreateOfferLink(context.Context, string, string, [5]string) (string, error) {
	f.calls.Add(1)
	return "https://s.shopee.vn/reuse", nil
}

func TestSameProductReusesValidLinkAcrossConcurrentServices(t *testing.T) {
	s, user, _ := testStore(t)
	ctx := context.Background()
	configureLinkPolicy(t, s)
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"123456789"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	f := &countedOffer{}
	var wg sync.WaitGroup
	results := make(chan map[string]any, 4)
	errors := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			svc := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, ProductLookup: verifiedLinkProduct, LinkGenerator: f}
			v, err := svc.CreateLink(ctx, user, "https://shopee.vn/product/1/2?variant=3")
			if err != nil {
				errors <- err
				return
			}
			results <- v.(map[string]any)
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Fatal(err)
	}
	close(results)
	var id string
	reused := 0
	for v := range results {
		if id == "" {
			id = v["id"].(string)
		}
		if v["id"] != id {
			t.Fatal("duplicate product generated separate links", v)
		}
		if v["reused"] == true {
			reused++
		}
	}
	if f.calls.Load() != 1 || reused != 3 {
		t.Fatal("expected one generation and three reuses", f.calls.Load(), reused)
	}
	svc := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, ProductLookup: verifiedLinkProduct, LinkGenerator: f}
	if err := svc.DeleteLink(ctx, user, id); err != nil {
		t.Fatal(err)
	}
	v, err := svc.CreateLink(ctx, user, "https://shopee.vn/product/1/2")
	if err != nil || v.(map[string]any)["id"] == id || f.calls.Load() != 2 {
		t.Fatal("deleted link prevented a new generation", v, err)
	}
	created := v.(map[string]any)["createdAt"].(time.Time)
	if !v.(map[string]any)["expiresAt"].(time.Time).Equal(created.Add(5 * 24 * time.Hour)) {
		t.Fatal("new link must last five days", v)
	}
	// An expired visible link must not prevent another generation.
	id = v.(map[string]any)["id"].(string)
	if _, err = s.Pool.Exec(ctx, `UPDATE affiliate_links SET created_at=created_at-interval '6 days',expires_at=expires_at-interval '6 days' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	v, err = svc.CreateLink(ctx, user, "https://shopee.vn/product/1/2")
	if err != nil || v.(map[string]any)["id"] == id || f.calls.Load() != 3 {
		t.Fatal("expired link prevented generation", v, err)
	}
}

func TestDeletingLinkPreservesPendingCashbackAndFutureTimelyReports(t *testing.T) {
	s, user, admin := testStore(t)
	ctx := context.Background()
	id, row := savedFixture(t, s, user, time.Now().UTC().Truncate(time.Second).Add(-24*time.Hour))
	importRows(t, s, admin, []imports.Row{row})
	svc := &affiliate.Service{Store: s}
	if err := svc.DeleteLink(ctx, user, id); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := s.Pool.QueryRow(ctx, purchasesSQL, user, 100, 0, "progress").Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(raw, &entry); err != nil || entry["link"] != nil || entry["order"] == nil {
		t.Fatal("deleted link hid its pending order", string(raw), err)
	}
	row.Status = "approved"
	importRows(t, s, admin, []imports.Row{row})
	var order string
	if err := s.Pool.QueryRow(ctx, `SELECT id::text FROM orders WHERE tracking_code=$1`, row.Tracking).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if _, err := (&orders.Service{Store: s}).Event(ctx, admin, order, "approve-deleted-link", orders.Event{Action: "approved"}); err != nil {
		t.Fatal(err)
	}
	future := row
	future.OrderID = "AFTER-DELETE"
	future.Date = row.Date.Add(2 * 24 * time.Hour)
	future.Status = "pending"
	future.LineID, _ = imports.SourceLineID(future)
	late := future
	late.OrderID = "AFTER-EXPIRY"
	late.Date = row.Date.Add(7 * 24 * time.Hour)
	late.LineID, _ = imports.SourceLineID(late)
	importRows(t, s, admin, []imports.Row{future, future, late})
	var total, pending, approved, retained int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE status='pending'),count(*) FILTER(WHERE status='approved') FROM orders WHERE user_id=$1`, user).Scan(&total, &pending, &approved); err != nil || total != 3 || pending != 2 || approved != 1 {
		t.Fatal(total, pending, approved, err)
	}
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links WHERE id=$1 AND deleted_at IS NOT NULL AND tracking_sub_ids IS NOT NULL`, id).Scan(&retained); err != nil || retained != 0 {
		t.Fatal("saved link was not physically deleted", retained, err)
	}
	var balance int64
	if err := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, user).Scan(&balance); err != nil || balance != 6301 {
		t.Fatal("deleted link changed credited cashback", balance, err)
	}
}
