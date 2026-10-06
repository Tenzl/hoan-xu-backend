package browser

import (
	"context"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Explicit opt-in against a supervised browser through a verified SSH tunnel.
// Inspect only our own tab; never interact with the administrator's login tab.
func TestRemoteEC2Sandbox(t *testing.T) {
	endpoint := os.Getenv("BROWSER_REMOTE_TEST_URL")
	if endpoint == "" {
		t.Skip("Set BROWSER_REMOTE_TEST_URL to a loopback tunnel to test the deployed browser")
	}
	m, err := NewRemote(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.OpenInteractive(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(m.root, 10*time.Second)
	defer cancel()
	var status string
	if err := chromedp.Run(ctx, chromedp.Navigate("chrome://sandbox/"), chromedp.Text("body", &status, chromedp.ByQuery)); err != nil {
		t.Fatal(err)
	}
	t.Log(status)
	layerOne := regexp.MustCompile(`(?i)(Namespace sandbox|SUID sandbox)\s+Yes|Layer 1 Sandbox\s+(Namespace|SUID)`)
	if !layerOne.MatchString(status) || !regexp.MustCompile(`(?i)Seccomp-BPF sandbox\s+Yes`).MatchString(status) {
		t.Fatal("Chromium OS sandbox is not active")
	}
}
