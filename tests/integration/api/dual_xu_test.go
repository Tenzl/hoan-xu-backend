package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"hoanxu/internal/auth"
	"hoanxu/internal/platform"
	"hoanxu/internal/rewards"
	"hoanxu/internal/wallet"
)

func TestDualXuBalancesAndExchange(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	svc := &wallet.Service{Store: s}
	fund := func(gold, green int64) {
		t.Helper()
		tx, e := s.Pool.Begin(ctx)
		if e != nil {
			t.Fatal(e)
		}
		defer tx.Rollback(ctx)
		if e = wallet.Credit(ctx, tx, u, fmt.Sprintf("gold:%d", gold), "Gold", gold); e != nil {
			t.Fatal(e)
		}
		if e = wallet.CreditGreen(ctx, tx, u, fmt.Sprintf("green:%d", green), "Green", green); e != nil {
			t.Fatal(e)
		}
		if e = tx.Commit(ctx); e != nil {
			t.Fatal(e)
		}
	}
	balance := func(kind string) int64 {
		t.Helper()
		var n int64
		if e := s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind=$2`, u, kind).Scan(&n); e != nil {
			t.Fatal(e)
		}
		return n
	}
	fund(60000, 100000)
	policy, e := svc.CurrentExchangePolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	changed, e := svc.SetExchangePolicy(ctx, admin, "exchange-policy-0001", wallet.ExchangePolicyInput{CurrentVersionID: policy.ID, GoldUnits: 4, GreenUnits: 3})
	if e != nil {
		t.Fatal(e)
	}
	policy = changed
	input := wallet.ExchangeInput{GoldAmountXu: 101, ExpectedPolicyID: policy.ID}
	var wg sync.WaitGroup
	results := make(chan any, 5)
	failures := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, e := svc.Exchange(ctx, u, "exchange-concurrent", input)
			if e != nil {
				failures <- e
			} else {
				results <- v
			}
		}()
	}
	wg.Wait()
	close(results)
	close(failures)
	for e := range failures {
		t.Fatal(e)
	}
	for v := range results {
		b, _ := json.Marshal(v)
		var result wallet.ExchangeResult
		if e := json.Unmarshal(b, &result); e != nil {
			t.Fatal(e)
		}
		if result.GoldSpent != 101 || result.GreenReceived != 78 {
			t.Fatal(string(b))
		}
	}
	if balance("available") != 59899 || balance("green_available") != 100078 {
		t.Fatal("incorrect conversion balances")
	}
	if _, e = svc.SetExchangePolicy(ctx, admin, "exchange-policy-0002", wallet.ExchangePolicyInput{CurrentVersionID: policy.ID, GoldUnits: 1, GreenUnits: 2}); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Exchange(ctx, u, "exchange-concurrent", input); e != nil {
		t.Fatal("replay after policy changed", e)
	}
	if _, e = svc.Exchange(ctx, u, "exchange-stale-new", input); e == nil {
		t.Fatal("stale rate accepted")
	}
	current, e := svc.CurrentExchangePolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for i, amount := range []int64{0, -1, 1000000000001, 60000} {
		if _, e = svc.Exchange(ctx, u, fmt.Sprintf("invalid-exchange-%d", i), wallet.ExchangeInput{GoldAmountXu: amount, ExpectedPolicyID: current.ID}); e == nil {
			t.Fatal("invalid exchange", amount)
		}
	}
	zero, e := svc.SetExchangePolicy(ctx, admin, "exchange-policy-0003", wallet.ExchangePolicyInput{CurrentVersionID: current.ID, GoldUnits: 1000, GreenUnits: 1})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Exchange(ctx, u, "exchange-rounded-zero", wallet.ExchangeInput{GoldAmountXu: 1, ExpectedPolicyID: zero.ID}); e == nil {
		t.Fatal("zero-result exchange accepted")
	}
	if balance("available") != 59899 {
		t.Fatal("failed exchange charged gold")
	}
	if _, e = svc.Withdraw(ctx, u, "withdraw-gold-only", wallet.WithdrawalInput{Amount: 60000, Bank: "Bank", Account: "0123456789", Holder: "CUSTOMER"}); e == nil {
		t.Fatal("green used for withdrawal")
	}
	beforeGold, beforeGreen := balance("available"), balance("green_available")
	if _, e = (&rewards.Service{Store: s}).Checkin(ctx, u); e != nil {
		t.Fatal(e)
	}
	if balance("available") != beforeGold || balance("green_available") != beforeGreen+300 {
		t.Fatal("checkin currency incorrect")
	}
	// The DB also rejects a globally balanced transaction that exchanges types without system legs.
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var transaction string
	if e = tx.QueryRow(ctx, `INSERT INTO wallet_transactions(reference,description) VALUES('bad-currency-ledger','Test') RETURNING id::text`).Scan(&transaction); e != nil {
		t.Fatal(e)
	}
	_, e = tx.Exec(ctx, `INSERT INTO wallet_entries(transaction_id,account_id,amount) SELECT $1,id,CASE WHEN kind='available' THEN -1 ELSE 1 END FROM wallet_accounts WHERE user_id=$2 AND kind IN ('available','green_available')`, transaction, u)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e == nil {
		t.Fatal("cross-currency imbalance accepted")
	}
	assertLedger(t, s)
}

func TestDualXuGiftFundingAndDebt(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	r := &rewards.Service{Store: s}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, u, "gift-gold-funds", "Gold", 100000); e != nil {
		t.Fatal(e)
	}
	if e = wallet.Post(ctx, tx, "gift-debt", "Debt", []wallet.Entry{{User: u, Kind: "debt", Amount: 1000}, {Kind: "system", Amount: -1000}}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE gift_catalog SET stock=3 WHERE id IN ('g1','g2')`); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Redeem(ctx, u, "gift-no-green", "g1"); e == nil {
		t.Fatal("gift used gold without conversion")
	}
	tx, e = s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.CreditGreen(ctx, tx, u, "gift-green-funds", "Green", 100000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var debt int64
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='debt'`, u).Scan(&debt); e != nil || debt != 1000 {
		t.Fatal("green repaid gold debt", debt, e)
	}
	v, e := r.Redeem(ctx, u, "gift-green-hold", "g1")
	if e != nil {
		t.Fatal(e)
	}
	id := v.(map[string]any)["id"].(string)
	var currency string
	if e = s.Pool.QueryRow(ctx, `SELECT currency FROM gift_redemptions WHERE id=$1`, id).Scan(&currency); e != nil || currency != "green" {
		t.Fatal(currency, e)
	}
	if _, e = r.OutOfStock(ctx, admin, "g1", "gift-green-outstock"); e != nil {
		t.Fatal(e)
	}
	var gold, green, held int64
	if e = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_gift_held')`, u).Scan(&gold, &green, &held); e != nil || gold != 100000 || green != 100000 || held != 0 {
		t.Fatal(gold, green, held, e)
	}
	// A pre-cutover reservation must continue refunding gold.
	tx, e = s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.QueryRow(ctx, `INSERT INTO gift_redemptions(user_id,gift_id,cost,cost_xu,currency) VALUES($1,'g2',27000,27000,'gold') RETURNING id::text`, u).Scan(&id); e != nil {
		t.Fatal(e)
	}
	if e = wallet.Post(ctx, tx, "old-gift-hold", "Historical hold", []wallet.Entry{{User: u, Kind: "available", Amount: -27000}, {User: u, Kind: "gift_held", Amount: 27000}}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = r.GiftEvent(ctx, admin, id, "old-gift-refund", "rejected", "", "Historical request"); e != nil {
		t.Fatal(e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, u).Scan(&gold); e != nil || gold != 100000 {
		t.Fatal(gold, e)
	}
	assertLedger(t, s)
}

func TestDualXuHTTPPermissionsAndValidation(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: s}
	srv := New(&Server{Store: s, Auth: a, Origin: "http://localhost:3000"})
	session := func(id string, recent bool) (string, string) {
		t.Helper()
		if _, e := s.Pool.Exec(ctx, `UPDATE internal_credentials SET must_change=false WHERE user_id=$1`, id); e != nil {
			t.Fatal(e)
		}
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
		user, e := a.Session(ctx, token)
		if e != nil {
			t.Fatal(e)
		}
		if recent {
			if _, e = s.Pool.Exec(ctx, `UPDATE sessions SET reauthenticated_at=now() WHERE id=$1`, user.SessionID); e != nil {
				t.Fatal(e)
			}
		}
		return token, user.CSRF
	}
	ct, cc := session(u, false)
	at, ac := session(admin, true)
	request := func(method, path, body, token, csrf string, status int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		}
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("X-CSRF-Token", csrf)
		req.Header.Set("Idempotency-Key", "http-exchange-key")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		if w.Code != status {
			t.Fatalf("%s %s got %d want %d: %s", method, path, w.Code, status, w.Body.String())
		}
		return w
	}
	w := request("GET", "/wallet/exchange-policy", "", ct, "", 200)
	var policy struct{ Data wallet.ExchangePolicy }
	if e := json.Unmarshal(w.Body.Bytes(), &policy); e != nil {
		t.Fatal(e)
	}
	body := fmt.Sprintf(`{"currentVersionId":%q,"goldUnits":4,"greenUnits":3}`, policy.Data.ID)
	request("POST", "/admin/xu-exchange-policies", body, ct, cc, 403)
	request("POST", "/admin/xu-exchange-policies", body, at, "", 403)
	st, sc := session(admin, false)
	request("POST", "/admin/xu-exchange-policies", body, st, sc, 403)
	request("POST", "/admin/xu-exchange-policies", body, at, ac, 201)
	request("GET", "/wallet/exchange-policy", "", "", "", 401)
	request("GET", "/wallet/exchange-policy", "", at, "", 403)
	request("POST", "/wallet/exchanges", `{}`, at, ac, 403)
	request("POST", "/wallet/exchanges", `{}`, ct, "", 403)
	staff, e := auth.CreateInternal(ctx, s, admin, "no-settings", "Staff", "test-staff-password", "staff", nil)
	if e != nil {
		t.Fatal(e)
	}
	staffToken, staffCSRF := session(staff, true)
	request("GET", "/admin/xu-exchange-policies/current", "", staffToken, "", 403)
	request("POST", "/admin/xu-exchange-policies", body, staffToken, staffCSRF, 403)
	if _, e = s.Pool.Exec(ctx, `INSERT INTO user_permissions(user_id,permission) VALUES($1,'settings')`, staff); e != nil {
		t.Fatal(e)
	}
	currentResponse := request("GET", "/admin/xu-exchange-policies/current", "", staffToken, "", 200)
	if e = json.Unmarshal(currentResponse.Body.Bytes(), &policy); e != nil {
		t.Fatal(e)
	}
	request("POST", "/admin/xu-exchange-policies", fmt.Sprintf(`{"currentVersionId":%q,"goldUnits":1,"greenUnits":1}`, policy.Data.ID), staffToken, staffCSRF, 201)
	for _, body := range []string{`{"goldAmountXu":1.5,"expectedPolicyId":"invalid"}`, `{"goldAmountXu":1,"expectedPolicyId":"invalid","direction":"green-to-gold"}`} {
		request("POST", "/wallet/exchanges", body, ct, cc, 400)
	}
	request("POST", "/wallet/exchanges", `{"goldAmountXu":1,"expectedPolicyId":"invalid"}`, ct, cc, 422)
	for _, path := range []string{"/wallet", "/me/dashboard"} {
		w = request("GET", path, "", ct, "", 200)
		var b struct{ Data map[string]any }
		if e := json.Unmarshal(w.Body.Bytes(), &b); e != nil {
			t.Fatal(e)
		}
		for _, key := range []string{"goldAvailable", "greenAvailable", "greenGiftHeld"} {
			if _, ok := b.Data[key]; !ok {
				t.Fatalf("missing %s: %s", key, w.Body.String())
			}
		}
	}
}

