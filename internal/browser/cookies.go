package browser

import (
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
)

const MaxCookieBytes = 64 * 1024

var ErrInvalidCookies = errors.New("INVALID_SHOPEE_COOKIES")
var ErrCookieStorage = errors.New("COOKIE_STORAGE_FAILED")
var cookieName = regexp.MustCompile(`^[A-Za-z0-9_!#$%&'*+.^~|-]+$`)

type exportedCookie struct {
	Name           string  `json:"name"`
	Value          string  `json:"value"`
	Domain         string  `json:"domain"`
	Path           string  `json:"path"`
	Secure         bool    `json:"secure"`
	HTTPOnly       bool    `json:"httpOnly"`
	HostOnly       bool    `json:"hostOnly"`
	SameSite       string  `json:"sameSite"`
	Expires        float64 `json:"expires"`
	ExpirationDate float64 `json:"expirationDate"`
	Session        bool    `json:"session"`
}

// Accept a Cookie header, JSON array, or {url,cookies} affiliate export.
// Never include supplied names/values in validation errors.
func ParseCookies(raw string) ([]*network.CookieParam, error) {
	raw = strings.TrimSpace(raw)
	if len(raw) == 0 || len(raw) > MaxCookieBytes || !utf8.ValidString(raw) {
		return nil, ErrInvalidCookies
	}
	var input []exportedCookie
	if strings.HasPrefix(raw, "[") {
		if json.Unmarshal([]byte(raw), &input) != nil {
			return nil, ErrInvalidCookies
		}
	} else if strings.HasPrefix(raw, "{") {
		var exported struct {
			URL     string           `json:"url"`
			Cookies []exportedCookie `json:"cookies"`
		}
		if json.Unmarshal([]byte(raw), &exported) != nil {
			return nil, ErrInvalidCookies
		}
		origin, err := url.Parse(exported.URL)
		if err != nil || origin.Scheme != "https" || !strings.EqualFold(origin.Host, "affiliate.shopee.vn") || origin.User != nil {
			return nil, ErrInvalidCookies
		}
		input = exported.Cookies
	} else {
		if strings.ContainsAny(raw, "\r\n\x00") {
			return nil, ErrInvalidCookies
		}
		if strings.HasPrefix(strings.ToLower(raw), "cookie:") {
			raw = strings.TrimSpace(raw[len("cookie:"):])
		}
		for _, part := range strings.Split(raw, ";") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			name, value, ok := strings.Cut(part, "=")
			if !ok {
				return nil, ErrInvalidCookies
			}
			input = append(input, exportedCookie{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value), Domain: ".shopee.vn", Path: "/", Secure: true, HTTPOnly: true})
		}
	}
	if len(input) == 0 || len(input) > 100 {
		return nil, ErrInvalidCookies
	}
	seen := map[string]bool{}
	out := make([]*network.CookieParam, 0, len(input))
	for _, v := range input {
		if !cookieName.MatchString(v.Name) || len(v.Name) > 256 || len(v.Name)+len(v.Value) > 4096 {
			return nil, ErrInvalidCookies
		}
		for _, r := range v.Value {
			if r < 0x21 || r > 0x7e || r == ';' {
				return nil, ErrInvalidCookies
			}
		}
		if v.Domain == "" {
			v.Domain = ".shopee.vn"
		}
		v.Domain = strings.ToLower(v.Domain)
		host := strings.TrimPrefix(strings.ToLower(v.Domain), ".")
		if host != "shopee.vn" && host != "affiliate.shopee.vn" {
			return nil, ErrInvalidCookies
		}
		if v.Path == "" {
			v.Path = "/"
		}
		if !strings.HasPrefix(v.Path, "/") || len(v.Path) > 512 || strings.ContainsAny(v.Path, "\r\n\x00") {
			return nil, ErrInvalidCookies
		}
		key := v.Name + "|" + v.Domain + "|" + v.Path
		if seen[key] {
			return nil, ErrInvalidCookies
		}
		seen[key] = true
		c := &network.CookieParam{Name: v.Name, Value: v.Value, Domain: strings.ToLower(v.Domain), Path: v.Path, Secure: true, HTTPOnly: v.HTTPOnly}
		if v.HostOnly || strings.HasPrefix(v.Name, "__Host-") {
			c.Domain = ""
			c.URL = "https://" + host + v.Path
		}
		if strings.HasPrefix(v.Name, "__Host-") && v.Path != "/" {
			return nil, ErrInvalidCookies
		}
		switch strings.ToLower(v.SameSite) {
		case "", "unspecified", "lax":
			c.SameSite = network.CookieSameSiteLax
		case "strict":
			c.SameSite = network.CookieSameSiteStrict
		case "none", "no_restriction":
			c.SameSite = network.CookieSameSiteNone
		default:
			return nil, ErrInvalidCookies
		}
		expiry := v.ExpirationDate
		if expiry == 0 {
			expiry = v.Expires
		}
		if !v.Session && expiry > 0 {
			if math.IsNaN(expiry) || math.IsInf(expiry, 0) || expiry > 32503680000 {
				return nil, ErrInvalidCookies
			}
			t := cdp.TimeSinceEpoch(time.Unix(int64(expiry), 0))
			c.Expires = &t
		}
		out = append(out, c)
	}
	return out, nil
}

type SecretCodec interface {
	Encrypt(string) string
	Decrypt(string) (string, error)
}
