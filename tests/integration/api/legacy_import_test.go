package api

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"hoanxu/internal/leaderboards"
	"hoanxu/internal/legacyimport"
)

const legacyTable = "| STT | Tên hiển thị | Mua lần đầu | Mua gần nhất | Tổng đơn |\n| ---: | --- | --- | --- | ---: |\n|1|Customer|05/03/2026|21/04/2026|3|\n|2|Một đơn|05/10/2026|05/10/2026|1|\n"

func TestLegacyImportCreditsHistoricalOrdersOnce(t *testing.T) {
	s, existing, _ := testStore(t)
	ctx := context.Background()
	plan, err := legacyimport.Prepare(strings.NewReader(legacyTable))
	if err != nil {
		t.Fatal(err)
	}
	// Preview requires neither a database nor mutations.
	var before int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM users`).Scan(&before); err != nil || before != 2 {
		t.Fatal(before, err)
	}
	var wg sync.WaitGroup
	results := make(chan legacyimport.Summary, 2)
	errors := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); result, err := plan.Apply(ctx, s.Pool); results <- result; errors <- err }()
	}
	wg.Wait()
	close(results)
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	already := 0
	for result := range results {
		if result.AlreadyApplied {
			already++
		}
		if result.Cashback != plan.Summary().Cashback {
			t.Fatal(result)
		}
	}
	if already != 1 {
		t.Fatal("concurrent retry was not a no-op", already)
	}
	var customers, orders, invalid int
	var total, available int64
	err = s.Pool.QueryRow(ctx, `SELECT
	 (SELECT count(*) FROM users WHERE role='customer'),count(*),coalesce(sum(o.cashback),0),
	 (SELECT sum(a.balance) FROM wallet_accounts a WHERE kind='available'),
	 count(*) FILTER(WHERE u.email<>'' OR o.status<>'approved' OR o.source_status<>'approved' OR o.approved_at<>o.ordered_at OR o.cashback NOT BETWEEN 5000 AND 40000 OR o.commission<>o.cashback OR o.value<>0 OR o.share_bps<>10000 OR l.original_url<>'link' OR l.affiliate_url<>'link')
	 FROM orders o JOIN users u ON u.id=o.user_id JOIN affiliate_links l ON l.id=o.link_id`).Scan(&customers, &orders, &total, &available, &invalid)
	if err != nil || customers != 3 || orders != 4 || total != plan.Summary().Cashback || available != total || invalid != 0 {
		t.Fatal(customers, orders, total, available, invalid, err)
	}
	var identities, credits, events int
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM auth_identities), (SELECT count(*) FROM wallet_transactions WHERE reference LIKE 'order_credit:%'), (SELECT count(*) FROM order_events WHERE action='approved')`).Scan(&identities, &credits, &events)
	if err != nil || identities != 0 || credits != 4 || events != 4 {
		t.Fatal(identities, credits, events, err)
	}
	var first, last string
	err = s.Pool.QueryRow(ctx, `SELECT to_char(min(ordered_at) AT TIME ZONE 'Asia/Ho_Chi_Minh','DD/MM/YYYY'),to_char(max(ordered_at) AT TIME ZONE 'Asia/Ho_Chi_Minh','DD/MM/YYYY') FROM orders o JOIN users u ON u.id=o.user_id WHERE u.name='Customer'`).Scan(&first, &last)
	if err != nil || first != "05/03/2026" || last != "21/04/2026" {
		t.Fatal(first, last, err)
	}
	var untouched int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM orders WHERE user_id=$1`, existing).Scan(&untouched); err != nil || untouched != 0 {
		t.Fatal("merged by display name", err)
	}
	assertLedger(t, s)
	board, err := (&leaderboards.Service{Store: s}).Read(ctx, "all", time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), "")
	if err != nil || board.Participants != 2 {
		t.Fatal(board, err)
	}
	month, err := (&leaderboards.Service{Store: s}).Read(ctx, "month", time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC), "")
	if err != nil || month.Participants != 1 || month.Items[0].Orders != 1 {
		t.Fatal(month, err)
	}
	changed, err := legacyimport.Prepare(strings.NewReader(strings.Replace(legacyTable, "|3|", "|4|", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = changed.Apply(ctx, s.Pool); err == nil {
		t.Fatal("changed batch accepted")
	}
}

func TestLegacyImportRollsBackAfterAnOrderWasCredited(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	_, err := s.Pool.Exec(ctx, `CREATE FUNCTION fail_legacy_import() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='legacy_import_completed' THEN
	 IF NOT EXISTS(SELECT 1 FROM wallet_entries) OR NOT EXISTS(SELECT 1 FROM wallet_accounts WHERE kind='available' AND balance>0) THEN RAISE EXCEPTION 'test did not reach credits'; END IF;
	 RAISE EXCEPTION 'injected failure after credits'; END IF; RETURN NEW; END $$;
	 CREATE TRIGGER fail_legacy_import BEFORE INSERT ON audit_logs FOR EACH ROW EXECUTE FUNCTION fail_legacy_import();`)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := legacyimport.Prepare(strings.NewReader(legacyTable))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plan.Apply(ctx, s.Pool); err == nil || !strings.Contains(err.Error(), "injected failure after credits") {
		t.Fatal("expected failure after credits", err)
	}
	var users, orders, links, credits, markers, policies int
	err = s.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users),(SELECT count(*) FROM orders),(SELECT count(*) FROM affiliate_links),(SELECT count(*) FROM wallet_transactions WHERE reference LIKE 'order_credit:%'),(SELECT count(*) FROM audit_logs WHERE action='legacy_import_completed'),(SELECT count(*) FROM cashback_policies WHERE share_percent=100)`).Scan(&users, &orders, &links, &credits, &markers, &policies)
	if err != nil || users != 2 || orders != 0 || links != 0 || credits != 0 || markers != 0 || policies != 0 {
		t.Fatal(users, orders, links, credits, markers, policies, err)
	}
	assertLedger(t, s)
	if _, err = s.Pool.Exec(ctx, `DROP TRIGGER fail_legacy_import ON audit_logs`); err != nil {
		t.Fatal(err)
	}
	if _, err = plan.Apply(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	assertLedger(t, s)
}

