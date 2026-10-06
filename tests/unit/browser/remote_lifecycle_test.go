package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

type tunnelWriter struct {
	http.ResponseWriter
	onHijack func(net.Conn)
}

func (w tunnelWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w tunnelWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, rw, err := w.ResponseWriter.(http.Hijacker).Hijack()
	if err == nil {
		w.onHijack(c)
	}
	return c, rw, err
}

// Own the external Chrome process separately from Manager so that closing Go's
// controller cannot accidentally pass a test by killing/relaunching Chrome.
func startExternalChrome(t *testing.T, profile string) (string, func()) {
	t.Helper()
	path := os.Getenv("BROWSER_TEST_PATH")
	if path == "" {
		t.Skip("Set BROWSER_TEST_PATH to run remote browser lifecycle tests")
	}
	activePort := filepath.Join(profile, "DevToolsActivePort")
	_ = os.Remove(activePort)
	cmd := exec.Command(path, "--headless", "--remote-debugging-port=0", "--user-data-dir="+profile, "--no-first-run", "--no-default-browser-check", "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	stop := func() { once.Do(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }) }
	t.Cleanup(stop)
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(activePort); err == nil {
			lines := strings.Fields(string(raw))
			if len(lines) >= 2 {
				return "http://127.0.0.1:" + lines[0], stop
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("external Chrome did not expose a CDP port")
	return "", stop
}

func TestRemoteLifecycleCaptureAndRecovery(t *testing.T) {
	var active, maxActive atomic.Int32
	var probeDuringWork atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/dashboard":
			if active.Load() > 0 {
				probeDuringWork.Store(true)
			}
			fmt.Fprint(w, remoteSPAHTML)
		case strings.HasPrefix(r.URL.Path, "/offer/"):
			fmt.Fprint(w, remoteSPAHTML)
		case r.URL.Path == "/api/v3/offer/product":
			n := active.Add(1)
			defer active.Add(-1)
			for old := maxActive.Load(); n > old; old = maxActive.Load() {
				if maxActive.CompareAndSwap(old, n) {
					break
				}
			}
			time.Sleep(300 * time.Millisecond)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"code":0,"data":{"item_id":"%s"}}`, r.URL.Query().Get("item_id"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	profile := t.TempDir()
	endpoint, stopChrome := startExternalChrome(t, profile)
	var destination atomic.Value
	u, _ := url.Parse(endpoint)
	destination.Store(u)
	// A stable tunnel endpoint forwards to a newly started browser after restart.
	var tunnelMu sync.Mutex
	var tunnelConnections []net.Conn
	proxy := &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(destination.Load().(*url.URL))
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) { w.WriteHeader(502) }}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxy.ServeHTTP(tunnelWriter{w, func(c net.Conn) {
			tunnelMu.Lock()
			tunnelConnections = append(tunnelConnections, c)
			tunnelMu.Unlock()
		}}, r)
	}))
	defer relay.Close()
	configure := func(endpoint string) *Manager {
		m, err := NewRemote(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		m.probeURL = server.URL + "/dashboard"
		m.offerBaseURL = server.URL + "/offer/"
		u, _ := url.Parse(server.URL)
		m.responseHost = u.Host
		return m
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m := configure(relay.URL)
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	waitState := func(m *Manager, state string) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if m.Status()["state"] == state {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("wanted %s: %+v", state, m.Status())
	}
	waitState(m, "authenticated")
	check := func(item string) error {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		body, err := m.Check(c, item)
		if err != nil {
			return err
		}
		if !json.Valid(body) || !strings.Contains(string(body), `"item_id":"`+item+`"`) {
			return fmt.Errorf("wrong response: %s", body)
		}
		return nil
	}
	errors := make(chan error, 2)
	go func() { errors <- check("101") }()
	go func() { errors <- check("102") }()
	deadline := time.Now().Add(5 * time.Second)
	for active.Load() != 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	refreshDone := make(chan struct{})
	go func() { m.RefreshSession(context.Background()); close(refreshDone) }()
	for i := 0; i < 2; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	<-refreshDone
	if probeDuringWork.Load() {
		t.Fatal("session probe ran while worker tabs were busy")
	}
	short, stopShort := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_, err := m.Check(short, "cancelled")
	stopShort()
	if err == nil {
		t.Fatal("cancelled request returned success")
	}
	// The worker receives cancellation asynchronously, then closes its target.
	time.Sleep(400 * time.Millisecond)
	targets, err := chromedp.Targets(m.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if target.URL == server.URL+"/offer/cancelled" {
			t.Fatal("cancelled worker tab leaked", target.URL)
		}
	}
	if maxActive.Load() != 2 {
		t.Fatal("expected two overlapping worker tabs", maxActive.Load())
	}
	if err := chromedp.Run(m.root, chromedp.Navigate(server.URL+"/dashboard"), chromedp.Evaluate(`localStorage.setItem('profile-fixture','saved')`, nil)); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("controller did not stop")
	}
	if _, err := remoteWebSocket(context.Background(), endpoint); err != nil {
		t.Fatal("Go shutdown killed external Chrome", err)
	}
	response, err := (&http.Client{Timeout: 3 * time.Second}).Get(endpoint + "/json/list")
	if err != nil {
		t.Fatal(err)
	}
	var remaining []struct{ Type string }
	err = json.NewDecoder(response.Body).Decode(&remaining)
	response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	pagesAfterClose := 0
	for _, info := range remaining {
		if info.Type == "page" {
			pagesAfterClose++
		}
	}
	if pagesAfterClose != 1 {
		t.Fatal("Go shutdown leaked its controller/warm tabs or closed the native tab", pagesAfterClose)
	}
	// Reattach, verify the existing profile, and lose the external process.
	m = configure(relay.URL)
	ctx2, cancel2 := context.WithCancel(context.Background())
	done2 := make(chan struct{})
	go func() { m.Run(ctx2); close(done2) }()
	defer func() { cancel2(); <-done2 }()
	waitState(m, "authenticated")
	var saved string
	if err := chromedp.Run(m.root, chromedp.Navigate(server.URL+"/dashboard"), chromedp.Evaluate(`localStorage.getItem('profile-fixture')`, &saved)); err != nil || saved != "saved" {
		t.Fatal("profile lost on controller restart", saved, err)
	}
	oldWS, _ := remoteWebSocket(context.Background(), relay.URL)
	oldVersion := m.SessionVersion()
	// Drop only CDP transport while the same browser/profile keeps running.
	tunnelMu.Lock()
	for _, c := range tunnelConnections {
		_ = c.Close()
	}
	tunnelConnections = nil
	tunnelMu.Unlock()
	waitState(m, "unavailable")
	if _, err := remoteWebSocket(context.Background(), endpoint); err != nil {
		t.Fatal("tunnel loss killed browser", err)
	}
	waitState(m, "authenticated")
	targets, err = chromedp.Targets(m.root)
	if err != nil {
		t.Fatal(err)
	}
	pages := 0
	for _, info := range targets {
		if info.Type == "page" {
			pages++
		}
	}
	if pages != 4 {
		t.Fatal("tunnel reconnect leaked controller/worker tabs or closed the native tab", pages)
	}
	if m.SessionVersion() <= oldVersion {
		t.Fatal("tunnel reconnect did not invalidate session cache")
	}
	oldVersion = m.SessionVersion()
	stopChrome()
	waitState(m, "unavailable")
	if _, err := m.Check(context.Background(), "103"); err == nil || err.Error() != "BROWSER_UNAVAILABLE" {
		t.Fatal("expected outage error", err)
	}
	// The new browser has a new ID; the controller reconnects automatically.
	endpoint, _ = startExternalChrome(t, profile)
	u, _ = url.Parse(endpoint)
	destination.Store(u)
	waitState(m, "authenticated")
	newWS, _ := remoteWebSocket(context.Background(), relay.URL)
	if newWS == oldWS || m.SessionVersion() <= oldVersion {
		t.Fatal("reconnect did not rediscover/invalidate session")
	}
	if err := check("104"); err != nil {
		t.Fatal(err)
	}
}
