package tracking

import (
	"crypto/hmac"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math/big"
	"regexp"
	"strings"
	"time"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
const Lifetime = 6 * 24 * time.Hour
const CurrentVersion uint8 = 2

type Signer func(string, [4]string) string
type Claims struct {
	Version    uint8
	CreatedAt  time.Time
	Shop, Item uint64
	Policy     uint32
	Tier       string
	Bps        int
}

func (c Claims) ExpiresAt() time.Time {
	if c.Version == 1 {
		return c.CreatedAt.Add(7 * 24 * time.Hour)
	}
	return c.CreatedAt.Add(Lifetime)
}
func (c Claims) Eligible(at time.Time) bool {
	return !at.Before(c.CreatedAt) && at.Before(c.ExpiresAt())
}

var factorFormat = regexp.MustCompile(`^(0\.[0-9]{2}|1\.00)$`)
var factorSubIDFormat = regexp.MustCompile(`^(0p[0-9]{2}|1p00)$`)
var tiers = []string{"bronze", "platinum", "diamond"}

// MatchesFactor compares the Shopee-safe carrier with the decimal policy factor.
// The HMAC authenticates the exact carrier (e.g. 0p63), never a normalized input.
func MatchesFactor(encoded, decimal string) bool {
	return factorFormat.MatchString(decimal) && encoded == strings.Replace(decimal, ".", "p", 1)
}

func Issue(c Claims, customer, publisher string, factor string, sign Signer) ([5]string, error) {
	var ids [5]string
	tier := -1
	for i, t := range tiers {
		if t == c.Tier {
			tier = i
		}
	}
	if c.CreatedAt.Unix() < 0 || c.CreatedAt.Unix() > 4294967295 || c.Shop == 0 || c.Item == 0 || c.Policy == 0 || tier < 0 || c.Bps < 0 || c.Bps > 10000 || customer == "" || len(customer) > 50 || publisher == "" || !factorFormat.MatchString(factor) {
		return ids, errors.New("invalid tracking claims")
	}
	b := make([]byte, 36)
	version := c.Version
	if version == 0 {
		version = CurrentVersion
	}
	if version != 1 && version != CurrentVersion {
		return ids, errors.New("invalid tracking version")
	}
	b[0] = version
	binary.BigEndian.PutUint32(b[1:5], uint32(c.CreatedAt.Unix()))
	if _, err := rand.Read(b[5:13]); err != nil {
		return ids, err
	}
	binary.BigEndian.PutUint64(b[13:21], c.Shop)
	binary.BigEndian.PutUint64(b[21:29], c.Item)
	binary.BigEndian.PutUint32(b[29:33], c.Policy)
	b[33] = byte(tier)
	binary.BigEndian.PutUint16(b[34:], uint16(c.Bps))
	n := new(big.Int).SetBytes(b)
	base := big.NewInt(62)
	rem := new(big.Int)
	encoded := ""
	for n.Sign() > 0 {
		n.QuoRem(n, base, rem)
		encoded = string(alphabet[rem.Int64()]) + encoded
	}
	ids = [5]string{customer, "hoanxu", strings.Repeat("0", 49-len(encoded)) + encoded, strings.Replace(factor, ".", "p", 1), ""}
	ids[4] = sign(publisher, [4]string{ids[0], ids[1], ids[2], ids[3]})
	return ids, nil
}
func Verify(ids [5]string, publisher string, sign Signer) (Claims, error) {
	fail := errors.New("Tracking không hợp lệ hoặc sai chữ ký")
	var c Claims
	for _, id := range ids {
		if len(id) == 0 || len(id) > 50 {
			return c, fail
		}
	}
	if ids[1] != "hoanxu" || len(ids[2]) != 49 || len(ids[4]) != 32 || publisher == "" {
		return c, fail
	}
	expected := sign(publisher, [4]string{ids[0], ids[1], ids[2], ids[3]})
	a, e := hex.DecodeString(ids[4])
	b, _ := hex.DecodeString(expected)
	if e != nil || !hmac.Equal(a, b) || ids[4] != strings.ToLower(ids[4]) {
		return c, fail
	}
	if !factorSubIDFormat.MatchString(ids[3]) {
		return c, fail
	}
	n := new(big.Int)
	for _, r := range ids[2] {
		i := strings.IndexRune(alphabet, r)
		if i < 0 {
			return c, fail
		}
		n.Mul(n, big.NewInt(62))
		n.Add(n, big.NewInt(int64(i)))
	}
	if n.BitLen() > 288 {
		return c, fail
	}
	packet := make([]byte, 36)
	n.FillBytes(packet)
	if (packet[0] != 1 && packet[0] != CurrentVersion) || int(packet[33]) >= len(tiers) {
		return c, fail
	}
	c = Claims{Version: packet[0], CreatedAt: time.Unix(int64(binary.BigEndian.Uint32(packet[1:5])), 0).UTC(), Shop: binary.BigEndian.Uint64(packet[13:21]), Item: binary.BigEndian.Uint64(packet[21:29]), Policy: binary.BigEndian.Uint32(packet[29:33]), Tier: tiers[int(packet[33])], Bps: int(binary.BigEndian.Uint16(packet[34:]))}
	if c.Shop == 0 || c.Item == 0 || c.Policy == 0 || c.Bps > 10000 {
		return Claims{}, fail
	}
	return c, nil
}
