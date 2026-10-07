package browser

// Compatibility for encrypted exports from older deployments. Production runtime uses browser profiles.
import (
	"context"
	"errors"
	"github.com/chromedp/cdproto/network"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

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
