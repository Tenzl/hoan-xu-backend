package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/storage"
	"github.com/chromedp/chromedp"
	"hoanxu/internal/platform"
)

func TestCookieParsingAndScope(t *testing.T) {
	c, e := ParseCookies("Cookie: SPC_EC=example=a==; SPC_ST=example-b")
	if e != nil || len(c) != 2 || c[0].Value != "example=a==" || c[0].Domain != ".shopee.vn" {
		t.Fatal("header parsing failed", e)
	}
	raw := `[{"name":"SPC_EC","value":"fixture","domain":".shopee.vn","httpOnly":true,"sameSite":"no_restriction","expirationDate":2000000000}]`
	c, e = ParseCookies(raw)
	if e != nil || len(c) != 1 || !c[0].HTTPOnly || c[0].Expires == nil || c[0].SameSite != network.CookieSameSiteNone {
		t.Fatal("JSON parsing failed", e)
	}
	for _, raw := range []string{"", "SPC=fixture\r\nX-Header=bad", "missing-equals", "SPC=1;SPC=2", strings.Repeat("X", MaxCookieBytes+1), `[{"name":"SPC","value":"fixture","domain":"shopee.vn.evil.com"}]`, `[{"name":"SPC","value":"fixture","domain":"google.com"}]`, `[{"name":"SPC","value":"fixture","path":"\r\n"}]`, `[{"name":"SPC","value":"fixture","sameSite":"bad"}]`} {
		if _, e := ParseCookies(raw); e == nil || strings.Contains(e.Error(), "fixture") {
			t.Fatal("invalid cookies accepted or echoed")
		}
	}
}
func TestAffiliateCookieExport(t *testing.T) {
	raw := `{"url":"https://affiliate.shopee.vn","cookies":[{"name":"SPC_EC","value":"fixture=a==","domain":".shopee.vn","httpOnly":true,"expirationDate":2000000000},{"name":"affiliate_session","value":"fixture","domain":"affiliate.shopee.vn","hostOnly":true}]}`
	cookies, err := ParseCookies(raw)
	if err != nil || len(cookies) != 2 {
		t.Fatal("affiliate export rejected", err)
	}
	if cookies[0].Domain != ".shopee.vn" || cookies[0].Value != "fixture=a==" || !cookies[0].HTTPOnly || cookies[0].Expires == nil || cookies[1].URL != "https://affiliate.shopee.vn/" || cookies[1].Domain != "" {
		t.Fatal("affiliate export changed cookie attributes")
	}
	store := secretStore(t)
	if err := store.Save(raw); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded) != 2 || loaded[0].Value != cookies[0].Value {
		t.Fatal("affiliate export failed encrypted round trip", err)
	}
	for _, invalid := range []string{
		`{"url":"https://affiliate.shopee.vn","cookies":[]}`,
		`{"url":"https://affiliate.shopee.vn","cookies":null}`,
		`{"url":"https://affiliate.shopee.vn","cookies":"fixture"}`,
		strings.Replace(raw, "https://affiliate.shopee.vn", "https://affiliate.shopee.vn.evil.com", 1),
		strings.Replace(raw, "https://affiliate.shopee.vn", "http://affiliate.shopee.vn", 1),
		strings.Replace(raw, "https://affiliate.shopee.vn", "https://fixture@affiliate.shopee.vn", 1),
		strings.Replace(raw, `"domain":".shopee.vn"`, `"domain":"evil.com"`, 1),
	} {
		if _, err := ParseCookies(invalid); err != ErrInvalidCookies {
			t.Fatal("invalid affiliate export accepted or validation error changed")
		}
	}
}

