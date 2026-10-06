package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const maxProductBody = 2 * 1024 * 1024

// Commands run on an initialized target; their deadlines never become its lifetime.
func boundedCommand(ctx context.Context, timeout time.Duration, actions ...chromedp.Action) error {
	c, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return chromedp.Run(c, actions...)
}

func (m *Manager) capture(lifetime, request context.Context, item string) ([]byte, error) {
	return m.captureAttempt(lifetime, request, item, 1)
}

func (m *Manager) captureAttempt(lifetime, request context.Context, item string, attempt int) (body []byte, captureErr error) {
	return m.captureAttemptWithLink(lifetime, request, item, attempt, nil)
}

func (m *Manager) captureAttemptWithLink(lifetime, request context.Context, item string, attempt int, link *offerLinkRequest) (body []byte, captureErr error) {
	started := time.Now()
	leaseCtx, stopLease := context.WithTimeout(request, 20*time.Second)
	defer stopLease()
	phase := "lease"
	var leaseTime, navigationTime, bodyTime time.Duration
	var mu sync.Mutex
	var loader cdp.LoaderID
	var frame cdp.FrameID
	var observed, pageOpened bool
	requestAt, headersAt, readyAt := int64(-1), int64(-1), int64(-1)
	defer func() {
		code := ""
		var f *Failure
		if errors.As(captureErr, &f) {
			code, phase = f.Code, f.Phase
			m.RecordFailure(code, phase)
		}
		mu.Lock()
		defer mu.Unlock()
		slog.Info("shopee_browser_capture", "phase", phase, "code", code, "attempt", attempt,
			"lease_ms", leaseTime.Milliseconds(), "navigation_ms", navigationTime.Milliseconds(),
			"product_request_after_ms", requestAt, "product_headers_after_ms", headersAt,
			"product_ready_after_ms", readyAt, "body_ms", bodyTime.Milliseconds(),
			"total_ms", time.Since(started).Milliseconds(), "success", captureErr == nil)
	}()
	// A session refresh must not leave a canceled job waiting on a mutex forever.
	for !m.sessionMu.TryRLock() {
		select {
		case <-leaseCtx.Done():
			return nil, failure("BROWSER_UNAVAILABLE", "lease", true)
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer m.sessionMu.RUnlock()
	if request.Err() != nil {
		return nil, request.Err()
	}
	m.mu.Lock()
	ready := m.running()
	m.mu.Unlock()
	if !ready {
		// A job must never silently start a new browser and assume it is logged in.
		return nil, failure("BROWSER_UNAVAILABLE", "lease", false)
	}
	m.mu.Lock()
	root := m.root
	m.mu.Unlock()
	// Bound lease creation as part of the capture budget, including queue/request cancellation.
	var w *workerTab
	var tab context.Context
	if m.workerTabs != nil {
		var err error
		w, err = m.acquireWorker(root, leaseCtx)
		if err != nil {
			return nil, failure("BROWSER_UNAVAILABLE", "lease", true)
		}
		tab = w.ctx
	} else {
		var err error
		w, err = m.newWorker(root, leaseCtx)
		if err != nil {
			return nil, failure("BROWSER_UNAVAILABLE", "lease", true)
		}
		tab = w.ctx
	}
	leaseTime = time.Since(started)
	keep := false
	defer func() {
		w.ready, w.item = keep, item
		if m.workerTabs != nil {
			m.releaseWorker(w, keep)
		} else {
			w.cancel()
			m.forgetTarget(w.ctx)
		}
		if !keep {
			code := "CANCELED"
			if captureErr != nil {
				code = captureErr.Error()
			}
			slog.Info("shopee_worker_replaced", "attempt", attempt, "reason", code, "phase", phase)
		}
		m.mu.Lock()
		if !m.running() {
			m.authenticated = false
			m.state = "unavailable"
		}
		m.mu.Unlock()
	}()
	deadline, _ := leaseCtx.Deadline()
	scope, cancel := context.WithDeadline(tab, deadline)
	defer cancel()
	stopRequest := context.AfterFunc(request, cancel)
	defer stopRequest()
	type capturedResponse struct {
		id     network.RequestID
		loader cdp.LoaderID
	}
	ids := make(chan capturedResponse, 8)
	failures := make(chan *Failure, 1)
	// Requests are correlated by navigation loader, not just product ID. This also
	// rejects a previous document's late response when checking the same item twice.
	type response struct {
		loader   cdp.LoaderID
		received bool
		bytes    int64
	}
	requests := map[network.RequestID]response{}
	sendFailure := func(f *Failure) {
		select {
		case failures <- f:
		default:
		}
	}
	checkLocation := func(location string) {
		if location == "" || location == "about:blank" {
			return
		}
		u, err := url.Parse(location)
		if err != nil {
			return
		}
		state := sessionLocationState(location, m.offerBaseURL+item)
		if state == "verification_required" || strings.Contains(strings.ToLower(u.Path), "login") {
			code := "SHOPEE_LOGIN_REQUIRED"
			if state == "verification_required" {
				code = "SHOPEE_VERIFICATION_REQUIRED"
			}
			sendFailure(failure(code, "session", false))
		}
	}
	// Both listeners have this job's scope and are removed on its cancellation.
	chromedp.ListenBrowser(scope, func(ev any) {
		if e, ok := ev.(*target.EventTargetDestroyed); ok && e.TargetID == chromedp.FromContext(tab).Target.TargetID {
			sendFailure(failure("BROWSER_UNAVAILABLE", "target", true))
		}
	})
	chromedp.ListenTarget(scope, func(ev any) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case *page.EventFrameNavigated:
			if e.Frame.ParentID == "" {
				frame = e.Frame.ID
				if e.Frame.URL == m.offerBaseURL+item {
					pageOpened = true
				}
				checkLocation(e.Frame.URL)
			}
		case *page.EventNavigatedWithinDocument:
			if e.FrameID == frame {
				checkLocation(e.URL)
			}
		case *network.EventRequestWillBeSent:
			if e.Type == network.ResourceTypeDocument && e.Request.URL == m.offerBaseURL+item {
				loader = e.LoaderID
				frame = e.FrameID
				observed = false
				requestAt = -1
			}
			u, err := url.Parse(e.Request.URL)
			if err == nil && u.Host == m.responseHost && u.Path == "/api/v3/offer/product" && u.Query().Get("item_id") == item && e.DocumentURL == m.offerBaseURL+item {
				requests[e.RequestID] = response{loader: e.LoaderID}
				if loader != "" && loader == e.LoaderID {
					observed = true
					requestAt = time.Since(started).Milliseconds()
				}
			} else if _, ok := requests[e.RequestID]; ok && e.RedirectResponse != nil {
				checkLocation(e.Request.URL)
				sendFailure(failure("SHOPEE_UPSTREAM_FAILED", "response", false))
			}
		case *network.EventResponseReceived:
			r, current := requests[e.RequestID]
			if current && loader != "" && r.loader == loader {
				checkLocation(e.Response.URL)
				switch {
				case e.Response.Status == 401:
					sendFailure(failure("SHOPEE_LOGIN_REQUIRED", "response", false))
				case e.Response.Status == 429:
					sendFailure(failure("SHOPEE_RATE_LIMITED", "response", false))
				case e.Response.Status < 200 || e.Response.Status >= 300:
					sendFailure(failure("SHOPEE_UPSTREAM_FAILED", "response", false))
				case e.Response.EncodedDataLength > maxProductBody:
					sendFailure(failure("SHOPEE_RESPONSE_INVALID", "body", false))
				default:
					r.received = true
					requests[e.RequestID] = r
					headersAt = time.Since(started).Milliseconds()
				}
			} else if e.Type == network.ResourceTypeDocument && e.FrameID == frame && e.Response.Status >= 400 {
				code := "SHOPEE_UPSTREAM_FAILED"
				if e.Response.Status == 401 {
					code = "SHOPEE_LOGIN_REQUIRED"
				}
				if e.Response.Status == 429 {
					code = "SHOPEE_RATE_LIMITED"
				}
				sendFailure(failure(code, "navigation", false))
			}
		case *network.EventLoadingFailed:
			if r, ok := requests[e.RequestID]; ok && loader != "" && r.loader == loader {
				sendFailure(failure("SHOPEE_UPSTREAM_FAILED", "response", false))
			}
		case *network.EventDataReceived:
			if r, ok := requests[e.RequestID]; ok && loader != "" && r.loader == loader {
				r.bytes += e.DataLength
				requests[e.RequestID] = r
				if r.bytes > maxProductBody {
					sendFailure(failure("SHOPEE_RESPONSE_INVALID", "body", false))
				}
			}
		case *network.EventLoadingFinished:
			if r, ok := requests[e.RequestID]; ok && r.received && loader != "" && r.loader == loader {
				delete(requests, e.RequestID)
				if e.EncodedDataLength > maxProductBody {
					sendFailure(failure("SHOPEE_RESPONSE_INVALID", "body", false))
					return
				}
				readyAt = time.Since(started).Milliseconds()
				select {
				case ids <- capturedResponse{e.RequestID, r.loader}:
				default:
				}
			}
		}
	})
	phase = "navigation"
	navigationStarted := time.Now()
	navigated := make(chan error, 1)
	go func() {
		navigated <- boundedCommand(scope, 5*time.Second, chromedp.ActionFunc(func(c context.Context) error {
			_, l, errorText, _, err := page.Navigate(m.offerBaseURL + item).Do(c)
			if err != nil {
				return err
			}
			if errorText != "" {
				return failure("SHOPEE_UPSTREAM_FAILED", "navigation", false)
			}
			mu.Lock()
			loader = l
			mu.Unlock()
			return nil
		}))
	}()
	var err error
	select {
	case f := <-failures:
		return nil, m.sessionFailure(f)
	case <-chromedp.FromContext(tab).Browser.LostConnection:
		return nil, failure("BROWSER_UNAVAILABLE", "connection", false)
	case <-scope.Done():
		return nil, failure("BROWSER_UNAVAILABLE", "navigation", true)
	case err = <-navigated:
	}
	navigationTime = time.Since(navigationStarted)
	if err != nil {
		var f *Failure
		if errors.As(err, &f) {
			return nil, f
		}
		select {
		case f := <-failures:
			return nil, m.sessionFailure(f)
		default:
		}
		if request.Err() != nil {
			return nil, request.Err()
		}
		return nil, failure("BROWSER_UNAVAILABLE", phase, true)
	}
	phase = "response"
	for {
		select {
		case f := <-failures:
			return nil, m.sessionFailure(f)
		case <-chromedp.FromContext(tab).Browser.LostConnection:
			return nil, failure("BROWSER_UNAVAILABLE", "connection", false)
		case <-scope.Done():
			if request.Err() == context.Canceled {
				return nil, request.Err()
			}
			if tab.Err() != nil {
				return nil, failure("BROWSER_UNAVAILABLE", "target", true)
			}
			mu.Lock()
			sent, opened := observed, pageOpened
			mu.Unlock()
			if sent {
				return nil, failure("SHOPEE_TIMEOUT", "response", false)
			}
			if !opened {
				return nil, failure("BROWSER_UNAVAILABLE", "navigation", true)
			}
			return nil, failure("SHOPEE_RESPONSE_NOT_OBSERVED", "response", false)
		case id := <-ids:
			mu.Lock()
			current := id.loader == loader
			mu.Unlock()
			if !current {
				continue
			}
			select {
			case f := <-failures:
				return nil, m.sessionFailure(f)
			default:
			}
			phase = "body"
			readStarted := time.Now()
			var location string
			err := boundedCommand(scope, 3*time.Second, chromedp.Location(&location))
			if err != nil {
				return nil, failure("BROWSER_UNAVAILABLE", "status", true)
			}
			if sessionLocationState(location, m.offerBaseURL+item) != "authenticated" {
				checkLocation(location)
				select {
				case f := <-failures:
					return nil, m.sessionFailure(f)
				default:
				}
				return nil, failure("SHOPEE_RESPONSE_INVALID", "status", false)
			}
			err = boundedCommand(scope, 3*time.Second, chromedp.ActionFunc(func(c context.Context) error {
				var err error
				body, err = network.GetResponseBody(id.id).Do(c)
				return err
			}))
			bodyTime = time.Since(readStarted)
			if err != nil {
				return nil, failure("BROWSER_UNAVAILABLE", "body", true)
			}
			if !validProductBody(body, item) {
				return nil, failure("SHOPEE_RESPONSE_INVALID", "schema", false)
			}
			if link != nil {
				phase = "offer_link"
				body, err = m.fetchOfferLink(scope, *link)
				if err != nil {
					var f *Failure
					if errors.As(err, &f) {
						return nil, m.sessionFailure(f)
					}
					return nil, err
				}
			}
			keep = true
			phase = "complete"
			return body, nil
		}
	}
}

func (m *Manager) sessionFailure(f *Failure) *Failure {
	if f.Code == "SHOPEE_LOGIN_REQUIRED" || f.Code == "SHOPEE_VERIFICATION_REQUIRED" {
		m.mu.Lock()
		m.authenticated = false
		m.state = "login_required"
		if f.Code == "SHOPEE_VERIFICATION_REQUIRED" {
			m.state = "verification_required"
		}
		m.mu.Unlock()
	}
	return f
}

func validProductBody(body []byte, item string) bool {
	if len(body) > maxProductBody || !json.Valid(body) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var envelope map[string]any
	if decoder.Decode(&envelope) != nil {
		return false
	}
	if code, ok := envelope["code"]; ok && fmt.Sprint(code) != "0" {
		return false
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		return false
	}
	for _, key := range []string{"item_id", "itemid"} {
		if id, exists := data[key]; exists && fmt.Sprint(id) != item {
			return false
		}
	}
	if card, ok := data["batch_item_for_item_card_full"].(map[string]any); ok {
		for _, key := range []string{"item_id", "itemid"} {
			if id, exists := card[key]; exists && fmt.Sprint(id) != item {
				return false
			}
		}
	}
	return true
}
