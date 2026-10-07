package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"hoanxu/internal/auth"
	"hoanxu/internal/leaderboards"
	"hoanxu/internal/legacyimport"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"hoanxu/internal/rewards"
	"hoanxu/internal/wallet"
)

func goldTotals(t *testing.T, s *platform.Store, u string, total, used int64) {
	t.Helper()
	var a, b int64
	if e := s.Pool.QueryRow(context.Background(), `SELECT gold_total,gold_used FROM wallet_user_totals WHERE user_id=$1`, u).Scan(&a, &b); e != nil {
		t.Fatal(e)
	}
	if a != total || b != used {
		t.Fatalf("totals = %d/%d; want %d/%d", a, b, total, used)
	}
}

func TestGoldTotalsLifecycle(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	var oid string
	must(s.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,share_bps,status,source_status,ordered_at) SELECT $1,id,'shopee','gold-totals','1','1','Test',200000,200000,100000,5000,'pending','approved',now() FROM cashback_policies WHERE mode='fixed' LIMIT 1 RETURNING orders.id::text`, u).Scan(&oid))
	osvc := &orders.Service{Store: s}
	svc := &wallet.Service{Store: s}
	_, e := osvc.Event(ctx, admin, oid, "gold-approve", orders.Event{Action: "approved"})
	must(e)
	goldTotals(t, s, u, 100000, 0)
	now := time.Now().Add(time.Second)
	before, e := (&leaderboards.Service{Store: s}).Read(ctx, "all", now, u)
	must(e)
	p, e := svc.CustomerExchangePolicy(ctx, u)
	must(e)
	input := wallet.ExchangeInput{GoldAmountXu: 10000, ExpectedPolicyID: p.ID, ExpectedTierCode: p.TierCode, ExpectedCashbackPolicyID: p.CashbackPolicyID}
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, e := svc.Exchange(ctx, u, "gold-concurrent", input); errs <- e }()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		must(e)
	}
	goldTotals(t, s, u, 100000, 10000)
	after, e := (&leaderboards.Service{Store: s}).Read(ctx, "all", now, u)
	must(e)
	b1, _ := json.Marshal(before.Me)
	b2, _ := json.Marshal(after.Me)
	if string(b1) != string(b2) {
		t.Fatal("conversion changed ranking")
	}
	_, e = (&rewards.Service{Store: s}).Checkin(ctx, u)
	must(e)
	goldTotals(t, s, u, 100000, 10000)
	w, e := svc.Withdraw(ctx, u, "gold-withdraw", wallet.WithdrawalInput{Amount: 50000, Bank: "Bank", Account: "0123456789", Holder: "CUSTOMER"})
	must(e)
	b, _ := json.Marshal(w)
	var wr struct{ ID string }
	must(json.Unmarshal(b, &wr))
	goldTotals(t, s, u, 100000, 10000)
	_, e = svc.Process(ctx, admin, wr.ID, "gold-processing", wallet.Event{Action: "processing"})
	must(e)
	goldTotals(t, s, u, 100000, 10000)
	var evidence string
	must(s.Pool.QueryRow(ctx, `INSERT INTO private_files(owner_id,purpose,name,path,content_type) VALUES($1,'evidence','Test','test-only','image/png') RETURNING id::text`, admin).Scan(&evidence))
	paid := wallet.Event{Action: "paid", BankReference: "TEST-PAID", Evidence: evidence}
	_, e = svc.Process(ctx, admin, wr.ID, "gold-paid-key", paid)
	must(e)
	_, e = svc.Process(ctx, admin, wr.ID, "gold-paid-key", paid)
	must(e)
	goldTotals(t, s, u, 100000, 60000)
	_, e = osvc.Event(ctx, admin, oid, "gold-adjustment", orders.Event{Action: "adjustment", Reason: "Adjust amount"})
	if e==nil {t.Fatal("approved correction accepted")}
	goldTotals(t, s, u, 100000, 60000)
	// Rejected corrections cannot change totals or create debt.
	var debt int64
	must(s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='debt'`, u).Scan(&debt))
	if debt != 0 {
		t.Fatal(debt)
	}
	assertLedger(t, s)
}

