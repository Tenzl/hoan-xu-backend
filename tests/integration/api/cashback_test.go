package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/cashback"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"hoanxu/internal/settings"
	"hoanxu/internal/wallet"
)

func TestTierMigrationPreservesLegacyMoney(t *testing.T) {
	s, customer, _ := testStoreWithTiers(t, false)
	ctx := context.Background()
	var link, order string
	if e := s.Pool.QueryRow(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id) SELECT $1,'shopee','https://shopee.vn/product/1/2','https://s.shopee.vn/test','legacy',id FROM cashback_policies RETURNING id::text`, customer).Scan(&link); e != nil {
		t.Fatal(e)
	}
	if e := s.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,link_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,ordered_at,status,source_status,approved_at) SELECT $1,$2,id,'shopee','test','legacy','line','Legacy',100000,10000,5000,now(),'approved','approved',now() FROM cashback_policies RETURNING id::text`, customer, link).Scan(&order); e != nil {
		t.Fatal(e)
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, customer, "order_credit:"+order, "Legacy", 5000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile(filepath.Join("../../database/migrations", "000008_cashback_tiers.up.sql"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, string(raw)); e != nil {
		t.Fatal(e)
	}
	var cash, balance int64
	var bps, minBps, maxBps int
	var tier *string
	if e = s.Pool.QueryRow(ctx, `SELECT o.cashback,o.share_bps,l.min_share_bps,l.max_share_bps,l.tier_code,a.balance FROM orders o JOIN affiliate_links l ON l.id=o.link_id JOIN wallet_accounts a ON a.user_id=o.user_id AND a.kind='available' WHERE o.id=$1`, order).Scan(&cash, &bps, &minBps, &maxBps, &tier, &balance); e != nil {
		t.Fatal(e)
	}
	if cash != 5000 || balance != 5000 || bps != 5000 || minBps != 5000 || maxBps != 5000 || tier != nil {
		t.Fatal(cash, balance, bps, minBps, maxBps, tier)
	}
	var tierCount int
 if e=s.Pool.QueryRow(ctx,`SELECT count(*) FROM cashback_tiers`).Scan(&tierCount);e!=nil||tierCount!=3{t.Fatal(tierCount,e)}
}

func TestLegacyOrderSnapshotSurvivesPolicyChangesAndRejectsAdjustment(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	policies := &cashback.Service{Store: s}
	p, e := policies.Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	initial := periodFixtureInput(p.ID, []cashback.Tier{{Code: "bronze", MinGold: 0, Min: 2200, Max: 2700}, {Code: "platinum", MinGold: 2, Min: 6000, Max: 7000}, {Code: "diamond", MinGold: 4, Min: 8000, Max: 9000}}, 0)
	if _, e = policies.Create(ctx, admin, "policy-initial", initial); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"template":"https://s.shopee.vn/an_redir"}' WHERE id='shopee'`); e != nil {
		t.Fatal(e)
	}
	var linkID, policyID string
	if e = s.Pool.QueryRow(ctx, `SELECT id::text FROM cashback_policies WHERE mode='tiered' ORDER BY created_at DESC LIMIT 1`).Scan(&policyID); e != nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id,tier_code,min_share_bps,max_share_bps) VALUES($1,'shopee','https://shopee.vn/product/1/2','https://s.shopee.vn/legacy','legacy-rate-link',$2,'bronze',2222,2224) RETURNING id::text`, customer, policyID).Scan(&linkID); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `INSERT INTO orders(user_id,link_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,ordered_at,source_status,tier_code,share_bps) VALUES($1,$2,$3,'shopee','fixture','random-one','line','Test',100000,10001,2223,now(),'approved','bronze',2223)`, customer, linkID, policyID); e != nil {
		t.Fatal(e)
	}
	l := map[string]any{"trackingCode": "legacy-rate-link"}
	// Pending and rejected rows cannot raise the customer's tier.
	if _, e = s.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,ordered_at,status) SELECT $1,p.id,'shopee','rank',n::text,'line','Rank',0,0,0,now(),CASE WHEN n%2=0 THEN 'pending' ELSE 'rejected' END FROM cashback_policies p CROSS JOIN generate_series(1,8)n WHERE p.mode='fixed'`, customer); e != nil {
		t.Fatal(e)
	}
	m, e := cashback.MembershipFor(ctx, s.Queries, customer)
	if e != nil || m.Code != "member" || m.ApprovedOrders != 0 {
		t.Fatal(m, e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE orders SET status='approved',source_status='approved',cashback=1,approved_at=now() WHERE publisher='rank' AND external_id IN ('1','2')`); e != nil {
		t.Fatal(e)
	}
	m, e = cashback.MembershipFor(ctx, s.Queries, customer)
	if e != nil || m.Code != "gold" {
		t.Fatal(m, e)
	}
	p, e = policies.Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	changed := periodFixtureInput(p.ID, []cashback.Tier{{Code: "bronze", MinGold: 0, Min: 8000, Max: 9000}, {Code: "platinum", MinGold: 2, Min: 8000, Max: 9000}, {Code: "diamond", MinGold: 4, Min: 8000, Max: 9000}}, 0)
	if _, e = policies.Create(ctx, admin, "policy-changed", changed); e != nil {
		t.Fatal(e)
	}
	svc := &imports.Service{Store: s}
	prepare := func(key string, commission int64, tracking string) string {
		t.Helper()
		row := imports.Row{Channel: "shopee", Publisher: "fixture", OrderID: "random-one", LineID: "line", Tracking: tracking, Date: time.Now().UTC(), Name: "Test", Value: 100000, Commission: commission, Status: "approved"}
		b, e := svc.Preview(ctx, admin, key+".csv", platform.Hash(key), nil, []imports.Row{row})
		if e != nil {
			t.Fatal(e)
		}
		id := b.(map[string]any)["id"].(string)
		if _, e = svc.Commit(ctx, admin, "commit-"+key, id); e != nil {
			t.Fatal(e)
		}
		return id
	}
	batches := []string{}
	for i := 0; i < 8; i++ {
		batches = append(batches, prepare(fmt.Sprintf("duplicate-%d", i), 10001, l["trackingCode"].(string)))
	}
	batches = append(batches, prepare("unmatched", 10001, "unknown"))
	workerCtx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); svc.Run(workerCtx) }()
	}
	defer func() { cancel(); wg.Wait() }()
	wait := func(ids []string) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			done := true
			for _, id := range ids {
				var st string
				if e = s.Pool.QueryRow(ctx, `SELECT status FROM import_batches WHERE id=$1`, id).Scan(&st); e != nil {
					t.Fatal(e)
				}
				if st == "failed" {
					t.Fatal("batch failed")
				}
				done = done && st == "completed"
			}
			if done {
				return
			}
			if time.Now().After(deadline) {
				t.Fatal("worker timeout")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	wait(batches)
	var id string
	var bps, count int
	var cash int64
	var tier string
	if e = s.Pool.QueryRow(ctx, `SELECT id::text,share_bps,cashback,tier_code FROM orders WHERE publisher='fixture'`).Scan(&id, &bps, &cash, &tier); e != nil {
		t.Fatal(e)
	}
	expected, _ := cashback.Amount(10001, bps)
	if bps < 2222 || bps > 2224 || cash != expected || tier != "bronze" {
		t.Fatal(bps, cash, tier)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE publisher='fixture'`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	// A changed pending commission and a restarted worker retain the same draw.
	cancel()
	wg.Wait()
	workerCtx, cancel = context.WithCancel(ctx)
	defer cancel()
	wg.Add(1)
	go func() { defer wg.Done(); (&imports.Service{Store: s}).Run(workerCtx) }()
	changedBatch := prepare("pending-change", 20001, l["trackingCode"].(string))
	wait([]string{changedBatch})
	var afterBps int
	if e = s.Pool.QueryRow(ctx, `SELECT share_bps,cashback FROM orders WHERE id=$1`, id).Scan(&afterBps, &cash); e != nil {
		t.Fatal(e)
	}
	expected, _ = cashback.Amount(20001, bps)
	if afterBps != bps || cash != expected {
		t.Fatal(afterBps, cash)
	}
	events := &orders.Service{Store: s}
	var approves sync.WaitGroup
	errors := make(chan error, 6)
	for i := 0; i < 6; i++ {
		approves.Add(1)
		go func() {
			defer approves.Done()
			_, e := events.Event(ctx, admin, id, "approve-random", orders.Event{Action: "approved"})
			errors <- e
		}()
	}
	approves.Wait()
	close(errors)
	for e := range errors {
		if e != nil {
			t.Fatal(e)
		}
	}
	if _, e = events.Event(ctx, admin, id, "adjust-random", orders.Event{Action: "adjustment", Reason: "Corrected commission"}); e == nil {
		t.Fatal(e)
	}
	var balance int64
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, customer).Scan(&balance); e != nil {
		t.Fatal(e)
	}
	expected, _ = cashback.Amount(20001, bps)
	if balance != expected {
		t.Fatal(balance, expected)
	}
	if _, e = events.Event(ctx, admin, id, "adjust-random-down", orders.Event{Action: "adjustment", Reason: "Reduced commission"}); e == nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT a.balance,o.share_bps FROM wallet_accounts a JOIN orders o ON o.user_id=a.user_id WHERE a.user_id=$1 AND a.kind='available' AND o.id=$2`, customer, id).Scan(&balance, &afterBps); e != nil {
		t.Fatal(e)
	}
	expected, _ = cashback.Amount(20001, bps)
	if balance != expected || afterBps != bps {
		t.Fatal(balance, afterBps)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM wallet_transactions WHERE reference=$1`, "order_credit:"+id).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT transaction_id FROM wallet_entries GROUP BY transaction_id HAVING sum(amount)<>0)t`).Scan(&count); e != nil || count != 0 {
		t.Fatal("unbalanced", count, e)
	}
}

func TestConcurrentPolicyEditsOnlyCommitOneVersion(t *testing.T) {
	s, _, admin := testStore(t)
	ctx := context.Background()
	svc := &cashback.Service{Store: s}
	p, e := svc.Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	input := periodFixtureInput(p.ID, []cashback.Tier{{Code: "bronze", Min: 1000, Max: 2000}, {Code: "platinum", MinGold: 30, Min: 2000, Max: 3000}, {Code: "diamond", MinGold: 100, Min: 3000, Max: 4000}}, 0)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, e := svc.Create(ctx, admin, fmt.Sprintf("parallel-policy-%d", i), input)
			results <- e
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for e := range results {
		if e == nil {
			success++
		} else if failure, ok := e.(*platform.Error); ok && failure.Code == "POLICY_VERSION_CONFLICT" {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='cashback_policy_created'`).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestCashbackPolicyAPIRequiresPermissionReauthAndPreservesOtherSettings(t *testing.T) {
	s, customer, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: s}
	if _, e := s.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin); e != nil {
		t.Fatal(e)
	}
	session := func(id string) (string, *auth.User) {
		t.Helper()
		tx, e := s.Pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		token, e := a.NewSession(ctx, tx, id)
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
		return token, u
	}
	customerToken, cu := session(customer)
	adminToken, au := session(admin)
	handler := New(&Server{Store: s, Auth: a, Affiliate: &affiliate.Service{Store: s}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	request := func(method, path, body, token, csrf, key string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if token != "" {
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", csrf)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	p, e := (&cashback.Service{Store: s}).Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	input := periodFixtureInput(p.ID, []cashback.Tier{{Code: "bronze", Min: 5000, Max: 5500}, {Code: "platinum", MinGold: 30, Min: 6000, Max: 7000}, {Code: "diamond", MinGold: 100, Min: 8000, Max: 9000}}, 0)
	raw, _ := json.Marshal(input)
	for _, tc := range []struct {
		token, csrf string
		status      int
	}{{"", "", 401}, {customerToken, cu.CSRF, 403}, {adminToken, "", 403}, {adminToken, au.CSRF, 403}} {
		w := request("POST", "/admin/cashback-policies", string(raw), tc.token, tc.csrf, "policy-api-key")
		if w.Code != tc.status {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, au.SessionID); e != nil {
		t.Fatal(e)
	}
	w := request("POST", "/admin/cashback-policies", string(raw), adminToken, au.CSRF, "policy-api-key")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body.String())
	}
	retry := request("POST", "/admin/cashback-policies", string(raw), adminToken, au.CSRF, "policy-api-key")
	var one, two struct{ Data any }
	_ = json.Unmarshal(w.Body.Bytes(), &one)
	_ = json.Unmarshal(retry.Body.Bytes(), &two)
	if retry.Code != 201 || !reflect.DeepEqual(one.Data, two.Data) {
		t.Fatal("non-idempotent policy")
	}
	if stale := request("POST", "/admin/cashback-policies", string(raw), adminToken, au.CSRF, "policy-stale-key"); stale.Code != 409 {
		t.Fatal(stale.Code, stale.Body.String())
	}
	bad := strings.Replace(string(raw), "50.00", "50.001", 1)
	if invalid := request("POST", "/admin/cashback-policies", bad, adminToken, au.CSRF, "policy-bad-key"); invalid.Code != 422 {
		t.Fatal(invalid.Code, invalid.Body.String())
	}
	var before, after int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM cashback_policies`).Scan(&before); e != nil {
		t.Fatal(e)
	}
	if e = (&settings.Service{Store: s}).Update(ctx, admin, settings.Input{Brand: "Hoàn Xu", FAQ: []settings.FAQ{{Question: "Policy?", Answer: "Snapshot."}}}); e != nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM cashback_policies`).Scan(&after); e != nil || before != after {
		t.Fatal("settings changed policy", before, after, e)
	}
	staff, e := auth.CreateInternal(ctx, s, admin, "staff", "Staff", "staff-test-password", "staff", nil)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, staff); e != nil {
		t.Fatal(e)
	}
	staffToken, _ := session(staff)
	if denied := request("GET", "/admin/cashback-policies/current", "", staffToken, "", ""); denied.Code != 403 {
		t.Fatal(denied.Code)
	}
	if _, e = s.Pool.Exec(ctx, `INSERT INTO user_permissions(user_id,permission) VALUES($1,'settings')`, staff); e != nil {
		t.Fatal(e)
	}
	if allowed := request("GET", "/admin/cashback-policies/current", "", staffToken, "", ""); allowed.Code != 200 {
		t.Fatal(allowed.Code, allowed.Body.String())
	}
	current, e := (&cashback.Service{Store: s}).Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id,tier_code,min_share_bps,max_share_bps) VALUES($1,'shopee','https://shopee.vn/product/1/2','https://s.shopee.vn/test','manual-random',$2,'bronze',5001,5502)`, customer, current.ID); e != nil {
		t.Fatal(e)
	}
	manual := `{"trackingCode":"manual-random","channel":"shopee","publisher":"fixture","externalId":"manual-one","lineId":"line","productName":"Manual","value":100000,"commission":10001,"evidence":"Test evidence"}`
	if w := request("POST", "/admin/orders", manual, adminToken, au.CSRF, "manual-legacy-denied"); w.Code != 422 {
		t.Fatal("old tracking created a new order", w.Code, w.Body.String())
	}
	var count int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE publisher='fixture'`).Scan(&count); e != nil || count != 0 {
		t.Fatal(count, e)
	}

}
