package browser

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
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
	manualLogin     bool
	cookies         *CookieStore
	version         uint64
	starts          int
	lastVerified    *time.Time
	lastFailure     *LastFailure
	state           string
	probeURL        string
	offerBaseURL    string
	responseHost    string
	remoteURL       string
	retryAt         time.Time
	retryDelay      time.Duration
	ownedTargets    map[target.ID]bool
	root            context.Context
	cancel          context.CancelFunc
	allocatorCancel context.CancelFunc
	queue           chan job
	workerTabs      chan *workerTab
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

// Manual mode waits for the administrator to open Chrome. It never imports or
// exports cookies; authentication belongs to the normal Chrome profile.
func NewManual(path, profile string, options ...Option) *Manager {
	m := NewManaged(path, profile, nil, false, WithHeadless(false))
	m.manualLogin = true
	// Local and remote manual browsers share the same two reusable workers.
	// The root tab stays separate for administrator login/verification.
	m.workerTabs = make(chan *workerTab, 2)
	m.workerTabs <- nil
	m.workerTabs <- nil
	for _, option := range options {
		option(m)
	}
	return m
}
func (m *Manager) UsesManualLogin() bool   { return m.manualLogin }
func (m *Manager) CookiesConfigured() bool { return m.cookies.Configured() }
func (m *Manager) SessionVersion() uint64  { m.mu.Lock(); defer m.mu.Unlock(); return m.version }

// OpenInteractive starts local Chrome or connects to the EC2 browser without
// requiring a Shopee session: the administrator needs its display to log in.
func (m *Manager) OpenInteractive() error {
	m.mu.Lock()
	life, headless := m.lifetime, m.headless
	m.mu.Unlock()
	if headless {
		return errors.New("interactive browser requires CHROME_HEADLESS=false")
	}
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	err := m.start(life)
	if err != nil {
		// An unreachable dashboard must not prevent manual recovery when
		// Chromium itself started successfully and its window is available.
		m.mu.Lock()
		running := m.running()
		m.mu.Unlock()
		if running {
			return nil
		}
	}
	return err
}
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
	var alloc context.Context
	var ac context.CancelFunc
	if m.remoteURL != "" {
		if time.Now().Before(m.retryAt) {
			return errors.New("BROWSER_UNAVAILABLE")
		}
		ws, err := remoteWebSocket(ctx, m.remoteURL)
		if err != nil {
			m.connectionFailed()
			return errors.New("BROWSER_UNAVAILABLE")
		}
		alloc, ac = chromedp.NewRemoteAllocator(ctx, ws, chromedp.NoModifyURL)
	} else {
		opts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
		opts = append(opts, chromedp.UserDataDir(m.profile), chromedp.WSURLReadTimeout(10*time.Second), chromedp.Flag("headless", m.headless))
		if m.path != "" {
			opts = append(opts, chromedp.ExecPath(m.path))
		}
		alloc, ac = chromedp.NewExecAllocator(ctx, opts...)
	}
	root, rc := chromedp.NewContext(alloc)
	// Bound the first connection without putting a deadline on its lifetime.
	connectTimeout := 10 * time.Second
	if m.remoteURL != "" {
		// Remote target setup crosses the SSH tunnel several times. Allow for
		// WAN latency and a cold renderer while keeping reconnects bounded.
		connectTimeout = 30 * time.Second
	}
	timer := time.AfterFunc(connectTimeout, func() { ac() })
	if e := chromedp.Run(root); e != nil {
		timer.Stop()
		rc()
		ac()
		if m.remoteURL != "" {
			m.connectionFailed()
			return errors.New("BROWSER_UNAVAILABLE")
		}
		return e
	}
	timer.Stop()
	if m.remoteURL != "" {
		// A broken tunnel cannot close old targets. On reattach, clean up only
		// targets this controller created; never touch the native login tab.
		cleanup, stopCleanup := context.WithTimeout(root, 5*time.Second)
		browserCtx := cdp.WithExecutor(cleanup, chromedp.FromContext(root).Browser)
		for id := range m.ownedTargets {
			_ = target.CloseTarget(id).Do(browserCtx)
		}
		stopCleanup()
		m.ownedTargets = map[target.ID]bool{chromedp.FromContext(root).Target.TargetID: true}
	}
	m.root = root
	m.cancel = rc
	m.allocatorCancel = ac
	m.retryDelay = 0
	m.retryAt = time.Time{}
	m.starts++
	if m.manualLogin {
		m.version++
	}
	m.authenticated = false
	m.state = "checking"
	if m.cookies != nil {
		cookies, e := m.cookies.LoadContext(ctx)
		if e != nil {
			m.state = "cookie_storage_error"
		} else if len(cookies) > 0 {
			if e = chromedp.Run(root, network.SetCookies(cookies)); e != nil {
				m.state = "cookie_storage_error"
			}
		}
	}
	if !m.headless && m.remoteURL == "" {
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
	if m.remoteURL != "" {
		go func() {
			select {
			case <-root.Done():
			case <-chromedp.FromContext(root).Browser.LostConnection:
			}
			m.mu.Lock()
			defer m.mu.Unlock()
			if m.root == root {
				m.authenticated = false
				m.state = "unavailable"
			}
		}()
	}
	return nil
}

