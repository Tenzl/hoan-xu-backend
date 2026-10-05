package browser

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
type CookieStore struct {
	Path  string
	Codec SecretCodec
	pool  *pgxpool.Pool
	saved atomic.Bool
}

// Database storage is authoritative. Import the legacy encrypted file only when
// there is no database row; concurrent imports cannot overwrite newer cookies.
func NewDatabaseCookieStore(ctx context.Context, pool *pgxpool.Pool, codec SecretCodec, legacyPath string) (*CookieStore, error) {
	if pool == nil || codec == nil {
		return nil, ErrCookieStorage
	}
	s := &CookieStore{pool: pool, Codec: codec}
	cookies, err := s.LoadContext(ctx)
	if err != nil || len(cookies) > 0 {
		return s, err
	}
	legacy := &CookieStore{Path: legacyPath, Codec: codec}
	if legacyPath == "" {
		return s, nil
	}
	if _, err = os.Stat(legacyPath); os.IsNotExist(err) {
		return s, nil
	} else if err != nil {
		return nil, ErrCookieStorage
	}
	raw, err := legacy.readFile()
	if err != nil {
		return nil, err
	}
	if _, err = ParseCookies(raw); err != nil {
		return nil, ErrCookieStorage
	}
	writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err = pool.Exec(writeCtx, `INSERT INTO browser_credentials(provider,cookie_cipher) VALUES('shopee',$1) ON CONFLICT(provider) DO NOTHING`, codec.Encrypt(raw))
	if err != nil {
		return nil, ErrCookieStorage
	}
	_, err = s.LoadContext(ctx)
	return s, err
}

func (s *CookieStore) Configured() bool {
	if s == nil {
		return false
	}
	if s.pool != nil {
		return s.saved.Load()
	}
	info, e := os.Stat(s.Path)
	return e == nil && info.Size() > 0
}
func (s *CookieStore) Load() ([]*network.CookieParam, error) {
	return s.LoadContext(context.Background())
}
func (s *CookieStore) LoadContext(ctx context.Context) ([]*network.CookieParam, error) {
	if s == nil {
		return nil, nil
	}
	var raw string
	var err error
	if s.pool != nil {
		readCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		var encrypted string
		err = s.pool.QueryRow(readCtx, `SELECT cookie_cipher FROM browser_credentials WHERE provider='shopee'`).Scan(&encrypted)
		if errors.Is(err, pgx.ErrNoRows) {
			s.saved.Store(false)
			return nil, nil
		}
		if err != nil || s.Codec == nil || len(encrypted) > 2*MaxCookieBytes {
			return nil, ErrCookieStorage
		}
		raw, err = s.Codec.Decrypt(encrypted)
	} else {
		if !s.Configured() {
			return nil, nil
		}
		raw, err = s.readFile()
	}
	if err != nil {
		return nil, ErrCookieStorage
	}
	cookies, err := ParseCookies(raw)
	if err != nil {
		return nil, ErrCookieStorage
	}
	s.saved.Store(true)
	return cookies, nil
}
func (s *CookieStore) readFile() (string, error) {
	info, e := os.Stat(s.Path)
	if e != nil || s.Codec == nil || info.Size() > 2*MaxCookieBytes {
		return "", ErrCookieStorage
	}
	b, e := os.ReadFile(s.Path)
	if e != nil {
		return "", ErrCookieStorage
	}
	raw, e := s.Codec.Decrypt(string(b))
	if e != nil {
		return "", ErrCookieStorage
	}
	return raw, nil
}
func (s *CookieStore) Save(raw string) error {
	return s.SaveContext(context.Background(), raw)
}
func (s *CookieStore) SaveContext(ctx context.Context, raw string) error {
	if s == nil || s.Codec == nil {
		return ErrCookieStorage
	}
	if _, err := ParseCookies(raw); err != nil {
		return err
	}
	if s.pool != nil {
		writeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		_, err := s.pool.Exec(writeCtx, `INSERT INTO browser_credentials(provider,cookie_cipher) VALUES('shopee',$1) ON CONFLICT(provider) DO UPDATE SET cookie_cipher=EXCLUDED.cookie_cipher,updated_at=now()`, s.Codec.Encrypt(strings.TrimSpace(raw)))
		if err != nil {
			return ErrCookieStorage
		}
		s.saved.Store(true)
		return nil
	}
	if e := os.MkdirAll(filepath.Dir(s.Path), 0700); e != nil {
		return ErrCookieStorage
	}
	f, e := os.CreateTemp(filepath.Dir(s.Path), ".shopee-cookie-*")
	if e != nil {
		return ErrCookieStorage
	}
	name := f.Name()
	defer os.Remove(name)
	if _, e = f.WriteString(s.Codec.Encrypt(raw)); e != nil {
		f.Close()
		return ErrCookieStorage
	}
	if e = f.Close(); e != nil {
		return ErrCookieStorage
	}
	if e = os.Rename(name, s.Path); e != nil {
		return ErrCookieStorage
	}
	return nil
}
