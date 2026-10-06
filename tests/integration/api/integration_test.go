package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/imports"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"hoanxu/internal/rewards"
	"hoanxu/internal/wallet"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCursorPagesHaveNoDuplicatesWhenDatesTie(t *testing.T) {
	s, uid, _ := testStore(t)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, e := s.Pool.Exec(ctx, `INSERT INTO deals(user_id,channel,body,created_at) VALUES($1,'shopee',$2,'2026-10-05T00:00:00Z')`, uid, fmt.Sprintf("Deal %d", i)); e != nil {
			t.Fatal(e)
		}
	}
	srv := New(&Server{Store: s, Auth: &auth.Service{Store: s}, Affiliate: &affiliate.Service{Store: s}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	seen := map[string]bool{}
	path := "/api/v1/deals?perPage=2"
	for page := 0; page < 3; page++ {
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
		var body struct {
			Data []struct {
				ID string `json:"id"`
			}
			Meta struct {
				Next    string `json:"nextCursor"`
				HasNext bool   `json:"hasNext"`
			}
		}
		if e := json.Unmarshal(w.Body.Bytes(), &body); e != nil {
			t.Fatal(e)
		}
		for _, row := range body.Data {
			if seen[row.ID] {
				t.Fatal("duplicate cursor result", row.ID)
			}
			seen[row.ID] = true
		}
		if page < 2 && (!body.Meta.HasNext || body.Meta.Next == "") {
			t.Fatal("missing cursor")
		}
		if page == 2 && body.Meta.HasNext {
			t.Fatal("unexpected remaining rows")
		}
		path = "/api/v1/deals?perPage=2&cursor=" + body.Meta.Next
	}
	if len(seen) != 5 {
		t.Fatal("missing rows", len(seen))
	}
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, httptest.NewRequest("GET", "/api/v1/deals?cursor=invalid", nil))
	if w.Code != 422 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestCSVCommitIsRestartableAndDoesNotApprovePendingSource(t *testing.T) {
	s, uid, admin := testStore(t)
	ctx := context.Background()
	_, e := s.Pool.Exec(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id) SELECT $1,'shopee','https://shopee.vn/product/1/2','https://s.shopee.vn/test','csvtracking',id FROM cashback_policies WHERE mode='fixed'`, uid)
	if e != nil {
		t.Fatal(e)
	}
	csv := "channel,publisher,order_id,line_id,tracking_code,date,product_name,value,commission,status\nshopee,publisher,one,line,csvtracking,2026-10-05,Test,100000,5000,pending\nshopee,publisher,two,line,unknown,2026-10-05,Test,100000,5000,approved\n"
	rows, e := imports.Parse(strings.NewReader(csv), nil)
	if e != nil {
		t.Fatal(e)
	}
	svc := &imports.Service{Store: s}
	batch, e := svc.Preview(ctx, admin, "test.csv", platform.Hash(csv), nil, rows)
	if e != nil {
		t.Fatal(e)
	}
	id := batch.(map[string]any)["id"].(string)
	if _, e = svc.Commit(ctx, admin, "commit-test-1", id); e != nil {
		t.Fatal(e)
	}
	// Simulate a process dying after claim; a new worker must recover the expired lease.
	if _, e = s.Pool.Exec(ctx, `UPDATE import_batches SET status='processing',lease_until=now()-interval '1 second' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go svc.Run(workerCtx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		if e = s.Pool.QueryRow(ctx, `SELECT status FROM import_batches WHERE id=$1`, id).Scan(&status); e != nil {
			t.Fatal(e)
		}
		if status == "completed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not complete")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var oid, status, source string
	e = s.Pool.QueryRow(ctx, `SELECT id::text,status,source_status FROM orders WHERE user_id=$1`, uid).Scan(&oid, &status, &source)
	if e != nil || status != "pending" || source != "pending" {
		t.Fatal(status, source, e)
	}
	if _, e = (&orders.Service{Store: s}).Event(ctx, admin, oid, "approve-pending", orders.Event{Action: "approved"}); e == nil {
		t.Fatal("source-pending order was credited")
	}
	if _, e = svc.Commit(ctx, admin, "commit-test-1", id); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id=$1`, uid).Scan(&n); e != nil || n != 1 {
		t.Fatal(n, e)
	}
}

func TestConcurrentDebtRepayment(t *testing.T) {
	s, uid, _ := testStore(t)
	ctx := context.Background()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Post(ctx, tx, "debt-fixture", "Debt fixture", []wallet.Entry{{User: uid, Kind: "debt", Amount: 600}, {Kind: "system", Amount: -600}}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	blocker, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = blocker.Exec(ctx, `SELECT id FROM wallet_accounts WHERE kind='system' FOR UPDATE`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tx, e := s.Pool.Begin(ctx)
			if e != nil {
				errCh <- e
				return
			}
			defer tx.Rollback(ctx)
			if e = wallet.Credit(ctx, tx, uid, fmt.Sprintf("credit-%d", i), "Repay", 100); e == nil {
				e = tx.Commit(ctx)
			}
			if e != nil {
				errCh <- e
			}
		}(i)
	}
	time.Sleep(300 * time.Millisecond)
	if e = blocker.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	wg.Wait()
	close(errCh)
	for e := range errCh {
		t.Error(e)
	}
	var available, debt int64
	e = s.Pool.QueryRow(ctx, `SELECT max(balance) FILTER(WHERE kind='available'),max(balance) FILTER(WHERE kind='debt') FROM wallet_accounts WHERE user_id=$1`, uid).Scan(&available, &debt)
	if e != nil || available != 1400 || debt != 0 {
		t.Fatal(available, debt, e)
	}
}

func testStore(t *testing.T) (*platform.Store, string, string) {
	return testStoreWithTiers(t, true)
}
func testStoreWithTiers(t *testing.T, tiers bool) (*platform.Store, string, string) {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("Set TEST_DATABASE_URL to a database whose name ends in _test")
	}
	ctx := context.Background()
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("Refusing a non-test database")
	}
	schema := "hx_test_" + platform.Hash(platform.Token())[:16]
	base, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = base.Exec(ctx, `CREATE SCHEMA `+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = base.Exec(context.Background(), `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()+` CASCADE`)
		base.Close()
	})
	for _, name := range []string{"000001_initial.up.sql", "000002_defaults.up.sql", "000003_private_files.up.sql", "000004_order_source.up.sql", "000005_support_faq.up.sql", "000006_user_bank.up.sql", "000007_order_approval_time.up.sql", "000008_cashback_tiers.up.sql", "000009_unified_wallet.up.sql", "000010_browser_credentials.up.sql", "000011_remove_saved_links.up.sql"} {
		if !tiers && (name == "000008_cashback_tiers.up.sql" || name == "000009_unified_wallet.up.sql") {
			continue
		}
		raw, e := os.ReadFile(filepath.Join("../../database/migrations", name))
		if e != nil {
			t.Fatal(e)
		}
		if _, e = pool.Exec(ctx, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	s, e := platform.New(pool, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	var uid, admin string
	e = pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Customer','test@example.com','customer') RETURNING id::text`).Scan(&uid)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = platform.Accounts(ctx, tx, uid); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	admin, e = auth.CreateInternal(ctx, s, "", "admin", "Admin", "test-admin-password", "admin", nil)
	if e != nil {
		t.Fatal(e)
	}
	return s, uid, admin
}
func TestMoneyConcurrencyAndIdempotency(t *testing.T) {
	s, uid, admin := testStore(t)
	ctx := context.Background()
	var oid string
	e := s.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,ordered_at) SELECT $1,id,'shopee','test','one','one','Test',500000,200000,100000,now() FROM cashback_policies WHERE mode='fixed' RETURNING orders.id::text`, uid).Scan(&oid)
	if e != nil {
		t.Fatal(e)
	}
	osvc := &orders.Service{Store: s}
	if _, e = s.Pool.Exec(ctx, `UPDATE orders SET source_status='approved' WHERE id=$1`, oid); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	success := 0
	var mu sync.Mutex
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := osvc.Event(ctx, admin, oid, fmt.Sprintf("approve-key-%d", i), orders.Event{Action: "approved"})
			if e == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if success != 1 {
		t.Fatalf("approval successes: %d", success)
	}
	var balance int64
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, uid).Scan(&balance); e != nil || balance != 100000 {
		t.Fatal(balance, e)
	}
	w := &wallet.Service{Store: s}
	input := wallet.WithdrawalInput{Amount: 50000, Bank: "Bank", Account: "123456789", Holder: "Test Customer"}
	v, e := w.Withdraw(ctx, uid, "withdraw-key-1", input)
	if e != nil {
		t.Fatal(e)
	}
	id := v.(map[string]any)["id"].(string)
	if _, e = w.Withdraw(ctx, uid, "withdraw-key-1", input); e != nil {
		t.Fatal(e)
	}
	input.Amount = 60000
	if _, e = w.Withdraw(ctx, uid, "withdraw-key-1", input); e == nil {
		t.Fatal("key payload conflict accepted")
	}
	if _, e = w.Process(ctx, admin, id, "reject-key-1", wallet.Event{Action: "rejected", Reason: "Testing refund"}); e != nil {
		t.Fatal(e)
	}
	if _, e = w.Process(ctx, admin, id, "reject-key-2", wallet.Event{Action: "rejected", Reason: "Testing refund"}); e == nil {
		t.Fatal("second refund accepted")
	}
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, uid).Scan(&balance); e != nil || balance != 100000 {
		t.Fatal(balance, e)
	}
	var mismatch int
	e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT a.id FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0)) x`).Scan(&mismatch)
	if e != nil || mismatch != 0 {
		t.Fatal("ledger mismatch", mismatch, e)
	}
}
func TestCheckinAndExchange(t *testing.T) {
	s, uid, _ := testStore(t)
	ctx := context.Background()
	r := &rewards.Service{Store: s}
	var wg sync.WaitGroup
	success := 0
	var mu sync.Mutex
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := r.Checkin(ctx, uid); e == nil {
				mu.Lock()
				success++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if success != 1 {
		t.Fatal(success)
	}
	if _, e := r.Exchange(ctx, uid, "exchange-test", 10); e == nil {
		t.Fatal("exchange enabled by default")
	}
	var balance int64
	if e := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, uid).Scan(&balance); e != nil || balance != 300 {
		t.Fatal(balance, e)
	}
	if _, e := r.Exchange(ctx, uid, "exchange-test", 20); e == nil {
		t.Fatal("legacy exchange must remain disabled")
	}

}
func TestGiftStockAndRefund(t *testing.T) {
	s, uid, admin := testStore(t)
	ctx := context.Background()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, uid, "gift-test-coins", "Test", 30000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE gift_catalog SET stock=1 WHERE id='g1'`); e != nil {
		t.Fatal(e)
	}
	r := &rewards.Service{Store: s}
	v, e := r.Redeem(ctx, uid, "gift-test-1", "g1")
	if e != nil {
		t.Fatal(e)
	}
	id := v.(map[string]any)["id"].(string)
	if _, e = r.Redeem(ctx, uid, "gift-test-2", "g1"); e == nil {
		t.Fatal("duplicate/stock accepted")
	}
	if _, e = r.GiftEvent(ctx, admin, id, "gift-reject-1", "rejected", "", "Testing stock refund"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.GiftEvent(ctx, admin, id, "gift-reject-2", "rejected", "", "Testing stock refund"); e == nil {
		t.Fatal("second refund accepted")
	}
	var balance int64
	e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, uid).Scan(&balance)
	if e != nil || balance != 30000 {
		t.Fatal(balance, e)
	}
}
func TestHTTPAuthCSRFAndPermissions(t *testing.T) {
	s, uid, admin := testStore(t)
	a := &auth.Service{Store: s}
	srv := New(&Server{Store: s, Auth: a, Affiliate: &affiliate.Service{Store: s}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	req := httptest.NewRequest("GET", "/api/v1/wallet", nil)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal(w.Code, w.Body.String())
	}
	tx, _ := s.Pool.Begin(context.Background())
	token, e := a.NewSession(context.Background(), tx, uid)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(context.Background()); e != nil {
		t.Fatal(e)
	}
	u, e := a.Session(context.Background(), token)
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []struct {
		path, method, body, csrf string
		want                     int
	}{{"/api/v1/admin/users", "GET", "", "", 403}, {"/api/v1/checkins", "POST", "", "", 403}, {"/api/v1/checkins", "POST", "", u.CSRF, 201}} {
		req = httptest.NewRequest(p.method, p.path, strings.NewReader(p.body))
		req.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("X-CSRF-Token", p.csrf)
		w = httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != p.want {
			t.Fatalf("%s: %d %s", p.path, w.Code, w.Body.String())
		}
	}
	_, _ = s.Pool.Exec(context.Background(), `UPDATE users SET blocked=true WHERE id=$1`, uid)
	if _, e = a.Session(context.Background(), token); e == nil {
		t.Fatal("blocked session accepted")
	}
	internal, e := a.Login(context.Background(), "admin", "test-admin-password")
	if e != nil {
		t.Fatal(e)
	}
	iu, e := a.Session(context.Background(), internal)
	if e != nil || !iu.MustChange || iu.ID != admin {
		t.Fatal(iu, e)
	}
	if e = a.ChangePassword(context.Background(), iu, "test-admin-password", "new-test-admin-password"); e != nil {
		t.Fatal(e)
	}
	_ = time.Now()
}
