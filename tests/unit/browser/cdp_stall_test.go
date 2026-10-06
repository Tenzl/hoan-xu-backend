package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

// Drop one CDP command on the real WebSocket, preserving the connection and
// other targets. This reproduces an unresponsive worker, not an HTTP timeout.
func TestCDPStallRecyclesOnlyWorkerAndRespectsRetryBudget(t *testing.T) {
	endpoint, _ := startExternalChrome(t, t.TempDir())
	u, _ := url.Parse(endpoint)
	cdpHost := u.Host
	proxy := httputil.NewSingleHostReverseProxy(u)
	var drop atomic.Value
	drop.Store("")
	var dropped atomic.Int32
	var mu sync.Mutex
	var connections []net.Conn
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			proxy.ServeHTTP(w, r)
			return
		}
		client, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		upstream, _, _, err := ws.Dial(context.Background(), "ws://"+cdpHost+r.URL.Path)
		if err != nil {
			client.Close()
			return
		}
		mu.Lock()
		connections = append(connections, client, upstream)
		mu.Unlock()
		go func() {
			defer client.Close()
			defer upstream.Close()
			for {
				data, op, err := wsutil.ReadServerData(upstream)
				if err != nil || wsutil.WriteServerMessage(client, op, data) != nil {
					return
				}
			}
		}()
		go func() {
			defer client.Close()
			defer upstream.Close()
			for {
				data, op, err := wsutil.ReadClientData(client)
				if err != nil {
					return
				}
				var command struct{ Method string }
				_ = json.Unmarshal(data, &command)
				if command.Method == drop.Load().(string) && dropped.CompareAndSwap(0, 1) {
					continue
				}
				if wsutil.WriteClientMessage(upstream, op, data) != nil {
					return
				}
			}
		}()
	}))
	defer func() {
		mu.Lock()
		for _, c := range connections {
			c.Close()
		}
		mu.Unlock()
		relay.Close()
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/offer/product" {
			fmt.Fprint(w, `{"code":0,"data":{"item_id":"101"}}`)
		} else {
			fmt.Fprint(w, remoteSPAHTML)
		}
	}))
	defer server.Close()
	m, err := NewRemote(relay.URL)
	if err != nil {
		t.Fatal(err)
	}
	u, _ = url.Parse(server.URL)
	m.responseHost, m.probeURL, m.offerBaseURL = u.Host, server.URL+"/dashboard", server.URL+"/offer/"
	ctx, cancel := context.WithCancel(context.Background())
	m.probe(ctx)
	if m.Status()["authenticated"] != true {
		t.Fatal("fixture probe failed", m.Status())
	}
	m.autoStart = false
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	login := chromedp.FromContext(m.root).Target.TargetID
	for _, method := range []string{"Page.navigate", "Runtime.evaluate", "Network.getResponseBody"} {
		t.Run(method, func(t *testing.T) {
			dropped.Store(0)
			drop.Store(method)
			request, stop := context.WithTimeout(ctx, 12*time.Second)
			defer stop()
			started := time.Now()
			body, err := m.Check(request, "101")
			if err != nil || !strings.Contains(string(body), `"item_id":"101"`) {
				t.Fatal("stalled CDP command did not recover", err)
			}
			last := m.Status()["lastFailure"].(*LastFailure)
			if dropped.Load() != 1 || time.Since(started) >= 10*time.Second || last.Code != "BROWSER_UNAVAILABLE" {
				t.Fatal("stall was not bounded", m.Status())
			}
			if m.Status()["authenticated"] != true || m.Status()["starts"] != 1 || chromedp.FromContext(m.root).Target.TargetID != login {
				t.Fatal("worker error invalidated whole browser", m.Status())
			}
		})
	}
	// With less than five seconds remaining, no replacement attempt is started.
	dropped.Store(0)
	drop.Store("Page.navigate")
	request, stop := context.WithTimeout(ctx, 6*time.Second)
	defer stop()
	_, err = m.Check(request, "101")
	if err == nil || err.Error() != "BROWSER_UNAVAILABLE" {
		t.Fatal("retried outside remaining budget", err)
	}
}