func TestDualXuMigrationPreservesGoldAndGuardsRollback(t *testing.T) {
	s, u, _ := testStore(t)
	ctx := context.Background()
	run := func(name, dir string) error {
		raw, e := os.ReadFile("../../database/migrations/" + name + "." + dir + ".sql")
		if e != nil {
			return e
		}
		_, e = s.Pool.Exec(ctx, string(raw))
		return e
	}
	for _, n := range []string{"000025_dual_xu_seed", "000024_dual_xu"} {
		if e := run(n, "down"); e != nil {
			t.Fatal(e)
		}
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, u, "pre-dual-gold", "Historical", 54321); e != nil {
		t.Fatal(e)
	}
	if e = wallet.Post(ctx, tx, "pre-dual-holds", "Historical holds and debt", []wallet.Entry{{User: u, Kind: "available", Amount: -10000}, {User: u, Kind: "held", Amount: 5000}, {User: u, Kind: "gift_held", Amount: 5000}, {User: u, Kind: "debt", Amount: 700}, {Kind: "system", Amount: -700}}); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `INSERT INTO gift_redemptions(user_id,gift_id,cost,cost_xu,cost_unit) VALUES($1,'g1',5000,5000,'xu')`, u); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var before string
	var accountsBefore string
	accountDigest := `SELECT md5(string_agg(to_jsonb(a)::text,',' ORDER BY a.id)) FROM wallet_accounts a WHERE kind NOT LIKE 'green_%'`
	if e = s.Pool.QueryRow(ctx, accountDigest).Scan(&accountsBefore); e != nil {
		t.Fatal(e)
	}
	digest := `SELECT md5(string_agg(to_jsonb(e)::text,',' ORDER BY e.id)) FROM wallet_entries e`
	if e = s.Pool.QueryRow(ctx, digest).Scan(&before); e != nil {
		t.Fatal(e)
	}
	for _, n := range []string{"000024_dual_xu", "000025_dual_xu_seed"} {
		if e = run(n, "up"); e != nil {
			t.Fatal(e)
		}
	}
	var after string
	var accountsAfter string
	if e = s.Pool.QueryRow(ctx, accountDigest).Scan(&accountsAfter); e != nil || accountsBefore != accountsAfter {
		t.Fatal("gold accounts changed", e)
	}
	var gold, green int64
	if e = s.Pool.QueryRow(ctx, digest).Scan(&after); e != nil || after != before {
		t.Fatal("ledger rewritten", e)
	}
	if e = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available')`, u).Scan(&gold, &green); e != nil || gold != 44321 || green != 0 {
		t.Fatal(gold, green, e)
	}
	var currency string
	if e = s.Pool.QueryRow(ctx, `SELECT currency FROM gift_redemptions WHERE user_id=$1`, u).Scan(&currency); e != nil || currency != "gold" {
		t.Fatal("historical gift funding changed", currency, e)
	}
	if _, e = (&rewards.Service{Store: s}).Checkin(ctx, u); e != nil {
		t.Fatal(e)
	}
	if e = run("000025_dual_xu_seed", "down"); e == nil {
		t.Fatal("rollback deleted green history")
	}
	if e = run("000024_dual_xu", "down"); e == nil {
		t.Fatal("schema rollback accepted green history")
	}
	assertLedger(t, s)
}