func TestGoldTotalsLegacyAndRollback(t *testing.T) {
	s, _, admin := testStore(t)
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	var u string
	must(s.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Legacy','','customer') RETURNING id::text`).Scan(&u))
	srv := &Server{Store: s}
	p := legacyOrderInput{ProductName: "Manual", OrderedAt: time.Now().Add(-time.Hour), Cashback: 60000}
	add := func(key string) error {
		_, e := s.Action(ctx, admin, key, "totals-manual", p, func(tx pgx.Tx) (any, error) { return srv.createLegacyOrder(ctx, tx, admin, u, p) })
		return e
	}
	must(add("totals-manual-1"))
	must(add("totals-manual-1"))
	goldTotals(t, s, u, 60000, 0)
	must(func() error {
		_, e := s.Pool.Exec(ctx, `CREATE FUNCTION reject_totals() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture totals failure'; END $$; CREATE TRIGGER reject_totals BEFORE UPDATE ON wallet_user_totals FOR EACH ROW EXECUTE FUNCTION reject_totals()`)
		return e
	}())
	if e := add("totals-manual-2"); e == nil {
		t.Fatal("expected rollback")
	}
	var count int
	var balance int64
	must(s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id=$1`, u).Scan(&count))
	must(s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, u).Scan(&balance))
	if count != 1 || balance != 60000 {
		t.Fatal(count, balance)
	}
	must(func() error {
		_, e := s.Pool.Exec(ctx, `DROP TRIGGER reject_totals ON wallet_user_totals; DROP FUNCTION reject_totals()`)
		return e
	}())
	must(add("totals-manual-2"))
	goldTotals(t, s, u, 120000, 0)
	svc := &wallet.Service{Store: s}
	v, e := svc.Withdraw(ctx, u, "totals-reject-hold", wallet.WithdrawalInput{Amount: 50000, Bank: "Bank", Account: "0123456789", Holder: "CUSTOMER"})
	must(e)
	b, _ := json.Marshal(v)
	var w struct{ ID string }
	must(json.Unmarshal(b, &w))
	_, e = svc.Process(ctx, admin, w.ID, "totals-reject", wallet.Event{Action: "rejected", Reason: "Test rejection"})
	must(e)
	goldTotals(t, s, u, 120000, 0)
	assertLedger(t, s)
}

func TestGoldTotalsHistoryBackfill(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	must := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	must(func() error {
		_, e := s.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at) SELECT $1,id,'shopee','history-total','1','1','Historical',0,90000,90000,'approved','approved',now(),now() FROM cashback_policies WHERE mode='fixed' LIMIT 1`, u)
		return e
	}())
	tx, e := s.Pool.Begin(ctx)
	must(e)
	defer tx.Rollback(ctx)
	must(wallet.Credit(ctx, tx, u, "history-total-fund", "Historical gold", 100000))
	must(tx.Commit(ctx))
	svc := &wallet.Service{Store: s}
	p, e := svc.CurrentExchangePolicy(ctx)
	must(e)
	_, e = svc.Exchange(ctx, u, "history-total-exchange", wallet.ExchangeInput{GoldAmountXu: 10000, ExpectedPolicyID: p.ID})
	must(e)
	must(func() error {
		_, e := s.Pool.Exec(ctx, `INSERT INTO withdrawals(user_id,amount,bank,bank_details,status) VALUES($1,50000,'Bank','encrypted','paid'),($1,50000,'Bank','encrypted','pending'),($1,50000,'Bank','encrypted','rejected')`, u)
		return e
	}())
	var before int64
	must(s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, u).Scan(&before))
	must(func() error { _, e := s.Pool.Exec(ctx, `DROP TABLE wallet_user_totals`); return e }())
	for _, file := range []string{"000027_wallet_user_totals.up.sql", "000028_wallet_user_totals_backfill.up.sql"} {
		raw, e := os.ReadFile("../../database/migrations/" + file)
		must(e)
		_, e = s.Pool.Exec(ctx, string(raw))
		must(e)
	}
	goldTotals(t, s, u, 90000, 60000)
	var after int64
	must(s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, u).Scan(&after))
	if before != after {
		t.Fatal("backfill changed balance")
	}
	goldTotals(t, s, admin, 0, 0) // Internal accounts also get a harmless zero statistics row.
	assertLedger(t, s)
}