// connectionFailed is called with mu held. EC2 outages never restart the API.
func (m *Manager) connectionFailed() {
	m.authenticated = false
	m.state = "unavailable"
	if m.retryDelay == 0 {
		m.retryDelay = 5 * time.Second
	} else {
		m.retryDelay *= 2
		if m.retryDelay > 30*time.Second {
			m.retryDelay = 30 * time.Second
		}
	}
	m.retryAt = time.Now().Add(m.retryDelay)
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
	var workers sync.WaitGroup
	defer func() {
		workers.Wait()
		m.Close()
	}()
	for i := 0; i < 2; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case j := <-m.queue:
					if j.ctx.Err() != nil {
						continue
					}
					close(j.started)
					b, e := m.captureAttempt(ctx, j.ctx, j.item, 1)
					var f *Failure
					if errors.As(e, &f) && f.Retryable && j.ctx.Err() == nil {
						if deadline, ok := j.ctx.Deadline(); ok && time.Until(deadline) >= 5*time.Second {
							b, e = m.captureAttempt(ctx, j.ctx, j.item, 2)
						}
					}
					j.result <- result{b, e}
				}
			}
		}()
	}
	interval := 10 * time.Minute
	if m.remoteURL != "" {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	if m.autoStart || m.CookiesConfigured() {
		m.probe(ctx)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if m.remoteURL != "" {
				m.mu.Lock()
				due := !m.running() || m.state == "unavailable" || m.lastVerified == nil || time.Since(*m.lastVerified) >= 10*time.Minute
				m.mu.Unlock()
				if !due {
					continue
				}
			}
			if m.autoStart || m.CookiesConfigured() || m.Status()["browser"] == true {
				m.probe(ctx)
			}
		}
	}
}
func (m *Manager) Close() {
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
	m.closeWorkerTabs()
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
	if (m.remoteURL != "" || m.root != nil) && !m.running() {
		m.authenticated = false
		m.state = "unavailable"
	}
	return map[string]any{"authenticated": m.authenticated, "pending": len(m.queue), "workers": 2, "browser": m.running(), "savedCookies": m.CookiesConfigured(), "state": m.state, "lastVerifiedAt": m.lastVerified, "starts": m.starts, "lastFailure": m.lastFailure}
}
func (m *Manager) probe(ctx context.Context) {
	// Probes and interactive setup wait for both worker tabs to finish.
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
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
	var w *workerTab
	var tab context.Context
	if m.workerTabs != nil {
		var err error
		w, err = m.acquireWorker(root, ctx)
		if err != nil {
			return
		}
		tab = w.ctx
	} else {
		var cancel context.CancelFunc
		tab, cancel = chromedp.NewContext(root)
		defer func() { cancel(); m.forgetTarget(tab) }()
	}
	tab, timeout := context.WithTimeout(tab, 20*time.Second)
	defer timeout()
	stopRequest := context.AfterFunc(ctx, timeout)
	defer stopRequest()
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
	e := chromedp.Run(tab, m.trackTarget(), chromedp.Navigate(m.probeURL), chromedp.WaitReady("body"), chromedp.Location(&location))
	if location == "" {
		locationMu.Lock()
		location = latestLocation
		locationMu.Unlock()
	}
	state := sessionLocationState(location, m.probeURL)
	if w != nil {
		w.ready = e == nil && state == "authenticated"
		w.item = ""
		m.releaseWorker(w, w.ready)
		if e == nil && state == "authenticated" {
			m.prewarmWorkers(ctx)
		}
	}
	var cookieSaveError error
	if e == nil && state == "authenticated" && m.cookies != nil && m.cookies.Codec != nil {
		// Capture manually refreshed login cookies too, so container restarts
		// restore the current session instead of an older pasted export.
		cookieSaveError = m.saveSessionCookies(tab)
	}
	m.mu.Lock()
	wasAuthenticated := m.authenticated
	m.authenticated = e == nil && state == "authenticated"
	if m.manualLogin && m.authenticated && !wasAuthenticated {
		m.version++
	}
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
	if cookieSaveError != nil {
		m.state = "cookie_storage_error"
	}
	m.mu.Unlock()
}

