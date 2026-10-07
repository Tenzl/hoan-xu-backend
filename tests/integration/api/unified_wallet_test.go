package api

import (
	"context"
	"hoanxu/internal/platform"
	"hoanxu/internal/rewards"
	"hoanxu/internal/wallet"
	"os"
	"sync"
	"testing"
)

func assertLedger(t *testing.T, s *platform.Store) {
	t.Helper()
	var n int
	e := s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM (SELECT a.id FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0)) d`).Scan(&n)
	if e != nil || n != 0 {
		t.Fatal("ledger mismatch", n, e)
	}
	e = s.Pool.QueryRow(context.Background(), `SELECT count(*) FROM (SELECT e.transaction_id FROM wallet_entries e JOIN wallet_accounts a ON a.id=e.account_id GROUP BY e.transaction_id,CASE WHEN a.kind LIKE 'green_%' THEN 'green' ELSE 'gold' END HAVING sum(e.amount)<>0) d`).Scan(&n)
	if e != nil || n != 0 {
		t.Fatal("currency ledger mismatch", n, e)
	}
}
func TestUnifiedMigrationPreservesPendingGiftsAndDebtAndIsRepeatable(t *testing.T) {
	s, u, _ := testStoreWithTiers(t, false)
	ctx := context.Background()
	// Legacy customer has 10 remaining coins, and 35 coins previously reserved for a gift.
	_, e := s.Pool.Exec(ctx, `UPDATE coin_accounts SET balance=10,streak=7,best=14,last_day='2026-10-04' WHERE user_id=$1;`, u)
	if e != nil {
		t.Fatal(e)
	}
	var gift string
	e = s.Pool.QueryRow(ctx, `INSERT INTO gift_redemptions(user_id,gift_id,cost) VALUES($1,'g1',35) RETURNING id::text`, u).Scan(&gift)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	e = wallet.Post(ctx, tx, "existing-debt", "Debt", []wallet.Entry{{User: u, Kind: "debt", Amount: 1000}, {Kind: "system", Amount: -1000}})
	if e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	raw, e := os.ReadFile("../../database/migrations/000009_unified_wallet.up.sql")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		if _, e = s.Pool.Exec(ctx, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	var a, h, d, c int64
	var streak int
	e = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='gift_held'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='debt'),balance,streak FROM coin_accounts WHERE user_id=$1`, u).Scan(&a, &h, &d, &c, &streak)
	if e != nil || a != 2000 || h != 10500 || d != 0 || c != 0 || streak != 7 {
		t.Fatal(a, h, d, c, streak, e)
	}
	assertLedger(t, s)
	// Apply the later split only after validating the historical migration.
	for _, name := range []string{"000024_dual_xu", "000025_dual_xu_seed"} {
		raw, e := os.ReadFile("../../database/migrations/" + name + ".up.sql")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = s.Pool.Exec(ctx, string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	// The converted reservation is returned once and remains backed by the ledger.
	r := &rewards.Service{Store: s}
	var actor string
	if e = s.Pool.QueryRow(ctx, `SELECT id::text FROM users WHERE role='admin' LIMIT 1`).Scan(&actor); e != nil {
		t.Fatal(e)
	}
	if _, e = r.GiftEvent(ctx, actor, gift, "legacy-gift-refund", "rejected", "", "Cancelled by customer"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.GiftEvent(ctx, actor, gift, "legacy-gift-refund", "rejected", "", "Cancelled by customer"); e != nil {
		t.Fatal(e)
	}
	if _, e = r.GiftEvent(ctx, actor, gift, "legacy-gift-refund-again", "rejected", "", "Cancelled by customer"); e == nil {
		t.Fatal("double refund")
	}
	if e = s.Pool.QueryRow(ctx, `SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'`, u).Scan(&a); e != nil || a != 12500 {
		t.Fatal(a, e)
	}
	assertLedger(t, s)
}
func TestWithdrawAndGiftRaceCannotOverspend(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, u, "fund-race", "Test", 50000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Pool.Exec(ctx, `UPDATE gift_catalog SET stock=1 WHERE id='g1'`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	var gift string
	var withdrawal string
	wg.Add(2)
	go func() {
		defer wg.Done()
		v, e := (&wallet.Service{Store: s}).Withdraw(ctx, u, "withdraw-race", wallet.WithdrawalInput{Amount: 50000, Bank: "Bank", Account: "0123456789", Holder: "CUSTOMER"})
		if e == nil {
			mu.Lock()
			wins++
			withdrawal = v.(map[string]any)["id"].(string)
			mu.Unlock()
		}
	}()
	go func() {
		defer wg.Done()
		v, e := (&rewards.Service{Store: s}).Redeem(ctx, u, "gift-race-key", "g1")
		if e == nil {
			mu.Lock()
			wins++
			gift = v.(map[string]any)["id"].(string)
			mu.Unlock()
		}
	}()
	wg.Wait()
	if wins != 1 {
		t.Fatal(wins)
	}
	if gift != "" {
		if _, e = (&rewards.Service{Store: s}).GiftEvent(ctx, admin, gift, "gift-complete-key", "completed", "VOUCHER-123", ""); e != nil {
			t.Fatal(e)
		}
		if _, e = (&rewards.Service{Store: s}).GiftEvent(ctx, admin, gift, "gift-complete-again", "completed", "VOUCHER-123", ""); e == nil {
			t.Fatal("completed twice")
		}
	}
	if withdrawal != "" {
		if _, e = (&wallet.Service{Store: s}).Process(ctx, admin, withdrawal, "withdraw-refund-key", wallet.Event{Action: "rejected", Reason: "Test refund"}); e != nil {
			t.Fatal(e)
		}
	}
	assertLedger(t, s)
}
func TestCheckinCreditsGreenWithoutRepayingGoldDebt(t *testing.T) {
	s, u, _ := testStore(t)
	ctx := context.Background()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Post(ctx, tx, "checkin-debt", "Debt", []wallet.Entry{{User: u, Kind: "debt", Amount: 100}, {Kind: "system", Amount: -100}}); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	v, e := (&rewards.Service{Store: s}).Checkin(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	if v.(map[string]any)["awardXu"] != 300 || v.(map[string]any)["available"] != int64(0) || v.(map[string]any)["greenAvailable"] != int64(300) {
		t.Fatal(v)
	}
	if _, e = (&rewards.Service{Store: s}).Exchange(ctx, u, "legacy-exchange", 10); e == nil {
		t.Fatal("legacy conversion accepted")
	}
	assertLedger(t, s)
}
