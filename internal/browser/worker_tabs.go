package browser

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

// Each entry is an exclusive lease. A nil entry reserves capacity for a tab
// that has not been created yet, so checks/probes never exceed two worker tabs.
type workerTab struct {
	ctx, root context.Context
	cancel    context.CancelFunc
	ready     bool
	item      string
	borrowed  bool
	closed    atomic.Bool
	rawCancel context.CancelFunc
}

func (w *workerTab) stop() {
	if w.closed.Load() {
		w.rawCancel()
	} else {
		w.cancel()
	}
}

func (m *Manager) acquireWorker(root, request context.Context) (*workerTab, error) {
	var w *workerTab
	select {
	case w = <-m.workerTabs:
	case <-request.Done():
		return nil, request.Err()
	case <-root.Done():
		return nil, errors.New("BROWSER_UNAVAILABLE")
	}
	// Prefer the already hydrated tab when the other slot is still empty.
	if w == nil {
		select {
		case other := <-m.workerTabs:
			m.workerTabs <- nil
			w = other
		default:
		}
	}
	if w != nil && (w.root != root || w.ctx.Err() != nil || w.closed.Load()) {
		w.stop()
		m.forgetTarget(w.ctx)
		m.unclaimWorker(w)
		w = nil
	}
	if w == nil {
		var err error
		w, err = m.newWorker(root, request)
		if err != nil {
			m.workerTabs <- nil
			return nil, err
		}
	}
	return w, nil
}

func (m *Manager) newWorker(root, request context.Context) (*workerTab, error) {
	parent := root
	var options []chromedp.ContextOption
	borrowed := false
	if m.remoteURL != "" {
		// Selection and attachment are atomic across both worker leases.
		m.workerTargetMu.Lock()
		defer m.workerTargetMu.Unlock()
		inspect, stop := context.WithTimeout(root, 3*time.Second)
		defer stop()
		stopRequest := context.AfterFunc(request, stop)
		defer stopRequest()
		infos, err := chromedp.Targets(inspect)
		if err != nil {
			return nil, err
		}
		privateContexts, err := target.GetBrowserContexts().Do(cdp.WithExecutor(inspect, chromedp.FromContext(root).Browser))
		if err != nil {
			return nil, err
		}
		id := m.availableRemoteTarget(infos, privateContexts)
		if id != "" {
			options = append(options, chromedp.WithTargetID(id))
			// A root cancellation must disconnect CDP before the borrowed target's
			// context is canceled, because chromedp cancellation closes its tab.
			parent = context.WithoutCancel(root)
			borrowed = true
			m.mu.Lock()
			m.borrowedTargets[id] = true
			m.mu.Unlock()
		}
	}
	ctx, cancel := chromedp.NewContext(parent, options...)
	rawCancel := cancel
	if borrowed {
		cancel = func() {
			// The controller has no page. Canceling it only disconnects CDP;
			// after that chromedp cannot close a native login/verification tab.
			_ = chromedp.Cancel(root)
			select {
			case <-chromedp.FromContext(root).Browser.LostConnection:
			case <-time.After(3 * time.Second):
			}
			rawCancel()
		}
	}
	w := &workerTab{ctx: ctx, root: root, cancel: cancel, rawCancel: rawCancel, borrowed: borrowed}
	// The first Run starts the target's event loop. Bind it to the worker
	// lifetime, not a probe/job deadline that expires before the next job.
	timer := time.AfterFunc(3*time.Second, cancel)
	stopRequest := context.AfterFunc(request, cancel)
	err := chromedp.Run(ctx, m.trackTarget(), network.Enable())
	stopRequest()
	timer.Stop()
	if err != nil {
		cancel()
		m.forgetTarget(ctx)
		return nil, err
	}
	if m.remoteURL != "" {
		id := chromedp.FromContext(ctx).Target.TargetID
		m.workerTargets[id] = true
		chromedp.ListenBrowser(ctx, func(event any) {
			if e, ok := event.(*target.EventTargetDestroyed); ok && e.TargetID == id {
				w.closed.Store(true)
			}
		})
	}
	return w, nil
}