// saveSessionCookies runs while the shared session lock is held. Only Shopee
// cookies are exported; values remain encrypted outside the source tree.
func (m *Manager) saveSessionCookies(ctx context.Context) error {
	var current []*network.Cookie
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(c context.Context) error {
		var err error
		current, err = network.GetCookies().WithURLs([]string{"https://shopee.vn/", "https://affiliate.shopee.vn/dashboard", "https://affiliate.shopee.vn/offer/product_offer/"}).Do(c)
		return err
	}))
	if err != nil {
		return errors.New("COOKIE_STORAGE_FAILED")
	}
	var exported []exportedCookie
	for _, cookie := range current {
		host := strings.TrimPrefix(strings.ToLower(cookie.Domain), ".")
		if host != "shopee.vn" && host != "affiliate.shopee.vn" || cookie.PartitionKey != nil || cookie.PartitionKeyOpaque {
			continue
		}
		exported = append(exported, exportedCookie{Name: cookie.Name, Value: cookie.Value, Domain: cookie.Domain, Path: cookie.Path, Secure: cookie.Secure, HTTPOnly: cookie.HTTPOnly, HostOnly: !strings.HasPrefix(cookie.Domain, "."), SameSite: string(cookie.SameSite), Expires: cookie.Expires, Session: cookie.Session})
	}
	if len(exported) == 0 {
		return nil
	}
	raw, err := json.Marshal(exported)
	if err != nil {
		return errors.New("COOKIE_STORAGE_FAILED")
	}
	// Use the same validated format as manual exports, including size limits.
	if _, err = ParseCookies(string(raw)); err != nil {
		return errors.New("COOKIE_STORAGE_FAILED")
	}
	return m.cookies.SaveContext(ctx, string(raw))
}
func (m *Manager) RefreshSession(ctx context.Context) map[string]any {
	m.mu.Lock()
	life := m.lifetime
	if m.manualLogin && m.remoteURL == "" && !m.running() {
		m.authenticated = false
		m.state = "login_required"
		m.mu.Unlock()
		return m.Status()
	}
	m.mu.Unlock()
	// Browser lifetime is independent of the HTTP request which triggers the probe.
	m.sessionMu.Lock()
	defer m.sessionMu.Unlock()
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
	if m.manualLogin {
		return nil, errors.New("COOKIE_IMPORT_REMOVED")
	}
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
	if e = m.cookies.SaveContext(ctx, strings.TrimSpace(raw)); e != nil {
		m.mu.Lock()
		m.state = "cookie_storage_error"
		m.mu.Unlock()
		return nil, e
	}
	m.probeSession(ctx)
	return m.Status(), nil
}
func (m *Manager) Check(ctx context.Context, item string) ([]byte, error) {
	m.mu.Lock()
	if (m.remoteURL != "" || m.root != nil) && !m.running() {
		m.authenticated = false
		m.state = "unavailable"
	}
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
	request, cancel := context.WithTimeout(ctx, 40*time.Second)
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
	case <-request.Done():
		return nil, request.Err()
	case <-wait.C:
		return nil, errors.New("QUEUE_TIMEOUT")
	case <-j.started:
	}
	select {
	case <-request.Done():
		return nil, request.Err()
	case r := <-j.result:
		return r.Body, r.Err
	}
}

func (m *Manager) trackTarget() chromedp.Action {
	return chromedp.ActionFunc(func(ctx context.Context) error {
		if m.remoteURL != "" {
			m.mu.Lock()
			m.ownedTargets[chromedp.FromContext(ctx).Target.TargetID] = true
			m.mu.Unlock()
		}
		return nil
	})
}

func (m *Manager) forgetTarget(ctx context.Context) {
	if m.remoteURL == "" {
		return
	}
	c := chromedp.FromContext(ctx)
	if c == nil || c.Target == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	select {
	case <-c.Browser.LostConnection:
		// Keep the ID so the next connection can close this orphaned tab.
	default:
		delete(m.ownedTargets, c.Target.TargetID)
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