func secretStore(t *testing.T) *CookieStore {
	t.Helper()
	codec, e := platform.New(nil, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	return &CookieStore{Path: filepath.Join(t.TempDir(), "shopee-cookies.enc"), Codec: codec}
}
func TestCookiesPersistEncryptedAndReplace(t *testing.T) {
	s := secretStore(t)
	raw := "SPC_EC=fixture-secret-one"
	if e := s.Save(raw); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(s.Path)
	if e != nil || strings.Contains(string(b), "fixture-secret") {
		t.Fatal("plaintext cookie file", e)
	}
	c, e := s.Load()
	if e != nil || c[0].Value != "fixture-secret-one" {
		t.Fatal("encrypted load failed", e)
	}
	if e = s.Save("SPC_EC=fixture-secret-two"); e != nil {
		t.Fatal(e)
	}
	c, e = s.Load()
	if e != nil || c[0].Value != "fixture-secret-two" {
		t.Fatal("atomic replacement failed", e)
	}
	if e = os.WriteFile(s.Path, []byte("corrupt-data"), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Load(); e == nil {
		t.Fatal("corrupted cookie file accepted")
	}
}
func TestVerificationRedirectInvalidatesSessionDuringProbeAndProductCheck(t *testing.T) {
	path := os.Getenv("BROWSER_TEST_PATH")
	if path == "" {
		t.Skip("Set BROWSER_TEST_PATH for Chromium verification redirect integration")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blocked-dashboard" || r.URL.Path == "/offer/blocked" {
			http.Redirect(w, r, "/verify/traffic/error", http.StatusFound)
			return
		}
		fmt.Fprint(w, "<html><body>Fixture</body></html>")
	}))
	defer fixture.Close()
	parsed, _ := url.Parse(fixture.URL)
	m := New(path, t.TempDir())
	defer m.Close()
	m.probeURL = fixture.URL + "/blocked-dashboard"
	m.offerBaseURL = fixture.URL + "/offer/"
	m.responseHost = parsed.Host
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	status := m.RefreshSession(ctx)
	if status["state"] != "verification_required" || status["authenticated"] != false {
		t.Fatal("verification redirect misclassified during probe", status)
	}
	if _, err := m.Check(ctx, "blocked"); err == nil || err.Error() != "SHOPEE_VERIFICATION_REQUIRED" {
		t.Fatal("blocked probe returned wrong check error", err)
	}
	m.probeURL = fixture.URL + "/dashboard"
	if status = m.RefreshSession(ctx); status["authenticated"] != true {
		t.Fatal("fixture session did not recover", status)
	}
	if _, err := m.capture(ctx, ctx, "blocked"); err == nil || err.Error() != "SHOPEE_VERIFICATION_REQUIRED" {
		t.Fatal("verification redirect misclassified during product check", err)
	}
	if m.Status()["authenticated"] != false || m.Status()["state"] != "verification_required" {
		t.Fatal("blocked product left session available", m.Status())
	}
}
func TestPasteCookiesReusesChromeForConcurrentChecksAndRestoresAfterRestart(t *testing.T) {
	path := os.Getenv("BROWSER_TEST_PATH")
	if path == "" {
		t.Skip("Set BROWSER_TEST_PATH for Chromium session integration")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/offer/product" {
			if r.URL.Query().Get("item_id") == "expired" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"data":{"fixture":true}}`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		if strings.HasPrefix(r.URL.Path, "/offer/") {
			fmt.Fprintf(w, `<html><body><script>fetch('/api/v3/offer/product?item_id=%s')</script></body></html>`, strings.TrimPrefix(r.URL.Path, "/offer/"))
			return
		}
		fmt.Fprint(w, "<html><body>Fixture dashboard</body></html>")
	}))
	defer fixture.Close()
	parsed, _ := url.Parse(fixture.URL)
	s := secretStore(t)
	profile := t.TempDir()
	m := NewLazy(path, profile, s)
	m.probeURL = fixture.URL + "/dashboard"
	m.offerBaseURL = fixture.URL + "/offer/"
	m.responseHost = parsed.Host
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	defer m.Close()
	status, e := m.PasteCookies(ctx, "SPC_EC=fixture-first")
	if e != nil || status["authenticated"] != true {
		t.Fatal("paste failed", e, status)
	}
	root := m.root
	// Simulate cookies rotated by Shopee. Checks/probes must preserve these live cookies.
	if e = chromedp.Run(root, network.SetCookies([]*network.CookieParam{{Name: "SPC_EC", Value: "fixture-rotated", Domain: ".shopee.vn", Path: "/", Secure: true}})); e != nil {
		t.Fatal(e)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { m.Run(runCtx); close(done) }()
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, item := range []string{"one", "two"} {
		wg.Add(1)
		go func(item string) {
			defer wg.Done()
			b, e := m.Check(ctx, item)
			if e == nil && !json.Valid(b) {
				e = fmt.Errorf("invalid fixture body")
			}
			errs <- e
		}(item)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if e != nil {
			t.Fatal(e)
		}
	}
	m.RefreshSession(ctx)
	if m.root != root || m.Status()["starts"] != 1 {
		t.Fatal("Chromium restarted for check/probe")
	}
	cookieValue := func(m *Manager) string {
		t.Helper()
		var cookies []*network.Cookie
		e := chromedp.Run(m.root, chromedp.ActionFunc(func(c context.Context) error { var e error; cookies, e = storage.GetCookies().Do(c); return e }))
		if e != nil {
			t.Fatal(e)
		}
		for _, c := range cookies {
			if c.Name == "SPC_EC" {
				return c.Value
			}
		}
		return ""
	}
	if cookieValue(m) != "fixture-rotated" {
		t.Fatal("live cookie was overwritten during check")
	}
	if _, e = m.PasteCookies(ctx, "SPC_EC=fixture-second"); e != nil {
		t.Fatal(e)
	}
	if m.root != root || m.SessionVersion() != 2 || cookieValue(m) != "fixture-second" {
		t.Fatal("cookie update did not use existing browser")
	}
	if _, e = m.Check(ctx, "expired"); e == nil || e.Error() != "SHOPEE_LOGIN_REQUIRED" {
		t.Fatal("401 did not invalidate session", e)
	}
	if m.Status()["authenticated"] != false {
		t.Fatal("expired session still ready")
	}
	stop()
	<-done
	restored := NewLazy(path, profile, s)
	defer restored.Close()
	if e = restored.start(ctx); e != nil {
		t.Fatal(e)
	}
	if cookieValue(restored) != "fixture-second" {
		t.Fatal("cookie not restored after restart")
	}
}
