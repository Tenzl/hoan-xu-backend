package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

const remoteSPAHTML = `<body><a href="/dashboard">Fixture dashboard</a><script>
window.fixtureBoot = Math.random().toString();
function route() {
  const item = location.pathname.match(/^\/offer\/(.+)$/)?.[1];
  if (item === 'verification') { location.href='/verify/captcha'; return; }
  if (item === 'login') { location.href='/login'; return; }
  if (item === 'spa-verification') { history.replaceState(history.state, '', '/verify/captcha'); return; }
  if (item === 'spa-login') { history.replaceState(history.state, '', '/login'); return; }
  if (item) fetch('/api/v3/offer/product?item_id='+encodeURIComponent(item));
}
window.addEventListener('popstate', route);
route();
</script></body>`

func TestWorkersReuseTabsAndRejectLateResponses(t *testing.T) {
	for _, mode := range []string{"local", "remote"} {
		t.Run(mode, func(t *testing.T) { warmWorkersReuseDocumentsAndRejectLateResponses(t, mode) })
	}
}

func workerTestManager(t *testing.T, mode string) *Manager {
	t.Helper()
	if mode == "local" {
		path := os.Getenv("BROWSER_TEST_PATH")
		if path == "" {
			t.Skip("Set BROWSER_TEST_PATH to test local browser workers")
		}
		return NewManual(path, t.TempDir(), WithHeadless(true))
	}
	endpoint, _ := startExternalChrome(t, t.TempDir())
	m, err := NewRemote(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func warmWorkersReuseDocumentsAndRejectLateResponses(t *testing.T, mode string) {
	late := make(chan struct{})
	lateStarted := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/offer/product" {
			fmt.Fprint(w, remoteSPAHTML)
			return
		}
		if r.URL.Query().Get("late") != "" {
			lateStarted <- struct{}{}
			select {
			case <-late:
			case <-r.Context().Done():
			}
			fmt.Fprint(w, `{"code":0,"data":{"item_id":"102","stale":true}}`)
			return
		}
		if r.URL.Query().Get("item_id") == "expired" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// Keep the new response pending until the previous job's response has
		// arrived, so accepting stale network events would fail deterministically.
		if r.URL.Query().Get("item_id") == "102" {
			time.Sleep(150 * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"code":0,"data":{"item_id":"%s"}}`, r.URL.Query().Get("item_id"))
	}))
	defer server.Close()
	m := workerTestManager(t, mode)
	m.probeURL, m.offerBaseURL = server.URL+"/dashboard", server.URL+"/offer/"
	u, _ := url.Parse(server.URL)
	m.responseHost = u.Host
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Close()
	m.probe(ctx)
	if m.Status()["state"] != "authenticated" {
		t.Fatal("fixture session unavailable", m.Status())
	}
	// Hold the second slot to exercise several jobs in exactly the same tab.
	other := <-m.workerTabs
	otherHeld := true
	var w *workerTab
	leased := false
	defer func() {
		if leased {
			m.workerTabs <- w
		}
		if otherHeld {
			m.workerTabs <- other
		}
	}()
	take := func() { w = <-m.workerTabs; leased = true }
	put := func() { m.workerTabs <- w; leased = false }
	take()
	if w == nil || !w.ready {
		t.Fatal("worker not preloaded")
	}
	var boot string
	if err := chromedp.Run(w.ctx, chromedp.Navigate(server.URL+"/dashboard"), chromedp.Evaluate(`window.fixtureBoot`, &boot)); err != nil {
		t.Fatal(err)
	}
	workerID := chromedp.FromContext(w.ctx).Target.TargetID
	put()
	check := func(item string) {
		t.Helper()
		request, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		body, err := m.capture(ctx, request, item)
		if err != nil || !strings.Contains(string(body), `"item_id":"`+item+`"`) || strings.Contains(string(body), `"stale"`) {
			t.Fatal("wrong product response", string(body), err)
		}
	}
	check("101")
	take()
	if err := chromedp.Run(w.ctx, chromedp.Evaluate(`void fetch('/api/v3/offer/product?item_id=102&late=1')`, nil)); err != nil {
		t.Fatal(err)
	}
	<-lateStarted
	put()
	go func() { time.Sleep(50 * time.Millisecond); close(late) }()
	check("102")
	check("103")
	take()
	var currentBoot string
	if err := chromedp.Run(w.ctx, chromedp.Evaluate(`window.fixtureBoot`, &currentBoot)); err != nil {
		t.Fatal(err)
	}
	if chromedp.FromContext(w.ctx).Target.TargetID != workerID || currentBoot == boot {
		t.Fatal("worker must reuse the tab and navigate a fresh document between products")
	}
	put()
	// Same-item refresh must trigger a fresh request rather than reusing the
	// previous page's result when the router ignores an unchanged destination.
	check("103")
	m.workerTabs <- other
	otherHeld = false
	for _, fixture := range []struct{ item, code, state string }{
		{"verification", "SHOPEE_VERIFICATION_REQUIRED", "verification_required"},
		{"spa-verification", "SHOPEE_VERIFICATION_REQUIRED", "verification_required"},
		{"login", "SHOPEE_LOGIN_REQUIRED", "login_required"},
		{"spa-login", "SHOPEE_LOGIN_REQUIRED", "login_required"},
		{"expired", "SHOPEE_LOGIN_REQUIRED", "login_required"},
	} {
		m.RefreshSession(ctx)
		request, stop := context.WithTimeout(ctx, 5*time.Second)
		_, err := m.capture(ctx, request, fixture.item)
		stop()
		if err == nil || err.Error() != fixture.code {
			t.Fatalf("warm %s did not invalidate session: %v", fixture.item, err)
		}
		if m.Status()["state"] != fixture.state || m.Status()["authenticated"] != false {
			t.Fatal("warm worker retained an invalid session", m.Status())
		}
	}
}

func TestWorkerNavigatesWithoutClientRouter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/offer/product" {
			fmt.Fprint(w, `{"code":0,"data":{"item_id":"101"}}`)
		} else if r.URL.Path == "/offer/101" {
			fmt.Fprint(w, `<body><script>fetch('/api/v3/offer/product?item_id=101')</script></body>`)
		} else {
			fmt.Fprint(w, `<body><a href="/dashboard">Ready without a SPA router</a></body>`)
		}
	}))
	defer server.Close()
	endpoint, _ := startExternalChrome(t, t.TempDir())
	m, err := NewRemote(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.probeURL, m.offerBaseURL = server.URL+"/dashboard", server.URL+"/offer/"
	u, _ := url.Parse(server.URL)
	m.responseHost = u.Host
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	m.probe(ctx)
	body, err := m.capture(ctx, ctx, "101")
	if err != nil || !strings.Contains(string(body), `"item_id":"101"`) {
		t.Fatal("normal navigation did not recover a missing SPA response", err)
	}
}
