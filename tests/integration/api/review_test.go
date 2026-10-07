package api

import (
	"context"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"strings"
	"testing"
	"time"
)

func TestImportPreservesInternalRejectionAndRequiresExplicitReopen(t *testing.T) {
	s, uid, admin := testStore(t)
	ctx := context.Background()
	_, err := s.Pool.Exec(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id) SELECT $1,'shopee','https://shopee.vn/product/1/2','https://s.shopee.vn/test','reviewtracking',id FROM cashback_policies WHERE mode='fixed'`, uid)
	if err != nil {
		t.Fatal(err)
	}
	var id string
	err = s.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,link_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,ordered_at,source_status) SELECT $1,l.id,l.policy_id,'shopee','publisher','review','line','Test',100000,5000,2500,now(),'approved' FROM affiliate_links l WHERE tracking_code='reviewtracking' RETURNING orders.id::text`, uid).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	osvc := &orders.Service{Store: s}
	if _, err = osvc.Event(ctx, admin, id, "reject-review", orders.Event{Action: "rejected", Reason: "Internal review decision"}); err != nil {
		t.Fatal(err)
	}
	csv := "channel,publisher,order_id,line_id,tracking_code,date,product_name,value,commission,status\nshopee,publisher,review,line,reviewtracking,2026-10-05,Test,100000,6000,approved\n"
	rows, err := imports.Parse(strings.NewReader(csv), nil)
	if err != nil {
		t.Fatal(err)
	}
	svc := &imports.Service{Store: s}
	batch, err := svc.Preview(ctx, admin, "review.csv", platform.Hash(csv), nil, rows)
	if err != nil {
		t.Fatal(err)
	}
	bid := batch.(map[string]any)["id"].(string)
	if _, err = svc.Commit(ctx, admin, "commit-review", bid); err != nil {
		t.Fatal(err)
	}
	// The worker invokes applyOne; test its transactional result without a timed polling loop.
	svcCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go svc.Run(svcCtx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var state string
		if err = s.Pool.QueryRow(ctx, `SELECT status FROM import_batches WHERE id=$1`, bid).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state == "completed" {
			break
		}
		if state == "failed" || time.Now().After(deadline) {
			t.Fatal("batch", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	var status string
	if err = s.Pool.QueryRow(ctx, `SELECT status FROM orders WHERE id=$1`, id).Scan(&status); err != nil || status != "rejected" {
		t.Fatalf("internal rejection overwritten: %s %v", status, err)
	}
	if _, err = osvc.Event(ctx, admin, id, "reopen-no-reason", orders.Event{Action: "reopened"}); err == nil {
		t.Fatal("reopened without reason")
	}
	if _, err = osvc.Event(ctx, admin, id, "reopen-review", orders.Event{Action: "reopened", Reason: "Reviewed the source again"}); err != nil {
		t.Fatal(err)
	}
	var balance int64
	if err = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, uid).Scan(&balance); err != nil || balance != 0 {
		t.Fatal("reopening credited wallet", balance, err)
	}
}