// Prefer an open product page, then the dashboard, then a login/verification
// page from the same Affiliate origin. Never borrow unrelated or incognito tabs.
// Caller holds workerTargetMu.
func (m *Manager) availableRemoteTarget(infos []*target.Info, privateContexts []cdp.BrowserContextID) target.ID {
	goal, _ := url.Parse(m.probeURL)
	offer, _ := url.Parse(m.offerBaseURL)
	if goal == nil || offer == nil || goal.Host == "" {
		return ""
	}
	best := 0
	var selected target.ID
	for _, info := range infos {
		private := false
		for _, id := range privateContexts {
			if id == info.BrowserContextID {
				private = true
				break
			}
		}
		if info.Type != "page" || private || m.workerTargets[info.TargetID] {
			continue
		}
		u, err := url.Parse(info.URL)
		if err != nil || u.Host != goal.Host || u.Scheme != goal.Scheme {
			continue
		}
		score := 1
		if u.Path == goal.Path {
			score = 2
		}
		if strings.HasPrefix(u.Path, offer.Path) {
			score = 3
		}
		if score > best {
			best, selected = score, info.TargetID
		}
	}
	return selected
}

func (m *Manager) unclaimWorker(w *workerTab) {
	if m.remoteURL == "" {
		return
	}
	c := chromedp.FromContext(w.ctx)
	if c == nil || c.Target == nil {
		return
	}
	m.workerTargetMu.Lock()
	delete(m.workerTargets, c.Target.TargetID)
	m.workerTargetMu.Unlock()
	m.mu.Lock()
	delete(m.borrowedTargets, c.Target.TargetID)
	m.mu.Unlock()
}

func (m *Manager) releaseWorker(w *workerTab, keep bool) {
	if w.borrowed && w.ctx.Err() == nil && !w.closed.Load() {
		// Preserve the tab, including a captcha/login page, after a failed check.
		// Stop the old document's requests before the next lease navigates it.
		if !keep {
			_ = boundedCommand(w.ctx, time.Second, page.StopLoading())
		}
		keep = true
	}
	if !keep || w.ctx.Err() != nil || w.closed.Load() {
		w.stop()
		m.forgetTarget(w.ctx)
		m.unclaimWorker(w)
		w = nil
	}
	m.workerTabs <- w
}

// Caller holds sessionMu exclusively, so all leases are idle and no third
// probe/preload tab is needed. Preloading does not affect the native login tab.
func (m *Manager) prewarmWorkers(request context.Context) {
	if m.workerTabs == nil || m.remoteURL != "" {
		return
	}
	for i := 0; i < 2; i++ {
		w := <-m.workerTabs
		if w != nil && w.root == m.root && w.ctx.Err() == nil && w.ready {
			m.workerTabs <- w
			continue
		}
		if w != nil {
			w.cancel()
			m.forgetTarget(w.ctx)
		}
		var err error
		w, err = m.newWorker(m.root, request)
		if err != nil {
			m.workerTabs <- nil
			continue
		}
		// Allocate the tab only. Each job navigates the product document normally;
		// readiness never depends on Shopee's internal router or anchor count.
		w.ready = true
		m.releaseWorker(w, w.ready)
	}
}

func (m *Manager) closeWorkerTabs() {
	if m.workerTabs == nil {
		return
	}
	var borrowed []*workerTab
	for i := 0; i < 2; i++ {
		w := <-m.workerTabs
		if w != nil {
			if w.borrowed {
				borrowed = append(borrowed, w)
			} else {
				w.stop()
				m.forgetTarget(w.ctx)
				m.unclaimWorker(w)
			}
		}
		m.workerTabs <- nil
	}
	// Close owned pages while connected, then disconnect before releasing native
	// pages. Borrowed contexts never inherit cancellation from the controller.
	for _, w := range borrowed {
		w.stop()
		m.unclaimWorker(w)
	}
}
