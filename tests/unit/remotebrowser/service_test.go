package remotebrowser

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"hoanxu/internal/auth"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeSessions struct {
	u  *auth.User
	mu sync.RWMutex
}

func (f *fakeSessions) Session(context.Context, string) (*auth.User, error) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.u == nil {
		return nil, errors.New("revoked")
	}
	return f.u, nil
}
func testService(t *testing.T) (*Service, *fakeSessions) {
	t.Helper()
	f := &fakeSessions{u: &auth.User{Role: "admin", Recent: true}}
	s, err := New(f, "https://backend.example.com")
	if err != nil {
		t.Fatal(err)
	}
	return s, f
}
func issueTicket(t *testing.T, s *Service) string {
	t.Helper()
	a, err := s.Issue(context.Background(), "session-secret")
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(a.URL)
	return strings.TrimPrefix(u.Fragment, "ticket=")
}
func exchange(t *testing.T, s *Service, ticket, origin string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest("POST", "/browser/session", strings.NewReader(`{"ticket":"`+ticket+`"}`))
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func TestAccessRequiresRecentUnblockedAdministrator(t *testing.T) {
	s, f := testService(t)
	for _, u := range []*auth.User{nil, {Role: "customer", Recent: true}, {Role: "staff", Recent: true, Permissions: []string{"settings"}}, {Role: "admin"}, {Role: "admin", Recent: true, Blocked: true}, {Role: "admin", Recent: true, MustChange: true}} {
		f.u = u
		if _, err := s.Issue(context.Background(), "secret"); err == nil {
			t.Fatalf("allowed %+v", u)
		}
	}
}
func TestOneTimeTicketCookieAndLogout(t *testing.T) {
	s, _ := testService(t)
	ticket := issueTicket(t, s)
	if w := exchange(t, s, ticket, "https://evil.example"); w.Code != 403 {
		t.Fatal(w.Code)
	}
	w := exchange(t, s, ticket, s.origin)
	if w.Code != 204 {
		t.Fatal(w.Code, w.Body.String())
	}
	c := w.Result().Cookies()[0]
	if !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/browser/" || c.MaxAge != 600 {
		t.Fatal(c)
	}
	if w := exchange(t, s, ticket, s.origin); w.Code != 401 {
		t.Fatal("replayed ticket", w.Code)
	}
	r := httptest.NewRequest("GET", "/browser/screen", nil)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || w.Header().Get("Cache-Control") != "no-store" || !strings.Contains(w.Body.String(), "Chrome") {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("POST", "/browser/logout", nil)
	r.Header.Set("Origin", s.origin)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	r = httptest.NewRequest("GET", "/browser/screen", nil)
	r.AddCookie(c)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("logout did not revoke", w.Code)
	}
}
func TestExpiryAndRevokedOriginalSession(t *testing.T) {
	s, f := testService(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	ticket := issueTicket(t, s)
	now = now.Add(time.Minute)
	if w := exchange(t, s, ticket, s.origin); w.Code != 401 {
		t.Fatal("expired ticket", w.Code)
	}
	ticket = issueTicket(t, s)
	w := exchange(t, s, ticket, s.origin)
	c := w.Result().Cookies()[0]
	r := httptest.NewRequest("GET", "/browser/screen", nil)
	r.AddCookie(c)
	f.u = nil
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("revoked original session", w.Code)
	}
	f.u = &auth.User{Role: "admin", Recent: true}
	now = now.Add(10 * time.Minute)
	w = httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("expired browser session", w.Code)
	}
}
func TestNoPublicDisplayAndWebSocketOrigin(t *testing.T) {
	s, _ := testService(t)
	for _, p := range []string{"/browser/screen", "/browser/view/core/rfb.js", "/browser/view/websockify"} {
		w := httptest.NewRecorder()
		s.ServeHTTP(w, httptest.NewRequest("GET", p, nil))
		if w.Code != 401 {
			t.Fatalf("public %s: %d", p, w.Code)
		}
	}
	ticket := issueTicket(t, s)
	c := exchange(t, s, ticket, s.origin).Result().Cookies()[0]
	r := httptest.NewRequest("GET", "/browser/view/websockify", nil)
	r.AddCookie(c)
	r.Header.Set("Upgrade", "websocket")
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestProxyStripsCredentialsAndUsesFixedUpstream(t *testing.T) {
	s, _ := testService(t)
	s.bridgePassword = "bridge-fixture"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, password, ok := r.BasicAuth()
		if !ok || username != "hoanxu" || password != "bridge-fixture" {
			t.Error("missing private bridge authentication")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("X-Forwarded-Host") != "" {
			t.Error("credentials forwarded")
		}
		if r.URL.Path != "/core/rfb.js" {
			t.Error(r.URL.Path)
		}
		_, _ = w.Write([]byte("fixture"))
	}))
	defer upstream.Close()
	// Route the fixed loopback target through the fixture transport.
	s.proxy.Transport = &fixtureTransport{target: upstream.URL}
	c := exchange(t, s, issueTicket(t, s), s.origin).Result().Cookies()[0]
	r := httptest.NewRequest("GET", "/browser/view/core/rfb.js", nil)
	r.AddCookie(c)
	r.Header.Set("Authorization", "private")
	r.Header.Set("X-Forwarded-Host", "evil.example")
	r.Header.Set("Origin", s.origin)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "fixture" {
		t.Fatal(w.Code, w.Body.String())
	}
}

type fixtureTransport struct{ target string }

func (f *fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "127.0.0.1:6080" {
		return nil, errors.New("unexpected upstream")
	}
	u, _ := url.Parse(f.target)
	r.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(r)
}
func TestOriginConfiguration(t *testing.T) {
	for _, origin := range []string{"", "http://example.com", "https://example.com/path", "https://user:pass@example.com", "https://example.com?x=1"} {
		if _, err := New(&fakeSessions{}, origin); err == nil {
			t.Fatal(origin)
		}
	}
	for _, origin := range []string{"https://backend.example.com", "http://localhost:8080", "http://127.0.0.1:8080"} {
		if _, err := New(&fakeSessions{}, origin); err != nil {
			t.Fatal(origin, err)
		}
	}
}

func TestUpgradedWebSocketClosesOnExpiryAndLogout(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint("revoke=", revoke), func(t *testing.T) {
			s, f := testService(t)
			s.checkInterval = 20 * time.Millisecond
			if !revoke {
				s.sessionTTL = 200 * time.Millisecond
			}
			upstreamClosed := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, rw, err := w.(http.Hijacker).Hijack()
				if err != nil {
					t.Error(err)
					return
				}
				defer conn.Close()
				defer close(upstreamClosed)
				_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
				_ = rw.Flush()
				_, _ = io.Copy(io.Discard, conn)
			}))
			defer upstream.Close()
			s.proxy.Transport = &fixtureTransport{target: upstream.URL}
			cookie := exchange(t, s, issueTicket(t, s), s.origin).Result().Cookies()[0]
			server := httptest.NewServer(s)
			defer server.Close()
			conn, err := net.Dial("tcp", strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, _ = fmt.Fprintf(conn, "GET /browser/view/websockify HTTP/1.1\r\nHost: backend.example.com\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nOrigin: %s\r\nCookie: hx_browser=%s\r\n\r\n", s.origin, cookie.Value)
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != 101 {
				t.Fatal(response.StatusCode)
			}
			if revoke {
				f.mu.Lock()
				f.u = nil
				f.mu.Unlock()
			}
			select {
			case <-upstreamClosed:
			case <-time.After(2 * time.Second):
				t.Fatal("upgraded socket survived expired/revoked session")
			}
		})
	}
}
