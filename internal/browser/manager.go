package browser

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
	"net/url"
	"strings"
	"sync"
	"time"
)

type job struct {
	ctx     context.Context
	item    string
	result  chan result
	started chan struct{}
}
type result struct {
	Body []byte
	Err  error
}
type Manager struct {
	mu              sync.Mutex
	sessionMu       sync.RWMutex
	lifetime        context.Context
	autoStart       bool
	headless        bool
	cookies         *CookieStore
	version         uint64
	starts          int
	lastVerified    *time.Time
	state           string
	probeURL        string
	offerBaseURL    string
	responseHost    string
	root            context.Context
	cancel          context.CancelFunc
	allocatorCancel context.CancelFunc
	queue           chan job
	path, profile   string
	authenticated   bool
}

func New(path, profile string) *Manager {
	return &Manager{path: path, profile: profile, queue: make(chan job, 50), lifetime: context.Background(), autoStart: true, headless: true, state: "not_started", probeURL: "https://affiliate.shopee.vn/dashboard", offerBaseURL: "https://affiliate.shopee.vn/offer/product_offer/", responseHost: "affiliate.shopee.vn"}
}
func NewLazy(path, profile string, cookies *CookieStore) *Manager {
	m := New(path, profile)
	m.autoStart = false
	m.cookies = cookies
	return m
}

type Option func(*Manager)

// WithHeadless configures the managed browser before it starts.
func WithHeadless(headless bool) Option {
	return func(m *Manager) { m.headless = headless }
}

func NewManaged(path, profile string, cookies *CookieStore, enabled bool, options ...Option) *Manager {
	m := NewLazy(path, profile, cookies)
	m.autoStart = enabled
	for _, option := range options {
		option(m)
	}
	return m
}
func (m *Manager) CookiesConfigured() bool { return m.cookies.Configured() }
func (m *Manager) SessionVersion() uint64  { m.mu.Lock(); defer m.mu.Unlock(); return m.version }
func (m *Manager) start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running() {
		return nil
	}
	if m.cancel != nil {
		m.cancel()
	}
	if m.allocatorCancel != nil {
		m.allocatorCancel()
	}
	opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
	opts = append(opts, chromedp.UserDataDir(m.profile), chromedp.WSURLReadTimeout(10*time.Second), chromedp.Flag("headless", m.headless))
	if m.path != "" {
		opts = append(opts, chromedp.ExecPath(m.path))
	}
	alloc, ac := chromedp.NewExecAllocator(ctx, opts...)
	root, rc := chromedp.NewContext(alloc)
	if e := chromedp.Run(root); e != nil {
		rc()
		ac()
		return e
	}
	m.root = root
	m.cancel = rc
	m.allocatorCancel = ac
	m.starts++
	m.authenticated = false
	m.state = "checking"
	if m.cookies != nil {
		cookies, e := m.cookies.Load()
		if e != nil {
			m.state = "cookie_storage_error"
		} else if len(cookies) > 0 {
			if e = chromedp.Run(root, network.SetCookies(cookies)); e != nil {
				m.state = "cookie_storage_error"
			}
		}
	}
	if !m.headless {
		// Keep the root tab open for manual login/verification. Probe and check
		// tabs can close without interrupting the user's interactive tab.
		if e := chromedp.Run(root, chromedp.ActionFunc(func(c context.Context) error {
			_, _, navigationError, _, e := page.Navigate(m.probeURL).Do(c)
			if e == nil && navigationError != "" {
				return errors.New("BROWSER_UNAVAILABLE")
			}
			return e
		})); e != nil {
			return e
		}
	}
	return nil
}

