package cashback

import (
	"encoding/json"
	"testing"
)

func TestPercentAndAmount(t *testing.T) {
	for _, raw := range []string{"50", "50.01", "0", "100.00"} {
		var p Percent
		if e := json.Unmarshal([]byte(raw), &p); e != nil {
			t.Fatal(e)
		}
		b, _ := json.Marshal(p)
		var q Percent
		if e := json.Unmarshal(b, &q); e != nil || q != p {
			t.Fatal(p, q, e)
		}
	}
	for _, raw := range []string{"1.001", "\"50\"", "-1", "null"} {
		var p Percent
		if json.Unmarshal([]byte(raw), &p) == nil {
			t.Fatal("accepted", raw)
		}
	}
	a, e := Amount(999, 3333)
	if e != nil || a != 332 {
		t.Fatal(a, e)
	}
	if _, e := Amount(1000000000001, 10000); e == nil {
		t.Fatal("overflow bound missing")
	}
}
func TestTierBoundaries(t *testing.T) {
	p := &Policy{ID: "test", Tiers: []Tier{{"bronze", 0, 5000, 5000}, {"platinum", 30, 6000, 7000}, {"diamond", 100, 7000, 8000}}}
	for _, tc := range []struct {
		n         int64
		code      string
		remaining int64
	}{{0, "bronze", 30}, {29, "bronze", 1}, {30, "platinum", 70}, {99, "platinum", 1}, {100, "diamond", 0}, {101, "diamond", 0}} {
		m := Select(p, tc.n)
		if m.Code != tc.code || m.OrdersToNext != tc.remaining {
			t.Fatal(m)
		}
	}
}
func TestPolicyValidationAndRequiredFields(t *testing.T) {
	valid := Input{CurrentVersionID: "11111111-1111-4111-8111-111111111111", Tax: 500, Tiers: []Tier{{"bronze", 0, 0, 10000}, {"platinum", 30, 5000, 5500}, {"diamond", 100, 8000, 9000}}}
	if e := Validate(valid); e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*Input){func(p *Input) { p.Tiers[0].MinOrders = 1 }, func(p *Input) { p.Tiers[1].MinOrders = 100 }, func(p *Input) { p.Tiers[2].Min = 9500 }, func(p *Input) { p.Tiers[1].Code = "silver" }, func(p *Input) { p.Tiers[0].Max = 10001 }, func(p *Input) { p.Tiers = p.Tiers[:2] }} {
		p := valid
		p.Tiers = append([]Tier(nil), valid.Tiers...)
		change(&p)
		if Validate(p) == nil {
			t.Fatal("invalid policy accepted", p)
		}
	}
	for _, raw := range []string{`{"tierCode":"bronze","minApprovedOrders":0,"maxSharePercent":50}`, `{"tierCode":"bronze","minApprovedOrders":0,"minSharePercent":null,"maxSharePercent":50}`} {
		var tier Tier
		if json.Unmarshal([]byte(raw), &tier) == nil {
			t.Fatal("missing range accepted")
		}
	}
}