func TestDualXuExchangeRollbackAndLimits(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	svc := &wallet.Service{Store: s}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = wallet.Credit(ctx, tx, u, "rollback-funds", "Test", 1000000000000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	policy, e := svc.CurrentExchangePolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `CREATE FUNCTION fail_green_credit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='green_available' AND NEW.balance>OLD.balance THEN RAISE EXCEPTION 'fixture green failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER fail_green_credit BEFORE UPDATE ON wallet_accounts FOR EACH ROW EXECUTE FUNCTION fail_green_credit()`); e != nil {
		t.Fatal(e)
	}
	input := wallet.ExchangeInput{GoldAmountXu: 101, ExpectedPolicyID: policy.ID}
	if _, e = svc.Exchange(ctx, u, "rollback-exchange", input); e == nil {
		t.Fatal("expected rollback")
	}
	var gold, green int64
	var count int
	if e = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available'),(SELECT count(*) FROM wallet_transactions WHERE reference LIKE 'xu_exchange:%')`, u).Scan(&gold, &green, &count); e != nil || gold != 1000000000000 || green != 0 || count != 0 {
		t.Fatal(gold, green, count, e)
	}
	if _, e = s.Pool.Exec(ctx, `DROP TRIGGER fail_green_credit ON wallet_accounts;DROP FUNCTION fail_green_credit()`); e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Exchange(ctx, u, "rollback-exchange", input); e != nil {
		t.Fatal("same key retry after rollback", e)
	}
	large, e := svc.SetExchangePolicy(ctx, admin, "maximum-policy", wallet.ExchangePolicyInput{CurrentVersionID: policy.ID, GoldUnits: 1, GreenUnits: 1000000})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = svc.Exchange(ctx, u, "above-green-limit", wallet.ExchangeInput{GoldAmountXu: 1000000000000, ExpectedPolicyID: large.ID}); e == nil {
		t.Fatal("output exceeded maximum")
	}
	for _, units := range []int64{0, -1, 1000001} {
		if _, e = svc.SetExchangePolicy(ctx, admin, fmt.Sprintf("invalid-rate-%d", units), wallet.ExchangePolicyInput{CurrentVersionID: large.ID, GoldUnits: units, GreenUnits: 1}); e == nil {
			t.Fatal("invalid rate accepted")
		}
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE xu_exchange_policies SET green_units=2 WHERE id=$1`, large.ID); e == nil {
		t.Fatal("policy mutable")
	}
	var audit int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM audit_logs WHERE action='xu_exchange_policy_created' AND actor_id=$1`, admin).Scan(&audit); e != nil || audit != 1 {
		t.Fatal(audit, e)
	}
	assertLedger(t, s)
}