// A context may remain alive after the Chrome process exits; also check the CDP connection.
func (m *Manager) running() bool {
	if m.root == nil || m.root.Err() != nil {
		return false
	}
	c := chromedp.FromContext(m.root)
	if c == nil || c.Browser == nil {
		return false
	}
	select {
	case <-c.Browser.LostConnection:
		return false
	default:
		return true
	}
}
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.lifetime = ctx
	m.mu.Unlock()
	defer m.Close()
	for i := 0; i < 2; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-m.queue:
					if j.ctx.Err() != nil {
						continue
					}
					close(j.started)
					b, e := m.capture(ctx, j.ctx, j.item)
					if e != nil && j.ctx.Err() == nil && e.Error() != "SHOPEE_LOGIN_REQUIRED" && e.Error() != "SHOPEE_VERIFICATION_REQUIRED" {
						if deadline, ok := j.ctx.Deadline(); ok && time.Until(deadline) > 20*time.Second {
							b, e = m.capture(ctx, j.ctx, j.item)
						}
					}
					j.result <- result{b, e}
				}
			}
		}()
	}
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	if m.autoStart || m.CookiesConfigured() {
		m.probe(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.autoStart || m.CookiesConfigured() || m.Status()["browser"] == true {
				m.probe(ctx)
			}
		}
	}
}
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cancel != nil {
		m.cancel()
	}
	if m.allocatorCancel != nil {
		m.allocatorCancel()
	}
}
func (m *Manager) Status() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{"authenticated": m.authenticated, "pending": len(m.queue), "workers": 2, "browser": m.running(), "savedCookies": m.CookiesConfigured(), "state": m.state, "lastVerifiedAt": m.lastVerified, "starts": m.starts}
}
func (m *Manager) probe(ctx context.Context) {
	m.sessionMu.RLock()
	defer m.sessionMu.RUnlock()
	m.probeSession(ctx)
}
func (m *Manager) probeSession(ctx context.Context) {
	m.mu.Lock()
	life := m.lifetime
	m.mu.Unlock()
	if e := m.start(life); e != nil {
		m.mu.Lock()
		m.authenticated = false
		m.state = "unavailable"
		m.mu.Unlock()
		return
	}
	m.mu.Lock()
	root := m.root
	m.mu.Unlock()
	tab, cancel := chromedp.NewContext(root)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-tab.Done():
		}
	}()
	tab, timeout := context.WithTimeout(tab, 20*time.Second)
	defer timeout()
	var location string
	var locationMu sync.Mutex
	var latestLocation string
	chromedp.ListenTarget(tab, func(event any) {
		if e, ok := event.(*page.EventFrameNavigated); ok && e.Frame.ParentID == "" {
			locationMu.Lock()
			latestLocation = e.Frame.URL
			locationMu.Unlock()
		}
	})
	e := chromedp.Run(tab, chromedp.Navigate(m.probeURL), chromedp.WaitReady("body"), chromedp.Location(&location))
	if location == "" {
		locationMu.Lock()
		location = latestLocation
		locationMu.Unlock()
	}
	state := sessionLocationState(location, m.probeURL)
	m.mu.Lock()
	m.authenticated = e == nil && state == "authenticated"
	now := time.Now().UTC()
	m.lastVerified = &now
	if state == "verification_required" {
		m.state = state
	} else if e != nil {
		m.state = "unavailable"
	} else if m.authenticated {
		m.state = "authenticated"
	} else {
		m.state = "login_required"
	}
	m.mu.Unlock()
}
func (m *Manager) RefreshSession(ctx context.Context) map[string]any {
	m.mu.Lock()
	life := m.lifetime
	m.mu.Unlock()
	// Browser lifetime is independent of the HTTP request which triggers the probe.
	m.sessionMu.RLock()
	defer m.sessionMu.RUnlock()
	if e := m.start(life); e != nil {
		m.mu.Lock()
		m.authenticated = false
		m.state = "unavailable"
		m.mu.Unlock()
		return m.Status()
	}
	m.probeSession(ctx)
	return m.Status()
}
func (m *Manager) PasteCookies(ctx context.Context, raw string) (map[string]any, error) {
	cookies, e := ParseCookies(raw)
	if e != nil {
		return nil, e
	}
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	m.mu.Lock()
	life := m.lifetime
	m.mu.Unlock()
	if e = m.start(life); e != nil {
		return nil, errors.New("BROWSER_UNAVAILABLE")
	}
	m.mu.Lock()
	root := m.root
	m.mu.Unlock()
	// Apply to the same default browser context shared by checker tabs.
	tab, cancel := chromedp.NewContext(root)
	defer cancel()
	go func() {
		select {
		case <-ctx.Done():
			cancel()
		case <-tab.Done():
		}
	}()
	if e = chromedp.Run(tab, network.Enable(), network.SetCookies(cookies)); e != nil {
		return nil, errors.New("BROWSER_UNAVAILABLE")
	}
	m.mu.Lock()
	m.version++
	m.authenticated = false
	m.state = "checking"
	m.mu.Unlock()
	if e = m.cookies.Save(strings.TrimSpace(raw)); e != nil {
		return nil, e
	}
	m.probeSession(ctx)
	return m.Status(), nil
}
func (m *Manager) Check(ctx context.Context, item string) ([]byte, error) {
	m.mu.Lock()
	authenticated := m.authenticated
	state := m.state
	m.mu.Unlock()
	if !authenticated {
		if state == "verification_required" {
			return nil, errors.New("SHOPEE_VERIFICATION_REQUIRED")
		}
		if state == "unavailable" {
			return nil, errors.New("BROWSER_UNAVAILABLE")
		}
		if state == "cookie_storage_error" {
			return nil, errors.New("SHOPEE_COOKIE_STORAGE_ERROR")
		}
		return nil, errors.New("SHOPEE_LOGIN_REQUIRED")
	}
	request, cancel := context.WithCancel(ctx)
	defer cancel()
	j := job{ctx: request, item: item, result: make(chan result, 1), started: make(chan struct{})}
	select {
	case m.queue <- j:
	default:
		return nil, errors.New("QUEUE_FULL")
	}
	wait := time.NewTimer(10 * time.Second)
	defer wait.Stop()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-wait.C:
		return nil, errors.New("QUEUE_TIMEOUT")
	case <-j.started:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-j.result:
		return r.Body, r.Err
	}
}
func (m *Manager) capture(lifetime, request context.Context, item string) ([]byte, error) {
	m.sessionMu.RLock()
	defer m.sessionMu.RUnlock()
	if e := m.start(lifetime); e != nil {
		return nil, e
	}
	m.mu.Lock()
	root := m.root
	m.mu.Unlock()
	tab, cancel := chromedp.NewContext(root)
	defer cancel()
	tab, stop := context.WithTimeout(tab, 20*time.Second)
	defer stop()
	go func() {
		select {
		case <-request.Done():
			cancel()
		case <-tab.Done():
		}
	}()
	ids := make(chan network.RequestID, 8)
	expired := make(chan struct{}, 1)
	var mu sync.Mutex
	targets := map[network.RequestID]bool{}
	chromedp.ListenTarget(tab, func(ev any) {
		switch e := ev.(type) {
		case *page.EventFrameNavigated:
			if e.Frame.ParentID == "" && sessionLocationState(e.Frame.URL, m.offerBaseURL+item) == "verification_required" {
				m.mu.Lock()
				m.authenticated = false
				m.state = "verification_required"
				m.mu.Unlock()
				select {
				case expired <- struct{}{}:
				default:
				}
			}
		case *network.EventResponseReceived:
			u, er := url.Parse(e.Response.URL)
			if er == nil && u.Host == m.responseHost && strings.HasPrefix(u.Path, "/api/") && (e.Response.Status == 401 || e.Response.Status == 403) {
				select {
				case expired <- struct{}{}:
				default:
				}
			}
			if er == nil && u.Host == m.responseHost && u.Path == "/api/v3/offer/product" && u.Query().Get("item_id") == item && e.Response.Status == 200 {
				mu.Lock()
				targets[e.RequestID] = true
				mu.Unlock()
			}
		case *network.EventLoadingFinished:
			mu.Lock()
			ok := targets[e.RequestID]
			delete(targets, e.RequestID)
			mu.Unlock()
			if ok {
				select {
				case ids <- e.RequestID:
				default:
				}
			}
		}
	})
	if e := chromedp.Run(tab, network.Enable(), chromedp.Navigate(m.offerBaseURL+item)); e != nil {
		m.mu.Lock()
		verification := m.state == "verification_required"
		m.mu.Unlock()
		if verification {
			return nil, errors.New("SHOPEE_VERIFICATION_REQUIRED")
		}
		return nil, e
	}
	var location string
	if e := chromedp.Run(tab, chromedp.Location(&location)); e == nil {
		if sessionLocationState(location, m.offerBaseURL+item) == "verification_required" {
			m.mu.Lock()
			m.authenticated = false
			m.state = "verification_required"
			m.mu.Unlock()
			return nil, errors.New("SHOPEE_VERIFICATION_REQUIRED")
		}
		u, _ := url.Parse(location)
		if u == nil || u.Host != m.responseHost || strings.Contains(u.Path, "login") {
			m.mu.Lock()
			m.authenticated = false
			m.state = "login_required"
			m.mu.Unlock()
			return nil, errors.New("SHOPEE_LOGIN_REQUIRED")
		}
	}
	for {
		select {
		case <-expired:
			m.mu.Lock()
			if m.state == "verification_required" {
				m.mu.Unlock()
				return nil, errors.New("SHOPEE_VERIFICATION_REQUIRED")
			}
			m.authenticated = false
			m.state = "login_required"
			m.mu.Unlock()
			return nil, errors.New("SHOPEE_LOGIN_REQUIRED")
		case <-tab.Done():
			return nil, tab.Err()
		case id := <-ids:
			var body []byte
			e := chromedp.Run(tab, chromedp.ActionFunc(func(c context.Context) error { var er error; body, er = network.GetResponseBody(id).Do(c); return er }))
			if e != nil {
				return nil, e
			}
			if !json.Valid(body) || len(body) > 2*1024*1024 {
				return nil, errors.New("SHOPEE_RESPONSE_INVALID")
			}
			return body, nil
		}
	}
}

// A Shopee verification redirect is not evidence that the affiliate login expired.
func sessionLocationState(location, expected string) string {
	u, err := url.Parse(location)
	if err != nil || u == nil {
		return "login_required"
	}
	goal, _ := url.Parse(expected)
	if goal == nil {
		return "login_required"
	}
	if (u.Host == goal.Host || u.Host == "shopee.vn" || u.Host == "www.shopee.vn" || u.Host == "affiliate.shopee.vn") && (strings.HasPrefix(u.Path, "/verify/") || strings.HasPrefix(u.Path, "/captcha")) {
		return "verification_required"
	}
	if u.Host == goal.Host && u.Path == goal.Path {
		return "authenticated"
	}
	return "login_required"
}
