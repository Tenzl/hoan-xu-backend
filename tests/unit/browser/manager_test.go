package browser

import (
	"context"
	"github.com/chromedp/chromedp"
	"os"
	"testing"
	"time"
)

func TestManagedBrowserHeadlessConfiguration(t *testing.T) {
	if err := NewManaged("missing-chromium", t.TempDir(), nil, false).OpenInteractive(); err == nil {
		t.Fatal("headless browser must reject interactive access before starting Chrome")
	}
	if m := NewManaged("", t.TempDir(), nil, false); !m.headless {
		t.Fatal("default browser should stay headless")
	}
	if m := NewManaged("", t.TempDir(), nil, false, WithHeadless(false)); m.headless {
		t.Fatal("explicit interactive browser configuration was ignored")
	}
	if m := NewManaged("", t.TempDir(), nil, false, WithHeadless(true)); !m.headless {
		t.Fatal("explicit headless configuration was ignored")
	}
}

func TestSessionLocationDistinguishesVerificationFromLogin(t *testing.T) {
	for _, test := range []struct{ location, want string }{
		{"https://affiliate.shopee.vn/dashboard", "authenticated"},
		{"https://affiliate.shopee.vn/login", "login_required"},
		{"https://shopee.vn/buyer/login", "login_required"},
		{"https://shopee.vn/verify/traffic/error", "verification_required"},
		{"https://shopee.vn/verify/captcha", "verification_required"},
		{"https://affiliate.shopee.vn/verify/traffic/error", "verification_required"},
		{"https://shopee.vn.evil.invalid/verify/traffic/error", "login_required"},
	} {
		if got := sessionLocationState(test.location, "https://affiliate.shopee.vn/dashboard"); got != test.want {
			t.Fatalf("%s: %s want %s", test.location, got, test.want)
		}
	}
	m := New("", t.TempDir())
	m.state = "verification_required"
	if _, err := m.Check(context.Background(), "1"); err == nil || err.Error() != "SHOPEE_VERIFICATION_REQUIRED" {
		t.Fatal("verification misreported as expired session", err)
	}
}

func TestCheckerRejectsExpiredSessionAndFullQueue(t *testing.T) {
	m := New("", t.TempDir())
	if _, e := m.Check(context.Background(), "1"); e == nil || e.Error() != "SHOPEE_LOGIN_REQUIRED" {
		t.Fatal(e)
	}
	m.authenticated = true
	for i := 0; i < 50; i++ {
		m.queue <- job{}
	}
	if _, e := m.Check(context.Background(), "1"); e == nil || e.Error() != "QUEUE_FULL" {
		t.Fatal(e)
	}
}

func TestBrowserSurvivesStartup(t *testing.T) {
	path := os.Getenv("BROWSER_TEST_PATH")
	if path == "" {
		t.Skip("Set BROWSER_TEST_PATH for a local Chromium lifecycle smoke test")
	}
	ctx, c := context.WithTimeout(context.Background(), 20*time.Second)
	defer c()
	m := New(path, t.TempDir())
	defer m.Close()
	if e := m.start(ctx); e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	var title string
	if e := chromedp.Run(m.root, chromedp.Navigate("data:text/html,<title>HoanXu</title>"), chromedp.Title(&title)); e != nil || title != "HoanXu" {
		t.Fatal(title, e)
	}
}
