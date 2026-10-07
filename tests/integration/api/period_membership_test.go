package api

import (
	"context"
	"encoding/json"
	"hoanxu/internal/cashback"
	"hoanxu/internal/orders"
	"hoanxu/internal/platform"
	"hoanxu/internal/wallet"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestPeriodMembershipDefaults(t *testing.T) {
	s, u, _ := testStore(t)
	p, e := (&cashback.Service{Store: s}).Current(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Tiers) != 4 {
		t.Fatalf("want four period tiers, got %#v", p.Tiers)
	}
	for i, c := range []struct {
		code        string
		gold, bonus int64
	}{{"member", 0, 3}, {"silver", 500000, 6}, {"gold", 1500000, 10}, {"diamond", 3000000, 15}} {
		if p.Tiers[i].Code != c.code || p.Tiers[i].MinGold != c.gold || p.Tiers[i].ExchangeBonus != c.bonus {
			t.Fatal(p.Tiers)
		}
	}
	m, e := cashback.MembershipFor(context.Background(), s.Queries, u)
	if e != nil || m.Code != "member" {
		t.Fatalf("new member: %#v %v", m, e)
	}
}

// Older regression fixtures supplied three ranges. Preserve those ranges while
// exercising the new four-tier policy API instead of the removed order-count API.
func periodFixtureInput(id string, old []cashback.Tier, tax cashback.Percent) cashback.Input {
	codes := []string{"member", "silver", "gold", "diamond"}
	vi := []string{"Thân thiết", "Bạc", "Vàng", "Kim cương"}
	en := []string{"Member", "Silver", "Gold", "Diamond"}
	bonuses := []int64{3, 6, 10, 15}
	rows := []cashback.Tier{old[0], old[0], old[1], old[2]}
	rows[1].MinGold = max(int64(1), old[1].MinGold/2)
	for i := range rows {
		rows[i].Code = codes[i]
		rows[i].NameVI = vi[i]
		rows[i].NameEN = en[i]
		rows[i].ExchangeBonus = bonuses[i]
	}
	return cashback.Input{CurrentVersionID: id, Tax: tax, Tiers: rows, PeriodConfig: cashback.PeriodConfig{PeriodMonths: 6, AnchorDate: "2026-01-01", DateBasis: "approved"}}
}

func TestPeriodMembershipHistoricalAmountsAndDateBasis(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	_, e := s.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at)
 SELECT $1,p.id,'shopee','period',x.id,'1','Period',0,x.cash,x.cash,'approved','approved',x.ordered::timestamptz,x.approved::timestamptz
 FROM (SELECT id FROM cashback_policies WHERE mode='fixed' LIMIT 1) p CROSS JOIN (VALUES
 ('early',3000000::bigint,'2026-06-29T00:00:00+07:00','2026-06-30T23:59:59+07:00'),
 ('boundary',500000::bigint,'2026-06-30T00:00:00+07:00','2026-07-01T00:00:00+07:00'),
 ('later',100000::bigint,'2026-07-05T00:00:00+07:00','2026-07-06T00:00:00+07:00')) x(id,cash,ordered,approved)`, u)
	if e != nil {
		t.Fatal(e)
	}
	read := func(at string) cashback.Membership {
		t.Helper()
		now, _ := time.Parse(time.RFC3339, at)
		m, e := cashback.MembershipForAt(ctx, s.Queries, u, now)
		if e != nil {
			t.Fatal(e)
		}
		return m
	}
	before := read("2026-06-30T23:59:59+07:00")
	if before.Code != "diamond" || before.PeriodGoldTotal != 3000000 {
		t.Fatal(before)
	}
	boundary := read("2026-07-01T00:00:00+07:00")
	if boundary.Code != "diamond" || boundary.PeriodGoldTotal != 500000 || boundary.PreviousPeriodGoldTotal != 3000000 {
		t.Fatal(boundary)
	}
	held := read("2026-12-31T23:59:59+07:00")
	if held.Code != "diamond" || held.GoldToMaintain != 2400000 || held.NextPeriodTierCode != "silver" {
		t.Fatal(held)
	}
	down := read("2027-01-01T00:00:00+07:00")
	if down.Code != "silver" || down.PeriodGoldTotal != 0 {
		t.Fatal(down)
	}
	if read("2027-07-01T00:00:00+07:00").Code != "member" {
		t.Fatal("carried tier across two inactive periods")
	}
	p, e := (&cashback.Service{Store: s}).Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	input := cashback.Input{CurrentVersionID: p.ID, Tiers: p.Tiers, Tax: p.Tax, PeriodConfig: p.PeriodConfig}
	input.DateBasis = "ordered"
	if _, e = (&cashback.Service{Store: s}).Create(ctx, admin, "period-date-basis", input); e != nil {
		t.Fatal(e)
	}
	m := read("2026-12-31T23:59:59+07:00")
	if m.PreviousPeriodGoldTotal != 3500000 || m.PeriodGoldTotal != 100000 {
		t.Fatal(m)
	}
	assertLedger(t, s)
}

func TestPeriodPolicyEditsAndDisabledApprovedAdjustments(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	svc := &cashback.Service{Store: s}
	p, e := svc.Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	input := cashback.Input{CurrentVersionID: p.ID, Tiers: p.Tiers, Tax: p.Tax, PeriodConfig: p.PeriodConfig}
	input.PeriodMonths = 1
	input.AnchorDate = "2026-01-31"
	input.Tiers[0].NameVI = "Khách thân thiết"
	input.Tiers[0].NameEN = "Loyal customer"
	input.Tiers[0].ExchangeBonus = 27
	first, e := svc.Create(ctx, admin, "period-settings", input)
	if e != nil {
		t.Fatal(e)
	}
	again, e := svc.Create(ctx, admin, "period-settings", input)
	if e != nil {
		t.Fatal(e)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(again)
	var left, right any
	json.Unmarshal(a, &left)
	json.Unmarshal(b, &right)
	if !reflect.DeepEqual(left, right) {
		t.Fatal("policy replay changed")
	}
	if _, e = svc.Create(ctx, admin, "period-settings-conflict", input); e == nil {
		t.Fatal("stale version accepted")
	}
	q, e := (&wallet.Service{Store: s}).CustomerExchangePolicy(ctx, u)
	if e != nil || q.BonusPercent != 27 || q.NameVI != "Khách thân thiết" {
		t.Fatal(q, e)
	}
	var oid string
	e = s.Pool.QueryRow(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at) SELECT $1,id,'shopee','period','locked','1','Locked',0,1000,1000,'approved','approved',now(),now() FROM cashback_policies WHERE mode='fixed' LIMIT 1 RETURNING id::text`, u).Scan(&oid)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = (&orders.Service{Store: s}).Event(ctx, admin, oid, platform.Token(), orders.Event{Action: "adjustment", Reason: "Attempt correction"}); e == nil {
		t.Fatal("approved adjustment accepted")
	}
	var cash, events int64
	if e = s.Pool.QueryRow(ctx, `SELECT cashback,(SELECT count(*) FROM order_events WHERE order_id=$1) FROM orders WHERE id=$1`, oid).Scan(&cash, &events); e != nil || cash != 1000 || events != 0 {
		t.Fatal(cash, events, e)
	}
	assertLedger(t, s)
}

