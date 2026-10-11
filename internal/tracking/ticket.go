// Package tracking carries an authenticated policy snapshot in five Shopee Sub IDs.
package tracking

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
	"time"
)

const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
const Lifetime = 5 * 24 * time.Hour
const CurrentVersion uint8 = 4

// FormatID also invalidates verification proofs for previous wire layouts.
const FormatID = "v4-compact-1"
const minEnvelopeSize, maxEnvelopeSize = 56, 91

var ErrInvalid = errors.New("Tracking v4 không hợp lệ hoặc sai xác thực")

// KeyError is operational and retryable. Importers must never classify it as
// ineligible attribution, including when an old key is temporarily unavailable.
type KeyError struct{ ID uint8 }

func (e *KeyError) Error() string {
	if e.ID == 0 {
		return "Tracking keys are not configured"
	}
	return fmt.Sprintf("Tracking key ID %d is not configured", e.ID)
}

type Codec struct {
	keys   map[uint8]cipher.AEAD
	active uint8
}

// NewCodec allows an entirely absent configuration to leave tracking disabled.
// Partial or malformed configuration fails startup, without echoing key material.
func NewCodec(raw, active string) (*Codec, error) {
	if raw == "" && active == "" {
		return nil, nil
	}
	fail := errors.New("TRACKING_KEYS must map canonical IDs 1..255 to base64 32-byte keys; TRACKING_ACTIVE_KEY_ID must name a configured key")
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fail
	}
	c := &Codec{keys: make(map[uint8]cipher.AEAD)}
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, fail
		}
		name, ok := token.(string)
		if !ok {
			return nil, fail
		}
		n, err := strconv.ParseUint(name, 10, 8)
		if err != nil || n == 0 || strconv.FormatUint(n, 10) != name {
			return nil, fail
		}
		id := uint8(n)
		if _, exists := c.keys[id]; exists {
			return nil, fail
		}
		var value string
		if decoder.Decode(&value) != nil {
			return nil, fail
		}
		key, err := base64.StdEncoding.Strict().DecodeString(value)
		if err != nil || len(key) != 32 {
			return nil, fail
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			return nil, fail
		}
		aead, err := cipher.NewGCMWithRandomNonce(block)
		if err != nil {
			return nil, fail
		}
		c.keys[id] = aead
	}
	if _, err = decoder.Token(); err != nil {
		return nil, fail
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, fail
	}
	n, err := strconv.ParseUint(active, 10, 8)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != active || c.keys[uint8(n)] == nil {
		return nil, fail
	}
	c.active = uint8(n)
	return c, nil
}
func (c *Codec) Ready() error {
	if c == nil || c.keys[c.active] == nil {
		return &KeyError{}
	}
	return nil
}

type Claims struct {
	Version             uint8
	Customer, Publisher string
	CreatedAt           time.Time
	Shop, Item          uint64
	Policy              uint32
	EffectiveBps        int
}

// ExpiresAt is solely derived metadata for saved-link retention. It is not
// carried in tracking and never limits late report attribution.
func (c Claims) ExpiresAt() time.Time       { return c.CreatedAt.Add(Lifetime) }
func (c Claims) Eligible(at time.Time) bool { return !at.Before(c.CreatedAt) }
func uuidBytes(s string) ([]byte, error) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return nil, ErrInvalid
	}
	b, err := hex.DecodeString(strings.ReplaceAll(s, "-", ""))
	if err != nil || len(b) != 16 || new(big.Int).SetBytes(b).Sign() == 0 {
		return nil, ErrInvalid
	}
	return b, nil
}
func uuidString(b []byte) string {
	s := hex.EncodeToString(b)
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}
func (c Claims) validate() error {
	if c.Version != CurrentVersion || c.Shop == 0 || c.Item == 0 || c.Policy == 0 || c.CreatedAt.Unix() < 0 || c.CreatedAt.Unix() > 4294967295 || c.CreatedAt.Nanosecond() != 0 || c.EffectiveBps < 0 || c.EffectiveBps > 10000 || c.EffectiveBps%100 != 0 {
		return ErrInvalid
	}
	if _, err := uuidBytes(c.Customer); err != nil {
		return err
	}
	if len(c.Publisher) < 1 || len(c.Publisher) > 32 || strings.Trim(c.Publisher, "0123456789") != "" {
		return ErrInvalid
	}
	return nil
}