func TestDualXuMixedGiftRefund(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	r := &rewards.Service{Store: s}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	var old string
	if e = tx.QueryRow(ctx, `INSERT INTO users(name,role) VALUES('Old customer','customer') RETURNING id::text`).Scan(&old); e != nil {
		t.Fatal(e)
	}
	if e = platform.Accounts(ctx, tx, old); e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, old, "mixed-old-gold", "Historical", 5000); e != nil {
		t.Fatal(e)
	}
	if e = wallet.CreditGreen(ctx, tx, u, "mixed-new-green", "Green", 20000); e != nil {
		t.Fatal(e)
	}
	var id string
	if e = tx.QueryRow(ctx, `INSERT INTO gift_redemptions(user_id,gift_id,cost,cost_xu,currency) VALUES($1,'g1',5000,5000,'gold') RETURNING id::text`, old).Scan(&id); e != nil {
		t.Fatal(e)
	}
	if e = wallet.Post(ctx, tx, "gift_hold:"+id, "Old hold", []wallet.Entry{{User: old, Kind: "available", Amount: -5000}, {User: old, Kind: "gift_held", Amount: 5000}}); e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(ctx, `UPDATE gift_catalog SET stock=3 WHERE id='g1'`); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = r.Redeem(ctx, u, "mixed-new-gift", "g1"); e != nil {
		t.Fatal(e)
	}
	v, e := r.OutOfStock(ctx, admin, "g1", "mixed-out-stock")
	if e != nil {
		t.Fatal(e)
	}
	result := v.(rewards.OutOfStockResult)
	if result.RefundedCount != 2 || result.RefundedGoldXu != 5000 || result.RefundedGreenXu != 10500 {
		t.Fatal(result)
	}
	if _, e = r.OutOfStock(ctx, admin, "g1", "mixed-out-stock"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.OutOfStock(ctx, admin, "g1", "mixed-out-again"); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM notifications WHERE (recipient_id=$1 AND body LIKE '%5.000 Xu vàng%') OR (recipient_id=$2 AND body LIKE '%10.500 Xu xanh%')`, old, u).Scan(&n); e != nil || n != 2 {
		t.Fatal(n, e)
	}
	assertLedger(t, s)
}

func TestDualXuDifferentKeysCannotOverspend(t *testing.T) {
	s, u, _ := testStore(t)
	ctx := context.Background()
	svc := &wallet.Service{Store: s}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = wallet.Credit(ctx, tx, u, "distinct-key-funds", "Test", 100); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	policy, e := svc.CurrentExchangePolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	outcomes := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, e := svc.Exchange(ctx, u, fmt.Sprintf("distinct-key-%d", index), wallet.ExchangeInput{GoldAmountXu: 100, ExpectedPolicyID: policy.ID})
			outcomes <- e
		}(i)
	}
	wg.Wait()
	close(outcomes)
	wins := 0
	for e := range outcomes {
		if e == nil {
			wins++
		} else {
			var problem *platform.Error
			if !errors.As(e, &problem) || problem.Code != "INSUFFICIENT_BALANCE" {
				t.Fatal(e)
			}
		}
	}
	if wins != 1 {
		t.Fatal(wins)
	}
	var gold, green int64
	if e = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available')`, u).Scan(&gold, &green); e != nil || gold != 0 || green != 103 {
		t.Fatal(gold, green, e)
	}
	assertLedger(t, s)
}
