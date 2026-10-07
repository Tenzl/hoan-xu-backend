package cashback

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
)

// LinkRate is calculated once per new link. Existing orders keep their stored rate.
type LinkRate struct{ MainBps, TaxBps, EffectiveBps int }

// AmountRoundedUp uses the same bounds as the legacy floor calculation, but
// credits the next whole Xu whenever a signed link leaves a fractional Xu.
func AmountRoundedUp(commission int64, bps int) (int64, error) {
	amount, err := Amount(commission, bps)
	if err != nil {
		return 0, err
	}
	if commission*int64(bps)%10000 != 0 {
		amount++
	}
	return amount, nil
}

// Completed legacy orders retain their original rounding rule.
func OrderAmount(commission int64, bps int, mode string) (int64, error) {
	switch mode {
	case "signed_link":
		return AmountRoundedUp(commission, bps)
	case "commission_share":
		return Amount(commission, bps)
	default:
		return 0, fmt.Errorf("invalid cashback mode")
	}
}

func ValidateLinkRange(min, max int) error {
	if min < 0 || max > 10000 || min%100 != 0 || max%100 != 0 || max-min < 500 {
		return fmt.Errorf("invalid whole-percent cashback range")
	}
	return nil
}

func adjustedPercent(n, min, max int) int {
	if n == min {
		return n + 1
	}
	if n == max {
		return n - 4
	}
	return n
}

// EffectiveRate rounds UP the fractional coefficient to hundredths, using
// integer arithmetic. 6600 bps with 500 bps tax gives 0.63 (6300 bps).
func EffectiveRate(main, tax int) (int, error) {
	if main < 0 || main > 10000 || tax < 0 || tax > 10000 {
		return 0, fmt.Errorf("invalid cashback rate")
	}
	return (main*(10000-tax) + 999999) / 1000000 * 100, nil
}

func (r LinkRate) Factor() string {
	return fmt.Sprintf("%d.%02d", r.EffectiveBps/10000, (r.EffectiveBps%10000)/100)
}

func SampleLink(source io.Reader, min, max, tax int) (LinkRate, error) {
	if err := ValidateLinkRange(min, max); err != nil {
		return LinkRate{}, err
	}
	if tax < 0 || tax > 10000 {
		return LinkRate{}, fmt.Errorf("invalid cashback rate")
	}
	if source == nil {
		source = rand.Reader
	}
	n, err := rand.Int(source, big.NewInt(int64((max-min)/100+1)))
	if err != nil {
		return LinkRate{}, err
	}
	main := 100 * adjustedPercent(min/100+int(n.Int64()), min/100, max/100)
	effective, err := EffectiveRate(main, tax)
	return LinkRate{main, tax, effective}, err
}

type PublicTier struct {
	Tier
	EffectiveMin     Percent `json:"effectiveMinSharePercent"`
	EffectiveMax     Percent `json:"effectiveMaxSharePercent"`
	PreviewAvailable bool    `json:"previewAvailable"`
}
type PublicMembership struct {
	PolicyID string `json:"policyId"`
	PublicTier
	MembershipProgress
	NextTier *PublicTier `json:"nextTier"`
}

func publicTier(t Tier, tax int) PublicTier {
	p := PublicTier{Tier: t}
	p.Min, p.Max = 0, 0
	if ValidateLinkRange(int(t.Min), int(t.Max)) != nil {
		return p
	}
	min, max := 10000, 0
	// Enumerate at most 101 draws: transformed endpoints are not the range extrema.
	for n := int(t.Min) / 100; n <= int(t.Max)/100; n++ {
		effective, err := EffectiveRate(100*adjustedPercent(n, int(t.Min)/100, int(t.Max)/100), tax)
		if err != nil {
			return p
		}
		if effective < min {
			min = effective
		}
		if effective > max {
			max = effective
		}
	}
	p.Min, p.Max = Percent(min), Percent(max)
	p.EffectiveMin, p.EffectiveMax, p.PreviewAvailable = p.Min, p.Max, true
	return p
}
func (m Membership) Public() PublicMembership {
	p := PublicMembership{PolicyID: m.PolicyID, PublicTier: publicTier(m.Tier, int(m.Tax)), MembershipProgress: m.MembershipProgress}
	if m.NextTier != nil {
		next := publicTier(*m.NextTier, int(m.Tax))
		p.NextTier = &next
	}
	return p
}

func (p *Input) UnmarshalJSON(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	value, ok := fields["taxPercent"]
	if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fmt.Errorf("missing tax percentage")
	}
	type plain Input
	var input plain
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return err
	}
	*p = Input(input)
	return nil
}