func TestPeriodMigrationPreservesHistoryAndSnapshots(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	tx, e := s.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if e = wallet.Credit(ctx, tx, u, "period-migration-fund", "Historical", 12345); e == nil {
		e = tx.Commit(ctx)
	} else {
		_ = tx.Rollback(ctx)
	}
	if e != nil {
		t.Fatal(e)
	}
	_, e = s.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,tier_code,share_bps,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at) SELECT $1,p.id,'bronze',5000,'shopee','history','old','1','Historical',0,12345,12345,'approved','approved',now(),now() FROM cashback_policies p WHERE EXISTS(SELECT 1 FROM cashback_tiers t WHERE t.policy_id=p.id AND t.tier_code='bronze') ORDER BY p.created_at DESC LIMIT 1`, u)
	if e != nil {
		t.Fatal(e)
	}
	digest := func() string {
		t.Helper()
		var result string
		e := s.Pool.QueryRow(ctx, `SELECT md5(concat((SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM wallet_accounts t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM wallet_entries t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM wallet_transactions t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY id)::text FROM orders t),(SELECT jsonb_agg(to_jsonb(t) ORDER BY user_id)::text FROM wallet_user_totals t)))`).Scan(&result)
		if e != nil {
			t.Fatal(e)
		}
		return result
	}
	before := digest()
	run := func(name, dir string) error {
		t.Helper()
		raw, e := os.ReadFile("../../database/migrations/" + name + "." + dir + ".sql")
		if e != nil {
			t.Fatal(e)
		}
		_, e = s.Pool.Exec(ctx, string(raw))
		return e
	}
	for _, step := range []struct{ name, dir string }{{"000030_period_membership_seed", "down"}, {"000029_period_membership", "down"}, {"000029_period_membership", "up"}, {"000030_period_membership_seed", "up"}} {
		if e := run(step.name, step.dir); e != nil {
			t.Fatal(step, e)
		}
	}
	if after := digest(); after != before {
		t.Fatal("period migration changed historical money or orders")
	}
	p, e := (&cashback.Service{Store: s}).Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	for i, code := range []string{"bronze", "bronze", "platinum", "diamond"} {
		var min, max int
		e = s.Pool.QueryRow(ctx, `SELECT min_share_bps,max_share_bps FROM cashback_tiers t JOIN cashback_policies p ON p.id=t.policy_id WHERE t.tier_code=$1 AND t.min_gold_total IS NULL ORDER BY p.created_at DESC LIMIT 1`, code).Scan(&min, &max)
		if e != nil || int(p.Tiers[i].Min) != min || int(p.Tiers[i].Max) != max {
			t.Fatal("migration changed purchase ranges", code, e)
		}
	}
	_, e = (&cashback.Service{Store: s}).Create(ctx, admin, "period-down-guard", cashback.Input{CurrentVersionID: p.ID, Tiers: p.Tiers, Tax: p.Tax, PeriodConfig: p.PeriodConfig})
	if e != nil {
		t.Fatal(e)
	}
	if e = run("000030_period_membership_seed", "down"); e == nil {
		t.Fatal("down allowed after period policy was used")
	}
	assertLedger(t, s)
}

func TestPeriodPolicyThresholdChangesRecalculateImmediately(t *testing.T) {
	s, u, admin := testStore(t)
	ctx := context.Background()
	_, e := s.Pool.Exec(ctx, `INSERT INTO orders(user_id,policy_id,channel,publisher,external_id,line_id,product_name,value,commission,cashback,status,source_status,ordered_at,approved_at) SELECT $1,id,'shopee','admin-legacy','manual','1','Manual',0,600000,600000,'approved','approved',now(),now() FROM cashback_policies WHERE mode='fixed' LIMIT 1`, u)
	if e != nil {
		t.Fatal(e)
	}
	svc := &cashback.Service{Store: s}
	p, e := svc.Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	m, e := cashback.MembershipFor(ctx, s.Queries, u)
	if e != nil || m.Code != "silver" {
		t.Fatal(m, e)
	}
	in := cashback.Input{CurrentVersionID: p.ID, Tiers: p.Tiers, Tax: p.Tax, PeriodConfig: p.PeriodConfig}
	in.Tiers[1].MinGold = 700000
	if _, e = svc.Create(ctx, admin, "raise-threshold", in); e != nil {
		t.Fatal(e)
	}
	m, e = cashback.MembershipFor(ctx, s.Queries, u)
	if e != nil || m.Code != "member" {
		t.Fatal(m, e)
	}
	p, e = svc.Current(ctx)
	if e != nil {
		t.Fatal(e)
	}
	in.CurrentVersionID = p.ID
	in.Tiers[1].MinGold = 500000
	if _, e = svc.Create(ctx, admin, "lower-threshold", in); e != nil {
		t.Fatal(e)
	}
	m, e = cashback.MembershipFor(ctx, s.Queries, u)
	if e != nil || m.Code != "silver" {
		t.Fatal(m, e)
	}
	assertLedger(t, s)
}
