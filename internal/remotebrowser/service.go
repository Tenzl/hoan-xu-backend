// Package remotebrowser grants short-lived, administrator-only access to the
// headed Chromium display. VNC and its WebSocket bridge stay on loopback.
package remotebrowser

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"hoanxu/internal/auth"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"
)

//go:embed assets/*
var assets embed.FS

type Sessions interface {
	Session(context.Context, string) (*auth.User, error)
}
type grant struct {
	token   string
	expires time.Time
}
type Access struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expiresAt"`
}
type Service struct {
	sessions              Sessions
	origin                string
	secure                bool
	proxy                 *httputil.ReverseProxy
	mu                    sync.Mutex
	tickets, grants       map[[32]byte]grant
	now                   func() time.Time
	ticketTTL, sessionTTL time.Duration
	checkInterval         time.Duration
	bridgePassword        string
}

type Option func(*Service)

func WithBridgePassword(password string) Option {
	return func(s *Service) { s.bridgePassword = password }
}

func New(sessions Sessions, origin string, options ...Option) (*Service, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))) {
		return nil, errors.New("REMOTE_BROWSER_ORIGIN must be an HTTPS origin (HTTP is allowed only on localhost)")
	}
	upstream, _ := url.Parse("http://127.0.0.1:6080")
	s := &Service{sessions: sessions, origin: origin, secure: u.Scheme == "https", tickets: make(map[[32]byte]grant), grants: make(map[[32]byte]grant), now: time.Now, ticketTTL: time.Minute, sessionTTL: 10 * time.Minute, checkInterval: 15 * time.Second}
	for _, option := range options {
		option(s)
	}
	s.proxy = &httputil.ReverseProxy{Rewrite: func(p *httputil.ProxyRequest) {
		p.SetURL(upstream)
		p.Out.URL.Path = strings.TrimPrefix(p.In.URL.Path, "/browser/view")
		p.Out.Header.Del("Cookie")
		p.Out.Header.Del("Authorization")
		p.Out.Header.Del("Origin")
		if s.bridgePassword != "" {
			p.Out.SetBasicAuth("hoanxu", s.bridgePassword)
		}
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "Màn hình Chrome chưa sẵn sàng. Thử lại sau.", http.StatusBadGateway)
	}}
	return s, nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func (s *Service) authorized(ctx context.Context, token string) bool {
	u, err := s.sessions.Session(ctx, token)
	return err == nil && u != nil && u.Role == "admin" && !u.Blocked && !u.MustChange && u.Recent
}
func (s *Service) cleanup() {
	for _, m := range []map[[32]byte]grant{s.tickets, s.grants} {
		for key, g := range m {
			if !s.now().Before(g.expires) {
				delete(m, key)
			}
		}
	}
}
func (s *Service) Issue(ctx context.Context, token string) (Access, error) {
	if !s.authorized(ctx, token) {
		return Access{}, errors.New("administrator reauthentication required")
	}
	ticket, err := randomToken()
	if err != nil {
		return Access{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup()
	if len(s.tickets)+len(s.grants) >= 128 {
		return Access{}, errors.New("too many browser sessions")
	}
	expires := s.now().Add(s.ticketTTL)
	s.tickets[sha256.Sum256([]byte(ticket))] = grant{token, expires}
	return Access{s.origin + "/browser/#ticket=" + ticket, expires}, nil
}
func (s *Service) browserGrant(r *http.Request) (grant, bool) {
	cookie, err := r.Cookie("hx_browser")
	if err != nil {
		return grant{}, false
	}
	s.mu.Lock()
	s.cleanup()
	g, ok := s.grants[sha256.Sum256([]byte(cookie.Value))]
	s.mu.Unlock()
	return g, ok && s.authorized(r.Context(), g.token)
}
func (s *Service) setCookie(w http.ResponseWriter, token string, age int) {
	http.SetCookie(w, &http.Cookie{Name: "hx_browser", Value: token, Path: "/browser/", HttpOnly: true, Secure: s.secure, SameSite: http.SameSiteStrictMode, MaxAge: age})
}
func (s *Service) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	wsOrigin := strings.Replace(strings.Replace(s.origin, "https://", "wss://", 1), "http://", "ws://", 1)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self' "+wsOrigin+"; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if r.Method == "POST" && r.Header.Get("Origin") != s.origin {
		http.Error(w, "Origin không hợp lệ.", 403)
		return
	}
	if r.URL.Path == "/browser/session" && r.Method == "POST" {
		s.exchange(w, r)
		return
	}
	if r.URL.Path == "/browser/logout" && r.Method == "POST" {
		if cookie, err := r.Cookie("hx_browser"); err == nil {
			s.mu.Lock()
			delete(s.grants, sha256.Sum256([]byte(cookie.Value)))
			s.mu.Unlock()
		}
		s.setCookie(w, "", -1)
		w.WriteHeader(204)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "Method không hợp lệ.", 405)
		return
	}
	if r.URL.Path == "/browser/" {
		s.asset(w, "index.html", "text/html; charset=utf-8")
		return
	}
	if r.URL.Path == "/browser/bootstrap.js" {
		s.asset(w, "bootstrap.js", "text/javascript; charset=utf-8")
		return
	}
	g, ok := s.browserGrant(r)
	if !ok {
		http.Error(w, "Phiên Chrome đã hết hạn. Mở lại từ trang quản trị.", 401)
		return
	}
	switch r.URL.Path {
	case "/browser/screen":
		s.asset(w, "screen.html", "text/html; charset=utf-8")
	case "/browser/viewer.js":
		s.asset(w, "viewer.js", "text/javascript; charset=utf-8")
	case "/browser/style.css":
		s.asset(w, "style.css", "text/css; charset=utf-8")
	default:
		if !strings.HasPrefix(r.URL.Path, "/browser/view/") || r.URL.RawPath != "" || path.Clean(r.URL.Path) != r.URL.Path {
			http.NotFound(w, r)
			return
		}
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			if r.URL.Path != "/browser/view/websockify" || r.Header.Get("Origin") != s.origin {
				http.Error(w, "Origin không hợp lệ.", 403)
				return
			}
			ctx, cancel := context.WithDeadline(r.Context(), g.expires)
			defer cancel()
			// ReverseProxy closes upgraded connections when this context is canceled.
			go func() {
				tick := time.NewTicker(s.checkInterval)
				defer tick.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
						checkCtx, stop := context.WithTimeout(ctx, 3*time.Second)
						_, valid := s.browserGrant(r.WithContext(checkCtx))
						stop()
						if !valid {
							cancel()
							return
						}
					}
				}
			}()
			r = r.WithContext(ctx)
		}
		s.proxy.ServeHTTP(w, r)
	}
}
func (s *Service) asset(w http.ResponseWriter, name, kind string) {
	b, err := assets.ReadFile("assets/" + name)
	if err != nil {
		http.Error(w, "Không tải được màn hình.", 500)
		return
	}
	w.Header().Set("Content-Type", kind)
	_, _ = w.Write(b)
}
func (s *Service) exchange(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var body struct {
		Ticket string `json:"ticket"`
	}
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&body) != nil || d.Decode(&struct{}{}) != io.EOF || len(body.Ticket) != 43 {
		http.Error(w, "Vé truy cập không hợp lệ.", 400)
		return
	}
	s.mu.Lock()
	s.cleanup()
	key := sha256.Sum256([]byte(body.Ticket))
	g, ok := s.tickets[key]
	delete(s.tickets, key)
	s.mu.Unlock()
	if !ok || !s.authorized(r.Context(), g.token) {
		http.Error(w, "Vé truy cập đã hết hạn hoặc đã dùng.", 401)
		return
	}
	token, err := randomToken()
	if err != nil {
		http.Error(w, "Không tạo được phiên.", 500)
		return
	}
	s.mu.Lock()
	s.grants[sha256.Sum256([]byte(token))] = grant{g.token, s.now().Add(s.sessionTTL)}
	s.mu.Unlock()
	s.setCookie(w, token, int(s.sessionTTL.Seconds()))
	w.WriteHeader(204)
}
