package api

import (
	"context"
	"hoanxu/internal/wallet"
	"sync"
	"testing"
)

func TestPaidAndRejectedCannotBothSpendHeldMoney(t *testing.T) {
	s, uid, admin := testStore(t)
	ctx := context.Background()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, uid, "race-credit", "Test credit", 100000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	svc := &wallet.Service{Store: s}
	result, e := svc.Withdraw(ctx, uid, "race-withdraw", wallet.WithdrawalInput{Amount: 50000, Bank: "Bank", Account: "123456789", Holder: "Customer"})
	if e != nil {
		t.Fatal(e)
	}
	id := result.(map[string]any)["id"].(string)
	if _, e = svc.Process(ctx, admin, id, "race-process", wallet.Event{Action: "processing"}); e != nil {
		t.Fatal(e)
	}
	var file string
	e = s.Pool.QueryRow(ctx, `INSERT INTO private_files(owner_id,purpose,name,path,content_type) VALUES($1,'evidence','test.pdf','fixture.pdf','application/pdf') RETURNING id::text`, admin).Scan(&file)
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	successes := make(chan string, 2)
	for _, event := range []wallet.Event{{Action: "paid", BankReference: "race-bank-reference", Evidence: file}, {Action: "rejected", Reason: "Payment was not sent"}} {
		wg.Add(1)
		go func(event wallet.Event) {
			defer wg.Done()
			if _, e := svc.Process(ctx, admin, id, "race-"+event.Action, event); e == nil {
				successes <- event.Action
			}
		}(event)
	}
	wg.Wait()
	close(successes)
	count := 0
	winner := ""
	for action := range successes {
		count++
		winner = action
	}
	if count != 1 {
		t.Fatal("both transitions committed", count)
	}
	var available, held int64
	e = s.Pool.QueryRow(ctx, `SELECT max(balance) FILTER(WHERE kind='available'),max(balance) FILTER(WHERE kind='held') FROM wallet_accounts WHERE user_id=$1`, uid).Scan(&available, &held)
	if e != nil {
		t.Fatal(e)
	}
	expected := int64(50000)
	if winner == "rejected" {
		expected = 100000
	}
	if held != 0 || available != expected {
		t.Fatal(winner, available, held)
	}
	var mismatches int
	e = s.Pool.QueryRow(ctx, `SELECT count(*) FROM (SELECT a.id FROM wallet_accounts a LEFT JOIN wallet_entries e ON e.account_id=a.id GROUP BY a.id HAVING a.balance<>coalesce(sum(e.amount),0)) q`).Scan(&mismatches)
	if e != nil || mismatches != 0 {
		t.Fatal(mismatches, e)
	}
}
