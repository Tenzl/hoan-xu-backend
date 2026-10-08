package api

import (
	"context"
	"encoding/json"
	"errors"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/platform"
	"sync/atomic"
	"testing"
	"time"
)

type blockingOffer struct {
	entered, release chan struct{}
	calls            atomic.Int32
}

func (f *blockingOffer) CreateOfferLink(ctx context.Context, _, _ string, _ [5]string) (string, error) {
	f.calls.Add(1)
	close(f.entered)
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-f.release:
		return "https://s.shopee.vn/test", nil
	}
}
func TestLinkOperationSurvivesDisconnectReplaysAndRejectsConflict(t *testing.T) {
	s, user, _ := testStore(t)
	configureLinkPolicy(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"123456789"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	fixture := &blockingOffer{entered: make(chan struct{}), release: make(chan struct{})}
	aff := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, ProductLookup: verifiedLinkProduct, LinkGenerator: fixture}
	request, cancel := context.WithCancel(ctx)
	result := make(chan error, 1)
	go func() {
		_, err := aff.CreateLinkOperation(request, user, "operation-test", "https://shopee.vn/product/1/2")
		result <- err
	}()
	select {
	case <-fixture.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("generator not entered")
	}
	cancel()
	_, err := aff.CreateLinkOperation(ctx, user, "operation-test", "https://shopee.vn/product/1/2")
	var p *platform.Error
	if !errors.As(err, &p) || p.Code != "LINK_CREATION_IN_PROGRESS" {
		t.Fatal(err)
	}
	close(fixture.release)
	if err = <-result; err != nil {
		t.Fatal(err)
	}
	// Cached responses from the former deletion policy must use the current permission.
	if _, err = s.Pool.Exec(ctx, `UPDATE link_operations SET response=response||'{"canDelete":false}'::jsonb WHERE user_id=$1 AND key='operation-test'`, user); err != nil {
		t.Fatal(err)
	}
	v, err := aff.CreateLinkOperation(ctx, user, "operation-test", "https://shopee.vn/product/1/2")
	if err != nil {
		t.Fatal(err)
	}
	var link map[string]any
	if err = json.Unmarshal(v.(json.RawMessage), &link); err != nil {
		t.Fatal(err)
	}
	if fixture.calls.Load() != 1 || link["payoutFactor"] == nil || link["canDelete"] != true {
		t.Fatal("re-sampled operation", fixture.calls.Load(), link)
	}
	if _, err = aff.CreateLinkOperation(ctx, user, "operation-test", "https://shopee.vn/product/1/3"); err == nil {
		t.Fatal("accepted changed payload")
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err = aff.DeleteLink(ctx, user, link["id"].(string)); err != nil {
		t.Fatal(err)
	}
	_, err = aff.CreateLinkOperation(ctx, user, "operation-test", "https://shopee.vn/product/1/2")
	if !errors.As(err, &p) || p.Status != 410 || p.Code != "LINK_DELETED" {
		t.Fatal("deleted operation replayed a hidden link", err)
	}
}
func TestFailedLinkOperationDoesNotSaveSnapshotOrRetryGenerator(t *testing.T) {
	s, user, _ := testStore(t)
	configureLinkPolicy(t, s)
	ctx := context.Background()
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"123456789"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	fixture := &offerLinkFixture{err: errors.New("SHOPEE_UPSTREAM_FAILED")}
	aff := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, ProductLookup: verifiedLinkProduct, LinkGenerator: fixture}
	if _, err := aff.CreateLinkOperation(ctx, user, "operation-fail", "https://shopee.vn/product/1/2"); err == nil {
		t.Fatal("accepted failure")
	}
	ids := fixture.ids
	fixture.err = nil
	if _, err := aff.CreateLinkOperation(ctx, user, "operation-fail", "https://shopee.vn/product/1/2"); err == nil {
		t.Fatal("failed key reused")
	}
	if fixture.ids != ids {
		t.Fatal("resampled failed request")
	}
	var count int
	if err := s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
