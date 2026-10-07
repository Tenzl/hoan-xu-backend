package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"hoanxu/internal/cashback"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
)

func addExchangeTierOrders(t *testing.T, s *platform.Store, user string, count int) {
	t.Helper()
	_, err := s.Pool.Exec(context.Background(), `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at) SELECT $1,p.id,'shopee','exchange-tier',$3||g.n::text,'1','Tier order',0,0,CASE WHEN $2::int=1 THEN 500000 WHEN $2::int=30 THEN 50000 ELSE 30000 END,'approved','approved',now(),now() FROM cashback_policies p CROSS JOIN generate_series(1,$2::int) g(n) WHERE p.mode='fixed'`, user, count, platform.Token())
	if err != nil {
		t.Fatal(err)
	}
}

func TestXuExchangeTierBonuses(t *testing.T) {
	for _, tc := range []struct {
		tier     string
		orders   int
		expected int64
	}{{"member", 0, 78}, {"silver", 1, 80}, {"gold", 30, 83}, {"diamond", 100, 87}} {
		t.Run(tc.tier, func(t *testing.T) {
			s, u, admin := testStore(t)
			ctx := context.Background()
			addExchangeTierOrders(t, s, u, tc.orders)
			svc := &wallet.Service{Store: s}
			p, e := svc.CurrentExchangePolicy(ctx)
			if e != nil {
				t.Fatal(e)
			}
			p, e = svc.SetExchangePolicy(ctx, admin, "tier-rate", wallet.ExchangePolicyInput{CurrentVersionID: p.ID, GoldUnits: 4, GreenUnits: 3})
			if e != nil {
				t.Fatal(e)
			}
			tx, e := s.Pool.Begin(ctx)
			if e != nil {
				t.Fatal(e)
			}
			defer tx.Rollback(ctx)
			if e = wallet.Credit(ctx, tx, u, "tier-fund", "Fund", 101); e != nil {
				t.Fatal(e)
			}
			if e = tx.Commit(ctx); e != nil {
				t.Fatal(e)
			}
			v, e := svc.Exchange(ctx, u, "tier-conversion", wallet.ExchangeInput{GoldAmountXu: 101, ExpectedPolicyID: p.ID})
			if e != nil {
				t.Fatal(e)
			}
			b, _ := json.Marshal(v)
			var result wallet.ExchangeResult
			if e = json.Unmarshal(b, &result); e != nil {
				t.Fatal(e)
			}
			if result.GreenReceived != tc.expected {
				t.Fatalf("%s received %d; want %d", tc.tier, result.GreenReceived, tc.expected)
			}
			// Even after promotion, replay must return the originally committed result.
			addExchangeTierOrders(t, s, u, 100)
			replay, e := svc.Exchange(ctx, u, "tier-conversion", wallet.ExchangeInput{GoldAmountXu: 101, ExpectedPolicyID: p.ID})
			if e != nil {
				t.Fatal(e)
			}
			b, _ = json.Marshal(replay)
			if e = json.Unmarshal(b, &result); e != nil {
				t.Fatal(e)
			}
			if result.GreenReceived != tc.expected {
				t.Fatal("replay used a new tier")
			}
			assertLedger(t, s)
		})
	}
}

func TestXuExchangeRejectsChangedTierQuote(t *testing.T) {
	s, u, _ := testStore(t)
	ctx := context.Background()
	svc := &wallet.Service{Store: s}
	p, e := svc.CurrentExchangePolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	m, e := cashback.MembershipFor(ctx, s.Queries, u)
	if e != nil {
		t.Fatal(e)
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = wallet.Credit(ctx, tx, u, "stale-tier-fund", "Fund", 1000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	var input wallet.ExchangeInput
	if e = json.Unmarshal([]byte(fmt.Sprintf(`{"goldAmountXu":100,"expectedPolicyId":%q,"expectedTierCode":"member","expectedCashbackPolicyId":%q}`, p.ID, m.PolicyID)), &input); e != nil {
		t.Fatal(e)
	}
	addExchangeTierOrders(t, s, u, 30)
	if _, e = svc.Exchange(ctx, u, "changed-tier", input); e == nil {
		t.Fatal("changed tier quote was accepted")
	}
	var gold, green int64
	if e = s.Pool.QueryRow(ctx, `SELECT (SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='available'),(SELECT balance FROM wallet_accounts WHERE user_id=$1 AND kind='green_available')`, u).Scan(&gold, &green); e != nil {
		t.Fatal(e)
	}
	if gold != 1000 || green != 0 {
		t.Fatal("stale quote changed balances")
	}
}

func TestXuExchangeLargeExactTierAmount(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	addExchangeTierOrders(t, s, u, 100)
	svc := &wallet.Service{Store: s}
	p, e := svc.CurrentExchangePolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p, e = svc.SetExchangePolicy(ctx, admin, "large-exact-rate", wallet.ExchangePolicyInput{CurrentVersionID: p.ID, GoldUnits: 1000000, GreenUnits: 1000000})
	if e != nil {
		t.Fatal(e)
	}
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(ctx)
	if e = wallet.Credit(ctx, tx, u, "large-exact-funds", "Test", 1000000000000); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	v, e := svc.Exchange(ctx, u, "large-exact-exchange", wallet.ExchangeInput{GoldAmountXu: 800000000001, ExpectedPolicyID: p.ID})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(v)
	var result wallet.ExchangeResult
	if e = json.Unmarshal(b, &result); e != nil {
		t.Fatal(e)
	}
	if result.GreenReceived != 920000000001 || result.BonusPercent != 15 || result.TierCode != "diamond" {
		t.Fatal(string(b))
	}
	goldTotals(t, s, u, 3000000, 800000000001)
	if _, e = svc.Exchange(ctx, u, "large-exact-invalid", wallet.ExchangeInput{GoldAmountXu: 1000000000000, ExpectedPolicyID: p.ID}); e == nil {
		t.Fatal("bonus exceeded green limit")
	}
	goldTotals(t, s, u, 3000000, 800000000001)
	assertLedger(t, s)
}

func TestXuExchangeRejectsChangedMembershipPolicy(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	svc := &wallet.Service{Store: s}
	quote, e := svc.CustomerExchangePolicy(ctx, u)
	if e != nil {
		t.Fatal(e)
	}
	p, e := cashback.Current(ctx, s.Queries)
	if e != nil {
		t.Fatal(e)
	}
	// The bootstrap preserves historical fractional rates; new policies require
	// whole percentages with a range of at least five percentage points.
	for i := range p.Tiers {
		p.Tiers[i].Min = 1000
		p.Tiers[i].Max = 2000
	}
	_, e = (&cashback.Service{Store: s}).Create(ctx, admin, "exchange-membership-change", cashback.Input{CurrentVersionID: p.ID, Tiers: p.Tiers, Tax: p.Tax, PeriodConfig:p.PeriodConfig})
	if e != nil {
		t.Fatal(e)
	}
	_, e = svc.Exchange(ctx, u, "stale-membership-quote", wallet.ExchangeInput{GoldAmountXu: 10, ExpectedPolicyID: quote.ID, ExpectedTierCode: quote.TierCode, ExpectedCashbackPolicyID: quote.CashbackPolicyID})
	if e == nil {
		t.Fatal("old membership policy accepted")
	}
	var problem *platform.Error
	if !errors.As(e, &problem) || problem.Code != "EXCHANGE_TIER_CHANGED" {
		t.Fatal(e)
	}
	goldTotals(t, s, u, 0, 0)
}
