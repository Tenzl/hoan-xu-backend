package cashback

import (
	"bytes"
	"encoding/json"
	"io"
	"testing"
)

func TestLinkRateEndpointsAndCeiling(t *testing.T) {
	for _, tc := range []struct {
		draw            byte
		main, effective int
		factor          string
	}{
		{0, 6600, 6300, "0.63"}, {1, 6600, 6300, "0.63"}, {4, 6900, 6600, "0.66"}, {10, 7100, 6800, "0.68"},
	} {
		rate, err := SampleLink(bytes.NewReader([]byte{tc.draw}), 6500, 7500, 500)
		if err != nil || rate.MainBps != tc.main || rate.EffectiveBps != tc.effective || rate.Factor() != tc.factor {
			t.Fatalf("draw %d: %+v %v", tc.draw, rate, err)
		}
	}
	for _, tc := range []struct{ main, tax, want int }{{6600, 0, 6600}, {6600, 500, 6300}, {6600, 10000, 0}, {0, 500, 0}, {10000, 0, 10000}, {6000, 500, 5700}, {6600, 555, 6300}} {
		got, err := EffectiveRate(tc.main, tc.tax)
		if err != nil || got != tc.want {
			t.Fatalf("effective %+v: %d %v", tc, got, err)
		}
	}
	rate, err := SampleLink(bytes.NewReader([]byte{5}), 6500, 7000, 500)
	if err != nil || rate.MainBps != 6600 {
		t.Fatalf("minimum width: %+v %v", rate, err)
	}
	if _, err = SampleLink(bytes.NewReader(nil), 6500, 7500, 500); err != io.EOF {
		t.Fatalf("entropy failure: %v", err)
	}
	for _, tc := range [][3]int{{6500, 6900, 500}, {6550, 7500, 500}, {6500, 7550, 500}, {-100, 500, 500}, {9500, 10100, 500}, {6500, 7500, -1}, {6500, 7500, 10001}} {
		if _, err := SampleLink(nil, tc[0], tc[1], tc[2]); err == nil {
			t.Fatalf("invalid range accepted: %v", tc)
		}
	}
}

func TestPublicMembershipOnlyContainsEffectiveRanges(t *testing.T) {
	p := &Policy{ID: "policy", Tax: 500, Tiers: []Tier{{Code:"bronze",MinGold:0,Min:6500,Max:7500}, {Code:"platinum",MinGold:30,Min:7500,Max:8500}, {Code:"diamond",MinGold:100,Min:8500,Max:9500}}}
	m := Select(p, 0)
	public := m.Public()
	raw, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("tax")) || bytes.Contains(raw, []byte("Tax")) {
		t.Fatalf("internal tax leaked: %s", raw)
	}
	if public.EffectiveMin != 6300 || public.EffectiveMax != 7100 || public.NextTier.EffectiveMin != 7300 || public.NextTier.EffectiveMax != 8000 {
		t.Fatalf("wrong achievable range: %+v", public)
	}
	// The 75% endpoint becomes 71%; the actual maximum is the interior 74% -> 71%.
}

func TestPolicyTaxInputAndRangeValidation(t *testing.T) {
	valid := Input{CurrentVersionID: "11111111-1111-4111-8111-111111111111", Tax: 500, Tiers: []Tier{{Code:"bronze",MinGold:0,Min:6500,Max:7500}, {Code:"platinum",MinGold:30,Min:7500,Max:8500}, {Code:"diamond",MinGold:100,Min:8500,Max:9500}}}
	valid=periodInputForUnit(valid)
	for _, change := range []func(*Input){
		func(p *Input) { p.Tax = -1 }, func(p *Input) { p.Tax = 10001 },
		func(p *Input) { p.Tiers[0].Max = 6900 }, func(p *Input) { p.Tiers[0].Min = 6550 }, func(p *Input) { p.Tiers[0].Max = 7550 },
	} {
		p := valid
		p.Tiers = append([]Tier(nil), valid.Tiers...)
		change(&p)
		if Validate(p) == nil {
			t.Fatalf("invalid input accepted: %+v", p)
		}
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip Input
	if err = json.Unmarshal(raw, &roundtrip); err != nil || roundtrip.Tax != 500 {
		t.Fatalf("tax input: %+v %v", roundtrip, err)
	}
	for _, bad := range [][]byte{
		bytes.Replace(raw, []byte(`"taxPercent":5.00`), []byte(`"taxPercent":null`), 1),
		bytes.Replace(raw, []byte(`"taxPercent":5.00`), []byte(`"taxPercent":5.001`), 1),
		bytes.Replace(raw, []byte(`,"taxPercent":5.00`), nil, 1),
	} {
		if json.Unmarshal(bad, &roundtrip) == nil {
			t.Fatalf("invalid tax input accepted: %s", bad)
		}
	}
	if (LinkRate{EffectiveBps: 0}).Factor() != "0.00" || (LinkRate{EffectiveBps: 10000}).Factor() != "1.00" {
		t.Fatal("factor format")
	}
	old := Select(&Policy{ID: "legacy", Tax: 500, Tiers: []Tier{{Code:"bronze",MinGold:0,Min:5000,Max:5000}}}, 0).Public()
	if old.PreviewAvailable {
		t.Fatal("invalid legacy policy forecast offered")
	}
}
