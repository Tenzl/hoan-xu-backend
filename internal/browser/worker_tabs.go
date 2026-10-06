package browser

import (
	"context"
	"errors"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
)

// Each entry is an exclusive lease. A nil entry reserves capacity for a tab
// that has not been created yet, so checks/probes never exceed two worker tabs.
type workerTab struct {
	ctx, root context.Context
	cancel    context.CancelFunc
	ready     bool
	item      string
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
	if w != nil && (w.root != root || w.ctx.Err() != nil) {
		w.cancel()
		m.forgetTarget(w.ctx)
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
	ctx, cancel := chromedp.NewContext(root)
	w := &workerTab{ctx: ctx, root: root, cancel: cancel}
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
	return w, nil
}

func (m *Manager) releaseWorker(w *workerTab, keep bool) {
	if !keep || w.ctx.Err() != nil {
		w.cancel()
		m.forgetTarget(w.ctx)
		w = nil
	}
	m.workerTabs <- w
}

// Caller holds sessionMu exclusively, so all leases are idle and no third
// probe/preload tab is needed. Preloading does not affect the native login tab.
func (m *Manager) prewarmWorkers(request context.Context) {
	if m.workerTabs == nil {
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
	for i := 0; i < 2; i++ {
		w := <-m.workerTabs
		if w != nil {
			w.cancel()
			m.forgetTarget(w.ctx)
		}
		m.workerTabs <- nil
	}
}