// Decimal digit count preserves leading zeros. Even 32 decimal digits need
// only 14 numeric bytes, instead of 32 ASCII bytes.
func publisherWidth(digits int) int {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)
	limit.Sub(limit, big.NewInt(1))
	return (limit.BitLen() + 7) / 8
}
func marshal(c Claims) ([]byte, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	customer, _ := uuidBytes(c.Customer)
	b := make([]byte, 21)
	copy(b, customer)
	binary.BigEndian.PutUint32(b[16:20], uint32(c.CreatedAt.Unix()))
	b[20] = byte(c.EffectiveBps / 100)
	b = binary.AppendUvarint(b, uint64(c.Policy))
	b = binary.AppendUvarint(b, c.Shop)
	b = binary.AppendUvarint(b, c.Item)
	b = append(b, byte(len(c.Publisher)))
	width := publisherWidth(len(c.Publisher))
	numeric, _ := new(big.Int).SetString(c.Publisher, 10)
	packed := make([]byte, width)
	numeric.FillBytes(packed)
	return append(b, packed...), nil
}
func unmarshal(b []byte) (Claims, error) {
	var c Claims
	if len(b) < 26 || len(b) > 61 {
		return c, ErrInvalid
	}
	c = Claims{Version: CurrentVersion, Customer: uuidString(b[:16]), CreatedAt: time.Unix(int64(binary.BigEndian.Uint32(b[16:20])), 0).UTC(), EffectiveBps: int(b[20]) * 100}
	remaining := b[21:]
	next := func() (uint64, error) {
		n, count := binary.Uvarint(remaining)
		if count <= 0 {
			return 0, ErrInvalid
		}
		remaining = remaining[count:]
		return n, nil
	}
	policy, err := next()
	if err != nil || policy > 4294967295 {
		return Claims{}, ErrInvalid
	}
	c.Policy = uint32(policy)
	if c.Shop, err = next(); err != nil {
		return Claims{}, err
	}
	if c.Item, err = next(); err != nil {
		return Claims{}, err
	}
	if len(remaining) < 2 || remaining[0] < 1 || remaining[0] > 32 {
		return Claims{}, ErrInvalid
	}
	digits := int(remaining[0])
	width := publisherWidth(digits)
	if len(remaining) != width+1 {
		return Claims{}, ErrInvalid
	}
	publisher := new(big.Int).SetBytes(remaining[1:]).String()
	if len(publisher) > digits {
		return Claims{}, ErrInvalid
	}
	c.Publisher = strings.Repeat("0", digits-len(publisher)) + publisher
	canonical, err := marshal(c)
	if err != nil || !bytes.Equal(canonical, b) {
		return Claims{}, ErrInvalid
	}
	return c, nil
}
func aad(header []byte) []byte {
	return append([]byte("hoanxu:shopee-tracking:"+FormatID+":"), header...)
}
func partLength(total, index int) int {
	n := total / 5
	if index < total%5 {
		n++
	}
	return n
}
func split(packet []byte) [5]string {
	var ids [5]string
	body := encode(packet)
	offset := 0
	for i := range ids {
		n := partLength(len(body), i)
		ids[i] = fmt.Sprintf("HX4%d", i+1) + body[offset:offset+n]
		offset += n
	}
	return ids
}
func (c *Codec) Issue(claims Claims) ([5]string, error) {
	var ids [5]string
	if err := c.Ready(); err != nil {
		return ids, err
	}
	if claims.Version == 0 {
		claims.Version = CurrentVersion
	}
	b, err := marshal(claims)
	if err != nil {
		return ids, err
	}
	header := []byte{CurrentVersion, c.active}
	packet := append(header, c.keys[c.active].Seal(nil, nil, b, aad(header))...)
	return split(packet), nil
}
func encode(b []byte) string {
	n := new(big.Int).SetBytes(b)
	base := big.NewInt(62)
	rem := new(big.Int)
	s := ""
	for n.Sign() > 0 {
		n.QuoRem(n, base, rem)
		s = string(alphabet[rem.Int64()]) + s
	}
	return s
}
func envelope(ids [5]string) ([]byte, error) {
	var body strings.Builder
	for i, id := range ids {
		if len(id) < 5 || len(id) > 29 || id[:4] != fmt.Sprintf("HX4%d", i+1) {
			return nil, ErrInvalid
		}
		body.WriteString(id[4:])
	}
	encoded := body.String()
	if len(encoded) < 75 || len(encoded) > 123 {
		return nil, ErrInvalid
	}
	for i, id := range ids {
		if len(id) != 4+partLength(len(encoded), i) {
			return nil, ErrInvalid
		}
	}
	n := new(big.Int)
	base := big.NewInt(62)
	for _, r := range encoded {
		index := strings.IndexRune(alphabet, r)
		if index < 0 {
			return nil, ErrInvalid
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(index)))
	}
	packet := n.Bytes()
	if len(packet) < minEnvelopeSize || len(packet) > maxEnvelopeSize || packet[0] != CurrentVersion || packet[1] == 0 || encode(packet) != encoded {
		return nil, ErrInvalid
	}
	return packet, nil
}

// Code identifies the entire encrypted envelope and exposes no business field.
func Code(ids [5]string) (string, error) {
	packet, err := envelope(ids)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(packet)
	return hex.EncodeToString(sum[:]), nil
}
func (c *Codec) Decode(ids [5]string) (Claims, error) {
	packet, err := envelope(ids)
	if err != nil {
		return Claims{}, err
	}
	if c == nil || c.keys[packet[1]] == nil {
		return Claims{}, &KeyError{ID: packet[1]}
	}
	plain, err := c.keys[packet[1]].Open(nil, nil, packet[2:], aad(packet[:2]))
	if err != nil {
		return Claims{}, ErrInvalid
	}
	return unmarshal(plain)
}
