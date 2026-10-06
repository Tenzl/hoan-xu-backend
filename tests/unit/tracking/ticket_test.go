package tracking

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"
)

func TestTicketRoundtripTamperingAndExpiry(t *testing.T) {
	sign := func(publisher string, ids [4]string) string {
		h := hmac.New(sha256.New, []byte("test-key"))
		h.Write([]byte(publisher))
		for _, id := range ids {
			h.Write([]byte{0})
			h.Write([]byte(id))
		}
		return hex.EncodeToString(h.Sum(nil)[:16])
	}
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	c := Claims{CreatedAt: now, Shop: 83496725, Item: 6939920023, Policy: 9, Tier: "bronze", Bps: 6500}
	ids, err := Issue(c, "customer", "publisher", "0.63", sign)
	if err != nil || len(ids[2]) != 49 || len(ids[4]) != 32 || ids[3] != "0p63" {
		t.Fatal(ids, err)
	}
	got, err := Verify(ids, "publisher", sign)
	if err != nil || got.Shop != c.Shop || got.Item != c.Item || got.Policy != c.Policy || got.Bps != c.Bps || !got.CreatedAt.Equal(now) {
		t.Fatal(got, err)
	}
	for i := range ids {
		bad := ids
		bad[i] += "1"
		if _, err := Verify(bad, "publisher", sign); err == nil {
			t.Fatalf("accepted modified subid %d", i+1)
		}
	}
	if _, err := Verify(ids, "other", sign); err == nil {
		t.Fatal("accepted other publisher")
	}
	for _, tc := range []struct {
		at    time.Time
		valid bool
	}{{now.Add(-time.Second), false}, {now, true}, {now.Add(6*24*time.Hour - time.Second), true}, {now.Add(6 * 24 * time.Hour), false}, {now.Add(7 * 24 * time.Hour), false}} {
		if got.Eligible(tc.at) != tc.valid {
			t.Fatal(tc)
		}
	}
	other, _ := Issue(c, "customer", "publisher", "0.63", sign)
	if other[2] == ids[2] {
		t.Fatal("nonce reused")
	}
}

func TestFactorTransportIsCanonicalAndAuthenticated(t *testing.T) {
	for _, tc := range []struct{ decimal, encoded string }{{"0.00", "0p00"}, {"0.01", "0p01"}, {"0.63", "0p63"}, {"0.99", "0p99"}, {"1.00", "1p00"}} {
		if !MatchesFactor(tc.encoded, tc.decimal) {
			t.Fatal(tc)
		}
		for _, bad := range []string{tc.decimal, "063", "0p630", "0P63", "0p100", "1p01", "0p6"} {
			if MatchesFactor(bad, tc.decimal) {
				t.Fatal("noncanonical factor accepted", bad)
			}
		}
	}
	if MatchesFactor("0p63", "0.630") || MatchesFactor("0p63", "1.63") {
		t.Fatal("invalid decimal factor accepted")
	}
}

func TestTrackingVersionsRetainTheirOwnDeadline(t *testing.T) {
	created := time.Now().UTC().Truncate(time.Second)
	sign := func(_ string, _ [4]string) string { return "01234567890123456789012345678901" }
	for _, version := range []uint8{1, 2} {
		c := Claims{Version: version, CreatedAt: created, Shop: 1, Item: 2, Policy: 1, Tier: "bronze", Bps: 6600}
		ids, err := Issue(c, "customer", "publisher", "0.63", sign)
		if err != nil {
			t.Fatal(err)
		}
		got, err := Verify(ids, "publisher", sign)
		if err != nil || got.Version != version {
			t.Fatal(got, err)
		}
		days := 6
		if version == 1 {
			days = 7
		}
		expiry := created.Add(time.Duration(days) * 24 * time.Hour)
		if !got.ExpiresAt().Equal(expiry) || !got.Eligible(expiry.Add(-time.Second)) || got.Eligible(expiry) {
			t.Fatal(got)
		}
	}
}
