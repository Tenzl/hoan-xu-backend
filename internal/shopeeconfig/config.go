// Package shopeeconfig stores private settings separately per application origin.
package shopeeconfig

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"hoanxu/internal/browser"
	"hoanxu/internal/platform"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Fields struct {
	Publisher      string `json:"publisher"`
	Enabled        bool   `json:"enabled"`
	PriceScale     int64  `json:"priceScale"`
	Mode           string `json:"mode"`
	ExecutablePath string `json:"executablePath"`
	ProfilePath    string `json:"profilePath"`
	Headless       bool   `json:"headless"`
	RemoteURL      string `json:"remoteUrl"`
}
type Input struct {
	Fields
	Version string `json:"version"`
}
type Config struct {
	Fields
	Version             string     `json:"version"`
	Revision            int64      `json:"revision"`
	TrackingVerified    bool       `json:"trackingVerified"`
	SchemaVerified      bool       `json:"schemaVerified"`
	VerifiedAt          *time.Time `json:"verifiedAt"`
	VerifiedFingerprint string     `json:"verificationFingerprint,omitempty"`
}

func Default() Config {
	return Config{Fields: Fields{Mode: "local", ProfilePath: "private-data/chrome-profile", RemoteURL: "http://127.0.0.1:9222", PriceScale: 100000}, Version: "0:0"}
}
func Key(origin string) string { return platform.Hash(strings.TrimRight(origin, "/")) }
func (c Config) Fingerprint() string {
	fields := c.Fields
	fields.Enabled = false
	if fields.Mode == "local" {
		fields.RemoteURL = ""
	} else {
		fields.ProfilePath = ""
		fields.ExecutablePath = ""
	}
	raw, _ := json.Marshal(fields)
	return platform.Hash(string(raw))
}
func parse(raw []byte, origin string) (Config, map[string]json.RawMessage, error) {
	cfg := Default()
	all := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &all); err != nil {
		return cfg, nil, err
	}
	runtimes := map[string]json.RawMessage{}
	if r := all["runtimeConfigs"]; len(r) > 0 {
		if err := json.Unmarshal(r, &runtimes); err != nil {
			return cfg, nil, err
		}
	}
	if r := runtimes[Key(origin)]; len(r) > 0 {
		if err := json.Unmarshal(r, &cfg); err != nil {
			return cfg, nil, err
		}
	}
	var pubRevision int64
	_ = json.Unmarshal(all["publisher"], &cfg.Publisher)
	_ = json.Unmarshal(all["publisherRevision"], &pubRevision)
	cfg.Version = fmt.Sprintf("%d:%d", cfg.Revision, pubRevision)
	verified := cfg.VerifiedAt != nil && cfg.VerifiedFingerprint == cfg.Fingerprint()
	cfg.TrackingVerified = verified
	cfg.SchemaVerified = verified
	if !verified {
		cfg.Enabled = false
		cfg.VerifiedAt = nil
	}
	return cfg, all, nil
}
func Load(ctx context.Context, s *platform.Store, origin string) (Config, error) {
	var raw []byte
	err := s.Pool.QueryRow(ctx, `SELECT settings FROM affiliate_channels WHERE id='shopee'`).Scan(&raw)
	if err != nil {
		return Default(), err
	}
	cfg, _, err := parse(raw, origin)
	return cfg, err
}
func (c Fields) Validate() error {
	invalid := func() error {
		return platform.Fail(422, "INVALID_SHOPEE_SETTINGS", "Cấu hình Shopee/Chrome không hợp lệ. Kiểm tra chế độ, đơn vị giá và địa chỉ Chrome loopback.")
	}
	if len(c.Publisher) > 32 {
		return invalid()
	}
	for _, ch := range c.Publisher {
		if ch < '0' || ch > '9' {
			return invalid()
		}
	}
	if c.Mode != "local" && c.Mode != "remote" || c.PriceScale < 1 || c.PriceScale > 1000000000 || c.Headless {
		return invalid()
	}
	u, e := url.Parse(c.RemoteURL)
	if e != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return invalid()
	}
	port, e := strconv.Atoi(u.Port())
	if e != nil || port < 1 || port > 65535 {
		return invalid()
	}
	if len(c.ProfilePath) == 0 || len(c.ProfilePath) > 1024 || len(c.ExecutablePath) > 1024 || strings.ContainsAny(c.ProfilePath+c.ExecutablePath, "\x00\r\n") {
		return invalid()
	}
	return nil
}
func (c Config) NewBrowser() (*browser.Manager, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if c.Mode == "remote" {
		return browser.NewRemote(c.RemoteURL, browser.WithAutoStart(c.Enabled))
	}
	profile, err := filepath.Abs(c.ProfilePath)
	if err != nil {
		return nil, err
	}
	return browser.NewManual(c.ExecutablePath, profile, browser.WithHeadless(false)), nil
}
func lock(ctx context.Context, s *platform.Store, origin string) (pgx.Tx, Config, map[string]json.RawMessage, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, Default(), nil, err
	}
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT settings FROM affiliate_channels WHERE id='shopee' FOR UPDATE`).Scan(&raw); err != nil {
		tx.Rollback(ctx)
		return nil, Default(), nil, err
	}
	cfg, all, err := parse(raw, origin)
	if err != nil {
		tx.Rollback(ctx)
	}
	return tx, cfg, all, err
}
func persist(ctx context.Context, tx pgx.Tx, origin string, c Config, all map[string]json.RawMessage) error {
	runtimes := map[string]json.RawMessage{}
	if r := all["runtimeConfigs"]; len(r) > 0 {
		if err := json.Unmarshal(r, &runtimes); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return err
	}
	runtimes[Key(origin)] = raw
	all["runtimeConfigs"], err = json.Marshal(runtimes)
	if err != nil {
		return err
	}
	raw, err = json.Marshal(all)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE affiliate_channels SET settings=$1 WHERE id='shopee'`, raw)
	return err
}
func Save(ctx context.Context, s *platform.Store, origin, actor string, in Input) (Config, error) {
	in.Publisher = strings.TrimSpace(in.Publisher)
	if err := in.Validate(); err != nil {
		return Default(), err
	}
	tx, old, all, err := lock(ctx, s, origin)
	if err != nil {
		return old, err
	}
	defer tx.Rollback(ctx)
	if in.Version != old.Version {
		return old, platform.Fail(409, "SHOPEE_SETTINGS_CONFLICT", "Cấu hình đã thay đổi ở nơi khác. Tải lại cấu hình trước khi lưu.")
	}
	next := old
	next.Fields = in.Fields
	for _, publisher := range []string{old.Publisher, next.Publisher} {
		if publisher != "" {
			if _, err = tx.Exec(ctx, `INSERT INTO tracking_publishers(publisher) VALUES($1) ON CONFLICT DO NOTHING`, publisher); err != nil {
				return old, err
			}
		}
	}
	if next.Fingerprint() != old.Fingerprint() {
		next.VerifiedAt = nil
		next.VerifiedFingerprint = ""
		next.TrackingVerified = false
		next.SchemaVerified = false
	}
	if next.Enabled && (!next.TrackingVerified || next.Publisher == "") {
		return old, platform.Fail(409, "TRACKING_NOT_VERIFIED", "Cần kiểm tra sản phẩm và tracking thành công trước khi cho khách tạo link.")
	}
	if next.Publisher != old.Publisher {
		var revision int64
		_ = json.Unmarshal(all["publisherRevision"], &revision)
		revision++
		all["publisher"], _ = json.Marshal(next.Publisher)
		all["publisherRevision"], _ = json.Marshal(revision)
	}
	next.Revision++
	var publisherRevision int64
	_ = json.Unmarshal(all["publisherRevision"], &publisherRevision)
	next.Version = fmt.Sprintf("%d:%d", next.Revision, publisherRevision)
	if err = persist(ctx, tx, origin, next, all); err != nil {
		return old, err
	}
	if err = platform.Audit(ctx, tx, actor, "shopee_settings_updated", "shopee", map[string]any{"origin": origin, "configuration": next.Fields}); err != nil {
		return old, err
	}
	if err = tx.Commit(ctx); err != nil {
		return old, err
	}
	return next, nil
}
