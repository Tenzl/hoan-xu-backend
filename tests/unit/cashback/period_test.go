package cashback

import (
	"testing"
	"time"
)

func periodPolicyForTest() *Policy {
	return &Policy{PeriodConfig: PeriodConfig{6, "2026-01-01", "approved"}, Tiers: []Tier{
		{Code: "member", NameVI: "Thân thiết", NameEN: "Member", ExchangeBonus: 3, Min: 6500, Max: 7500},
		{Code: "silver", NameVI: "Bạc", NameEN: "Silver", MinGold: 500000, ExchangeBonus: 6, Min: 7500, Max: 8500},
		{Code: "gold", NameVI: "Vàng", NameEN: "Gold", MinGold: 1500000, ExchangeBonus: 10, Min: 8500, Max: 9500},
		{Code: "diamond", NameVI: "Kim cương", NameEN: "Diamond", MinGold: 3000000, ExchangeBonus: 15, Min: 9000, Max: 10000},
	}}
}
func TestPeriodTierMoneyBoundariesAndRetention(t *testing.T) {
	p := periodPolicyForTest()
	for _, c := range []struct {
		gold int64
		code string
	}{{0, "member"}, {499999, "member"}, {500000, "silver"}, {1499999, "silver"}, {1500000, "gold"}, {2999999, "gold"}, {3000000, "diamond"}, {3000001, "diamond"}} {
		m := SelectPeriod(p, 0, c.gold)
		if m.Code != c.code || m.GoldToMaintain != 0 || m.NextPeriodTierCode != c.code {
			t.Fatal(c, m)
		}
	}
	held := SelectPeriod(p, 3000000, 600000)
	if held.Code != "diamond" || held.StartingTierCode != "diamond" || held.NextPeriodTierCode != "silver" || held.GoldToMaintain != 2400000 || held.NextTier != nil {
		t.Fatal(held)
	}
	next := SelectPeriod(p, 600000, 0)
	if next.Code != "silver" || next.GoldToMaintain != 500000 || next.GoldToNext != 1500000 {
		t.Fatal(next)
	}
	if SelectPeriod(p, 0, 0).Code != "member" {
		t.Fatal("inactive periods retained old high tier")
	}
}
func TestMembershipCalendarClampsWithoutDrift(t *testing.T) {
	for _, c := range []struct {
		months                            int
		anchor, now, start, end, previous string
	}{
		{6, "2026-01-01", "2026-06-30T23:59:59+07:00", "2026-01-01", "2026-07-01", "2025-07-01"},
		{6, "2026-01-01", "2026-07-01T00:00:00+07:00", "2026-07-01", "2027-01-01", "2026-01-01"},
		{1, "2024-01-31", "2024-02-29T00:00:00+07:00", "2024-02-29", "2024-03-31", "2024-01-31"},
		{1, "2026-01-31", "2026-03-30T23:59:59+07:00", "2026-02-28", "2026-03-31", "2026-01-31"},
		{1, "2026-01-31", "2026-03-31T00:00:00+07:00", "2026-03-31", "2026-04-30", "2026-02-28"},
		{5, "2026-01-31", "2025-12-30T00:00:00+07:00", "2025-08-31", "2026-01-31", "2025-03-31"},
	} {
		now, _ := time.Parse(time.RFC3339, c.now)
		prev, start, end, e := PeriodBounds(PeriodConfig{c.months, c.anchor, "approved"}, now)
		if e != nil || start.Format("2006-01-02") != c.start || end.Format("2006-01-02") != c.end || prev.Format("2006-01-02") != c.previous {
			t.Fatal(c, prev, start, end, e)
		}
	}
	for _, p := range []PeriodConfig{{0, "2026-01-01", "approved"}, {13, "2026-01-01", "approved"}, {6, "2026-02-30", "approved"}, {6, "2026-01-01", "wrong"}} {
		if _, _, _, e := PeriodBounds(p, time.Now()); e == nil {
			t.Fatal("invalid calendar", p)
		}
	}
}

func periodInputForUnit(in Input) Input {
	if len(in.Tiers) == 3 {
		in.Tiers = append(in.Tiers, in.Tiers[2])
		in.Tiers[3].MinGold = in.Tiers[2].MinGold + 100
	}
	model := periodPolicyForTest()
	for i := range in.Tiers {
		in.Tiers[i].Code = model.Tiers[i].Code
		in.Tiers[i].NameVI = model.Tiers[i].NameVI
		in.Tiers[i].NameEN = model.Tiers[i].NameEN
		in.Tiers[i].ExchangeBonus = model.Tiers[i].ExchangeBonus
	}
	in.PeriodConfig = model.PeriodConfig
	return in
}

func TestPeriodPolicyValidation(t *testing.T) {
	p := periodPolicyForTest()
	base := Input{CurrentVersionID: "11111111-1111-4111-8111-111111111111", Tiers: p.Tiers, Tax: 500, PeriodConfig: p.PeriodConfig}
	if e := Validate(base); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Input){
		func(p *Input) { p.Tiers[1].NameVI = " " }, func(p *Input) { p.Tiers[1].NameEN = string(make([]byte, 41)) },
		func(p *Input) { p.Tiers[1].MinGold = 0 }, func(p *Input) { p.Tiers[3].MinGold = 1000000000001 },
		func(p *Input) { p.Tiers[1].ExchangeBonus = -1 }, func(p *Input) { p.Tiers[1].ExchangeBonus = 101 },
		func(p *Input) { p.PeriodMonths = 0 }, func(p *Input) { p.PeriodMonths = 13 }, func(p *Input) { p.AnchorDate = "2026-02-29" }, func(p *Input) { p.DateBasis = "random" },
	} {
		in := base
		in.Tiers = append([]Tier(nil), base.Tiers...)
		change(&in)
		if e := Validate(in); e == nil {
			t.Fatal("invalid configurable policy accepted", in)
		}
	}
}
