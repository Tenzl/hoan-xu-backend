package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRemoteConfigurationAndDiscovery(t *testing.T) {
	for _, endpoint := range []string{"", "http://ec2.example:9222", "https://127.0.0.1:9222", "http://127.0.0.1", "http://user:pass@127.0.0.1:9222", "http://127.0.0.1:9222/path", "http://127.0.0.1:9222?token=secret"} {
		if _, err := NewRemote(endpoint); err == nil {
			t.Fatal("accepted unsafe endpoint", endpoint)
		}
	}
	version := `{"webSocketDebuggerUrl":"ws://container.internal:9223/devtools/browser/first"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/json/version" {
			t.Error("wrong discovery path", r.URL.Path)
		}
		fmt.Fprint(w, version)
	}))
	defer server.Close()
	m, err := NewRemote(server.URL)
	if err != nil || m.cookies != nil || m.path != "" || m.profile != "" || !m.autoStart || !m.manualLogin {
		t.Fatal("remote must own no process/profile/cookie store", err)
	}
	ws, err := remoteWebSocket(context.Background(), server.URL)
	if err != nil || ws != strings.Replace(server.URL, "http:", "ws:", 1)+"/devtools/browser/first" {
		t.Fatal(ws, err)
	}
	version = `{"webSocketDebuggerUrl":"ws://127.0.0.1:9223/devtools/browser/restarted"}`
	ws, err = remoteWebSocket(context.Background(), server.URL)
	if err != nil || !strings.HasSuffix(ws, "/restarted") {
		t.Fatal("discovery cached the old browser ID", ws, err)
	}
	for _, invalid := range []string{`{}`, `invalid`, `{"webSocketDebuggerUrl":"ws://localhost/devtools/page/1"}`, `{"webSocketDebuggerUrl":"ws://localhost/devtools/browser/"}`} {
		version = invalid
		if _, err := remoteWebSocket(context.Background(), server.URL); err == nil {
			t.Fatal("accepted invalid discovery", invalid)
		}
	}
}

func TestRemoteOutageBackoffAndUnavailableStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	m, _ := NewRemote(server.URL)
	defer m.Close()
	if err := m.start(context.Background()); err == nil || err.Error() != "BROWSER_UNAVAILABLE" {
		t.Fatal(err)
	}
	if m.Status()["state"] != "unavailable" || m.starts != 0 || m.retryDelay != 5*time.Second {
		t.Fatal(m.Status())
	}
	deadline := m.retryAt
	if err := m.start(context.Background()); err == nil || !m.retryAt.Equal(deadline) {
		t.Fatal("reconnect ignored backoff", err)
	}
	if _, err := m.Check(context.Background(), "1"); err == nil || err.Error() != "BROWSER_UNAVAILABLE" {
		t.Fatal("outage misreported as login required", err)
	}
	for i := 0; i < 6; i++ {
		m.connectionFailed()
	}
	if m.retryDelay != 30*time.Second {
		t.Fatal("backoff must be capped", m.retryDelay)
	}
}

func TestDiscoveryDoesNotFollowRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, "http://example.invalid/", 302) }))
	defer server.Close()
	if _, err := remoteWebSocket(context.Background(), server.URL); err == nil {
		t.Fatal("discovery followed redirect")
	}
}
