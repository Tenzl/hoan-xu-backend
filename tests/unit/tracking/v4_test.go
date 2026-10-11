package tracking

import (
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func testCodec(t *testing.T, id string, key byte) *Codec {
	t.Helper()
	c, err := NewCodec(`{"`+id+`":"`+base64.StdEncoding.EncodeToString([]byte(strings.Repeat(string(key), 32)))+`"}`, id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
func exampleClaims() Claims {
	return Claims{Version: 4, Customer: "12345678-1234-4321-8123-123456789012", Publisher: "00123456789012345678901234567890", CreatedAt: time.Unix(1700000000, 0).UTC(), Shop: 1, Item: ^uint64(0), Policy: ^uint32(0), EffectiveBps: 6300}
}
func TestV4RoundTripAndPrivacy(t *testing.T) {
	codec := testCodec(t, "255", 'a')
	c := exampleClaims()
	ids, err := codec.Issue(c)
	if err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		if len(id) > 29 || !strings.HasPrefix(id, "HX4"+string(rune('1'+i))) {
			t.Fatal("invalid carrier length/prefix")
		}
	}
	got, err := codec.Decode(ids)
	if err != nil || !reflect.DeepEqual(c, got) {
		t.Fatal(got, err)
	}
	code, err := Code(ids)
	if err != nil || len(code) != 64 {
		t.Fatal(code, err)
	}
	other, err := codec.Issue(c)
	if err != nil || other == ids {
		t.Fatal("random nonce missing", err)
	}
	otherCode, _ := Code(other)
	if code == otherCode {
		t.Fatal("duplicate digest")
	}
	if !got.ExpiresAt().Equal(c.CreatedAt.Add(Lifetime)) || got.Eligible(c.CreatedAt.Add(-time.Second)) || !got.Eligible(c.CreatedAt.Add(365*24*time.Hour)) {
		t.Fatal("retention changed attribution")
	}
	for _, publisher := range []string{"0", "000", "123456789", strings.Repeat("9", 32)} {
		for _, bps := range []int{0, 10000} {
			v := c
			v.Publisher = publisher
			v.EffectiveBps = bps
			v.CreatedAt = time.Unix(4294967295, 0).UTC()
			ids, err := codec.Issue(v)
			if err != nil {
				t.Fatal(err)
			}
			got, err := codec.Decode(ids)
			if err != nil || !reflect.DeepEqual(v, got) {
				t.Fatal(got, err)
			}
		}
	}
}

func TestCompactTrackingLength(t *testing.T) {
	c := exampleClaims()
	c.Publisher = "123456789"
	c.Shop = 83496725
	c.Item = 6939920023
	c.Policy = 1
	ids, err := testCodec(t, "1", 'a').Issue(c)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, id := range ids {
		total += len(id)
		if len(id) > 23 {
			t.Fatal("carrier is not compact", len(id))
		}
	}
	if total > 110 {
		t.Fatal("tracking is not compact", total)
	}
	t.Logf("Sub ID lengths: %d/%d/%d/%d/%d; total %d", len(ids[0]), len(ids[1]), len(ids[2]), len(ids[3]), len(ids[4]), total)
}

func TestV4RejectsNonCanonicalPayloads(t *testing.T) {
	codec := testCodec(t, "1", 'a')
	claims := exampleClaims()
	claims.Policy, claims.Shop, claims.Item, claims.Publisher = 1, 1, 1, "0"
	plain, err := marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	overlong := append([]byte(nil), plain[:21]...)
	overlong = append(overlong, 0x81, 0)
	overlong = append(overlong, plain[22:]...)
	badCoefficient := append([]byte(nil), plain...)
	badCoefficient[20] = 101
	badDigits := append([]byte(nil), plain...)
	badDigits[24] = 33
	for _, bad := range [][]byte{plain[:len(plain)-1], append(append([]byte(nil), plain...), 0), overlong, badCoefficient, badDigits} {
		header := []byte{CurrentVersion, 1}
		packet := append(header, codec.keys[1].Seal(nil, nil, bad, aad(header))...)
		if _, err := codec.Decode(split(packet)); err == nil {
			t.Fatal("accepted malformed authenticated payload")
		}
	}
	claims.Shop, claims.Item, claims.Policy, claims.Publisher = ^uint64(0), ^uint64(0), ^uint32(0), strings.Repeat("9", 32)
	ids, err := codec.Issue(claims)
	if err != nil {
		t.Fatal(err)
	}
	got, err := codec.Decode(ids)
	if err != nil || !reflect.DeepEqual(got, claims) {
		t.Fatal(got, err)
	}
	for _, id := range ids {
		if len(id) > 29 {
			t.Fatal("maximum carrier exceeded", len(id))
		}
	}
}
func TestV4RejectsTamperingMixingAndOldFormats(t *testing.T) {
	codec := testCodec(t, "1", 'a')
	ids, _ := codec.Issue(exampleClaims())
	other, _ := codec.Issue(exampleClaims())
	for i := range ids {
		bad := ids
		last := bad[i][len(bad[i])-1]
		replacement := byte('0')
		if last == '0' {
			replacement = '1'
		}
		bad[i] = bad[i][:len(bad[i])-1] + string(replacement)
		if _, err := codec.Decode(bad); err == nil {
			t.Fatal("accepted modified part", i)
		}
		bad = ids
		bad[i] = ""
		if _, err := codec.Decode(bad); err == nil {
			t.Fatal("accepted missing part", i)
		}
		bad = ids
		bad[i] = other[i]
		if _, err := codec.Decode(bad); err == nil {
			t.Fatal("accepted mixed part", i)
		}
	}
	bad := ids
	bad[0], bad[1] = bad[1], bad[0]
	if _, err := codec.Decode(bad); err == nil {
		t.Fatal("accepted swapped parts")
	}
	for _, bad := range [][5]string{{"customer", "hoanxu", strings.Repeat("a", 49), "0p63", strings.Repeat("a", 32)}, {"HX41" + strings.Repeat("z", 37), "HX42" + strings.Repeat("z", 37), "HX43" + strings.Repeat("z", 36), "HX44" + strings.Repeat("z", 36), "HX45" + strings.Repeat("z", 36)}} {
		if _, err := codec.Decode(bad); err == nil {
			t.Fatal("accepted invalid envelope")
		}
	}
	if _, err := testCodec(t, "1", 'b').Decode(ids); err == nil {
		t.Fatal("accepted wrong key")
	}
	_, err := testCodec(t, "2", 'b').Decode(ids)
	var missing *KeyError
	if !errors.As(err, &missing) {
		t.Fatal("missing key must remain retryable", err)
	}
	var disabled *Codec
	if _, err := disabled.Issue(exampleClaims()); !errors.As(err, &missing) {
		t.Fatal(err)
	}
}
func TestV4KeyConfigAndInvalidClaims(t *testing.T) {
	for _, raw := range []string{`{}`, `{"0":"x"}`, `{"256":"x"}`, `{"01":"x"}`, `{"1":"x"}`, `{"1":null}`, `[]`} {
		if _, err := NewCodec(raw, "1"); err == nil {
			t.Fatal("invalid key config accepted")
		}
	}
	codec := testCodec(t, "1", 'a')
	for _, change := range []func(*Claims){func(c *Claims) { c.Customer = "customer" }, func(c *Claims) { c.Customer = "00000000-0000-0000-0000-000000000000" }, func(c *Claims) { c.Publisher = "fixture" }, func(c *Claims) { c.Publisher = strings.Repeat("1", 33) }, func(c *Claims) { c.EffectiveBps = 6301 }, func(c *Claims) { c.EffectiveBps = -100 }, func(c *Claims) { c.EffectiveBps = 10100 }, func(c *Claims) { c.Policy = 0 }, func(c *Claims) { c.Version = 3 }, func(c *Claims) { c.Shop = 0 }, func(c *Claims) { c.Item = 0 }, func(c *Claims) { c.CreatedAt = time.Unix(4294967296, 0) }, func(c *Claims) { c.CreatedAt = time.Unix(-1, 0) }} {
		c := exampleClaims()
		change(&c)
		if _, err := codec.Issue(c); err == nil {
			t.Fatal("invalid claims accepted")
		}
	}

	old := testCodec(t, "1", 'a')
	ids, _ := old.Issue(exampleClaims())
	rotated, err := NewCodec(`{"1":"`+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))+`","2":"`+base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))+`"}`, "2")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = rotated.Decode(ids); err != nil {
		t.Fatal("old retained key rejected", err)
	}
	newIDs, _ := rotated.Issue(exampleClaims())
	if _, err = old.Decode(newIDs); err == nil {
		t.Fatal("active key was not rotated")
	}
}
