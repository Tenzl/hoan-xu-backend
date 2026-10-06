package browser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// NewRemote controls an independently supervised browser. Authentication lives
// exclusively in its default profile; no cookies are imported or exported.
func NewRemote(endpoint string) (*Manager, error) {
	if _, err := remoteEndpoint(endpoint); err != nil {
		return nil, err
	}
	m := NewManual("", "")
	m.remoteURL = endpoint
	m.autoStart = true
	m.workerTabs = make(chan *workerTab, 2)
	m.workerTabs <- nil
	m.workerTabs <- nil
	return m, nil
}

func remoteEndpoint(endpoint string) (*url.URL, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("CHROME_REMOTE_URL must be an HTTP loopback origin with a port (use an SSH tunnel)")
	}
	return u, nil
}

// Discover on every reconnect: the browser ID changes when Chromium restarts.
// Rewrite the authority to the tunnel, rather than trusting a container address.
func remoteWebSocket(ctx context.Context, endpoint string) (string, error) {
	u, err := remoteEndpoint(endpoint)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	u.Path = "/json/version"
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, err := client.Do(r)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var version struct {
		WebSocket string `json:"webSocketDebuggerUrl"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&version) != nil {
		return "", errors.New("invalid CDP discovery response")
	}
	ws, err := url.Parse(version.WebSocket)
	if err != nil || ws.Scheme != "ws" || !strings.HasPrefix(ws.Path, "/devtools/browser/") || len(strings.TrimPrefix(ws.Path, "/devtools/browser/")) == 0 || ws.User != nil || ws.RawQuery != "" || ws.Fragment != "" {
		return "", errors.New("invalid browser WebSocket URL")
	}
	ws.Host = u.Host
	return ws.String(), nil
}