func TestLegacyImportFullBatchReconcilesEveryCustomer(t *testing.T) {
	s, _, _ := testStore(t)
	ctx := context.Background()
	var input strings.Builder
	input.WriteString("| STT | Tên hiển thị | Mua lần đầu | Mua gần nhất | Tổng đơn |\n| ---: | --- | --- | --- | ---: |\n")
	for i := 1; i <= 100; i++ {
		count := 67
		if i == 100 {
			count = 116
		}
		fmt.Fprintf(&input, "|%d|Khách %d|01/01/2026|05/10/2026|%d|\n", i, i, count)
	}
	plan, err := legacyimport.Prepare(strings.NewReader(input.String()))
	if err != nil {
		t.Fatal(err)
	}
	if plan.Summary().Orders != 6749 {
		t.Fatal(plan.Summary())
	}
	if _, err = plan.Apply(ctx, s.Pool); err != nil {
		t.Fatal(err)
	}
	var invalid int
	err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM (
	 SELECT u.id,u.name,count(*) n,sum(o.cashback) cash,min(o.ordered_at) first_at,max(o.ordered_at) last_at,a.balance
	 FROM orders o JOIN users u ON u.id=o.user_id JOIN wallet_accounts a ON a.user_id=u.id AND a.kind='available'
	 WHERE o.publisher='legacy-server' GROUP BY u.id,a.balance
	) c WHERE cash<>balance OR n<>CASE WHEN name='Khách 100' THEN 116 ELSE 67 END
	 OR (first_at AT TIME ZONE 'Asia/Ho_Chi_Minh')::date<>'2026-01-01'::date
	 OR (last_at AT TIME ZONE 'Asia/Ho_Chi_Minh')::date<>'2026-10-05'::date`).Scan(&invalid)
	if err != nil || invalid != 0 {
		t.Fatal(invalid, err)
	}
	assertLedger(t, s)
}
