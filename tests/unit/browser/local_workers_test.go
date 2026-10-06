package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

func TestLocalWorkersShareProfileAndKeepLoginTabSeparate(t *testing.T) {
	var active, maxActive atomic.Int32
	var probeDuringWork atomic.Bool
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/dashboard" && active.Load() > 0 {
			probeDuringWork.Store(true)
		}
		if r.URL.Path != "/api/v3/offer/product" {
			fmt.Fprint(w, remoteSPAHTML)
			return
		}
		item := r.URL.Query().Get("item_id")
		if item == "cancelled" {
			<-r.Context().Done()
			return
		}
		if item == "101" || item == "102" {
			n := active.Add(1)
			defer active.Add(-1)
			for old := maxActive.Load(); n > old; old = maxActive.Load() {
				if maxActive.CompareAndSwap(old, n) {
					break
				}
			}
			started <- struct{}{}
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"code":0,"data":{"item_id":"%s"}}`, item)
	}))
	defer server.Close()
	m := workerTestManager(t, "local")
	m.probeURL, m.offerBaseURL = server.URL+"/dashboard", server.URL+"/offer/"
	u, _ := url.Parse(server.URL)
	m.responseHost = u.Host
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	if m.Status()["browser"] != false || m.remoteURL != "" {
		t.Fatal("local manual mode started or connected before administrator setup")
	}
	// Simulate opening the local administrator tab; tests stay headless.
	if err := m.start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(m.root, chromedp.Navigate(server.URL+"/dashboard"), chromedp.Evaluate(`localStorage.setItem('local-profile', 'shared')`, nil)); err != nil {
		t.Fatal(err)
	}
	if m.RefreshSession(ctx)["authenticated"] != true {
		t.Fatal("local workers were not preloaded after session verification", m.Status())
	}
	loginID := chromedp.FromContext(m.root).Target.TargetID
	check := func(item string) error {
		request, stop := context.WithTimeout(ctx, 5*time.Second)
		defer stop()
		body, err := m.Check(request, item)
		if err == nil && !strings.Contains(string(body), `"item_id":"`+item+`"`) {
			return fmt.Errorf("wrong product response for %s", item)
		}
		return err
	}
	results := make(chan error, 2)
	go func() { results <- check("101") }()
	go func() { results <- check("102") }()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-time.After(5 * time.Second):
			close(release)
			t.Fatal("local checks did not run in two concurrent tabs")
		}
	}
	refreshed := make(chan struct{})
	go func() { m.RefreshSession(ctx); close(refreshed) }()
	close(release)
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	<-refreshed
	if maxActive.Load() != 2 || probeDuringWork.Load() {
		t.Fatal("local worker concurrency or idle-only probe failed", maxActive.Load(), probeDuringWork.Load())
	}
	for i := 0; i < 2; i++ {
		w := <-m.workerTabs
		if w == nil {
			m.workerTabs <- w
			t.Fatal("successful local worker was discarded")
		}
		var saved string
		err := chromedp.Run(w.ctx, chromedp.Evaluate(`localStorage.getItem('local-profile')`, &saved))
		workerID := chromedp.FromContext(w.ctx).Target.TargetID
		m.workerTabs <- w
		if err != nil || saved != "shared" || workerID == loginID {
			t.Fatal("worker lost the shared profile or reused the administrator tab", saved, err)
		}
	}
	short, stop := context.WithTimeout(ctx, 150*time.Millisecond)
	_, err := m.Check(short, "cancelled")
	stop()
	if err == nil {
		t.Fatal("cancelled local check returned success")
	}
	// Wait for the worker's asynchronous cancellation and target cleanup.
	deadline := time.Now().Add(3 * time.Second)
	for {
		targets, err := chromedp.Targets(m.root)
		if err != nil {
			t.Fatal(err)
		}
		pending := false
		for _, info := range targets {
			pending = pending || info.URL == server.URL+"/offer/cancelled"
		}
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("cancelled local worker tab leaked")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err := check("103"); err != nil {
		t.Fatal("local workers did not recover after cancellation", err)
	}
	if m.RefreshSession(ctx)["authenticated"] != true {
		t.Fatal("local workers could not be preloaded again")
	}
	targets, err := chromedp.Targets(m.root)
	if err != nil {
		t.Fatal(err)
	}
	pages := 0
	for _, info := range targets {
		if info.Type == "page" {
			pages++
		}
	}
	if pages != 3 || chromedp.FromContext(m.root).Target.TargetID != loginID {
		t.Fatal("expected two reusable workers and one separate administrator tab", pages)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("local browser workers did not shut down")
	}
	if m.Status()["browser"] != false {
		t.Fatal("local browser remained running after shutdown")
	}
}