func TestGoldTotalsLegacyImport(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	p, e := legacyimport.Prepare(strings.NewReader("| STT | Tên hiển thị | Mua lần đầu | Mua gần nhất | Tổng đơn |\n| ---: | --- | --- | --- | ---: |\n| 1 | Tổng khách cũ | 01/01/2026 | 02/01/2026 | 3 |\n"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Apply(ctx, s.Pool); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Apply(ctx, s.Pool); e != nil {
		t.Fatal(e)
	}
	var u string
	var total int64
	if e = s.Pool.QueryRow(ctx, `SELECT u.id::text,sum(o.cashback)::bigint FROM users u JOIN orders o ON o.user_id=u.id WHERE u.name='Tổng khách cũ' GROUP BY u.id`).Scan(&u, &total); e != nil {
		t.Fatal(e)
	}
	goldTotals(t, s, u, total, 0)
	assertLedger(t, s)
}

func TestGoldTotalsAPI(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	if _, e := s.Pool.Exec(ctx, `UPDATE wallet_user_totals SET gold_total=12345,gold_used=6789 WHERE user_id=$1`, u); e != nil {
		t.Fatal(e)
	}
	a := &auth.Service{Store: s}
	srv := New(&Server{Store: s, Auth: a, Origin: "http://localhost:3000"})
	for _, owner := range []string{u, admin} {
		tx, e := s.Pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		token, e := a.NewSession(ctx, tx, owner)
		if e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
		paths := []string{"/wallet", "/me/dashboard"}
		if owner == admin {
			_, e = s.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, admin)
			if e != nil {
				t.Fatal(e)
			}
			paths = []string{"/admin/users/" + u, "/admin/users?kind=new"}
		}
		for _, path := range paths {
			r := httptest.NewRequest("GET", "/api/v1"+path, nil)
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatal(path, w.Code, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), `"goldTotal":12345`) || !strings.Contains(w.Body.String(), `"goldUsed":6789`) {
				t.Fatal(fmt.Sprintf("missing totals: %s", w.Body.String()))
			}
		}
	}
}

func TestGoldTotalsConcurrentOrdersAndConversions(t *testing.T) {
	s, _, admin := testStore(t)
	ctx := context.Background()
	var u string
	if e := s.Pool.QueryRow(ctx, `INSERT INTO users(name,email,role) VALUES('Concurrent legacy','','customer') RETURNING id::text`).Scan(&u); e != nil {
		t.Fatal(e)
	}
	srv := &Server{Store: s}
	add := func(key string, amount int64) error {
		p := legacyOrderInput{ProductName: "Concurrent manual", OrderedAt: time.Now().Add(-time.Hour), Cashback: amount}
		_, e := s.Action(ctx, admin, key, "concurrent-manual", p, func(tx pgx.Tx) (any, error) { return srv.createLegacyOrder(ctx, tx, admin, u, p) })
		return e
	}
	if e := add("concurrent-initial", 60000); e != nil {
		t.Fatal(e)
	}
	svc := &wallet.Service{Store: s}
	p, e := svc.CurrentExchangePolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 10)
	for i := 0; i < 5; i++ {
		wg.Add(2)
		go func(i int) { defer wg.Done(); failures <- add(fmt.Sprintf("concurrent-order-%d", i), 1000) }(i)
		go func(i int) {
			defer wg.Done()
			_, e := svc.Exchange(ctx, u, fmt.Sprintf("concurrent-exchange-%d", i), wallet.ExchangeInput{GoldAmountXu: 2000, ExpectedPolicyID: p.ID})
			failures <- e
		}(i)
	}
	wg.Wait()
	close(failures)
	for e := range failures {
		if e != nil {
			t.Fatal(e)
		}
	}
	goldTotals(t, s, u, 65000, 10000)
	var available int64
	if e := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, u).Scan(&available); e != nil {
		t.Fatal(e)
	}
	if available != 55000 {
		t.Fatal(available)
	}
	assertLedger(t, s)
}
