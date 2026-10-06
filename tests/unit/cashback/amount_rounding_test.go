package cashback

import "testing"

func TestSignedCashbackRoundsUpWithoutChangingLegacyAmounts(t *testing.T) {
	for _, tc := range []struct {
		commission int64
		bps        int
		want       int64
	}{
		{10001, 6300, 6301}, {5001, 6300, 3151}, {10000, 6300, 6300}, {0, 6300, 0}, {10001, 0, 0}, {1, 1, 1}, {999, 3333, 333}, {1000000000000, 10000, 1000000000000},
	} {
		got, err := AmountRoundedUp(tc.commission, tc.bps)
		if err != nil || got != tc.want {
			t.Fatalf("%d * %d: %d want %d (%v)", tc.commission, tc.bps, got, tc.want, err)
		}
	}
	if got, err := OrderAmount(10001, 6300, "signed_link"); err != nil || got != 6301 {
		t.Fatal(got, err)
	}
	if got, err := OrderAmount(10001, 6300, "commission_share"); err != nil || got != 6300 {
		t.Fatal("legacy rounding changed", got, err)
	}
	if _, err := OrderAmount(10001, 6300, "unknown"); err == nil {
		t.Fatal("unknown mode accepted")
	}
	for _, tc := range []struct {
		commission int64
		bps        int
	}{{-1, 6300}, {1000000000001, 6300}, {1, -1}, {1, 10001}} {
		if _, err := AmountRoundedUp(tc.commission, tc.bps); err == nil {
			t.Fatal("invalid input", tc)
		}
	}
}
