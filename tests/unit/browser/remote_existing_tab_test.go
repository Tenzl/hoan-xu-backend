package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func TestRemoteReusesOpenAffiliateTabAndPreservesIt(t *testing.T) {
	closedStarted := make(chan struct{}, 1)
	var closedCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v3/offer/product" {
			if r.URL.Query().Get("item_id") == "106" && closedCalls.Add(1) == 1 {
				closedStarted <- struct{}{}
				<-r.Context().Done()
				return
			}
			if r.URL.Query().Get("item_id") == "104" || r.URL.Query().Get("item_id") == "105" {
				time.Sleep(200 * time.Millisecond)
			}
			fmt.Fprintf(w, `{"code":0,"data":{"item_id":"%s"}}`, r.URL.Query().Get("item_id"))
		} else {
			fmt.Fprint(w, remoteSPAHTML)
		}
	}))
	defer server.Close()
	endpoint, _ := startExternalChrome(t, t.TempDir())
	alloc, stopAlloc := chromedp.NewRemoteAllocator(context.Background(), endpoint)
	defer stopAlloc()
	driver, stopDriver := chromedp.NewContext(alloc)
	defer stopDriver()
	infos, err := chromedp.Targets(driver)
	if err != nil {
		t.Fatal(err)
	}
	var nativeID target.ID
	for _, info := range infos {
		if info.Type == "page" {
			nativeID = info.TargetID
			break
		}
	}
	if nativeID == "" {
		t.Fatal("native tab missing")
	}
	native, stopNative := chromedp.NewContext(driver, chromedp.WithTargetID(nativeID))
	defer stopNative()
	if err := chromedp.Run(native, chromedp.Navigate(server.URL+"/dashboard")); err != nil {
		t.Fatal(err)
	}
	m, err := NewRemote(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.probeURL, m.offerBaseURL = server.URL+"/dashboard", server.URL+"/offer/"
	u, _ := url.Parse(server.URL)
	m.responseHost = u.Host
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	m.probe(ctx)
	if m.Status()["state"] != "authenticated" {
		t.Fatal(m.Status())
	}
	assertOnlyNative := func() {
		t.Helper()
		infos, err := chromedp.Targets(driver)
		if err != nil {
			t.Fatal(err)
		}
		var pages []target.ID
		for _, info := range infos {
			if info.Type == "page" {
				pages = append(pages, info.TargetID)
			}
		}
		if len(pages) != 1 || pages[0] != nativeID {
			t.Fatalf("check opened new tabs instead of using the existing tab: %v, native %s", pages, nativeID)
		}
	}
	assertOnlyNative()
	for _, item := range []string{"101", "102", "102"} {
		body, err := m.capture(ctx, ctx, item)
		if err != nil || !strings.Contains(string(body), `"item_id":"`+item+`"`) {
			t.Fatal(string(body), err)
		}
		assertOnlyNative()
	}
	_, err = m.capture(ctx, ctx, "verification")
	if err == nil || err.Error() != "SHOPEE_VERIFICATION_REQUIRED" {
		t.Fatal("missing verification redirect", err)
	}
	assertOnlyNative()
	m.RefreshSession(ctx)
	assertOnlyNative()
	// A lost controller connection must reattach to the same native tab.
	m.cancel()
	m.probe(ctx)
	assertOnlyNative()
	var work sync.WaitGroup
	results := make(chan error, 2)
	for _, item := range []string{"104", "105"} {
		work.Add(1)
		go func(item string) {
			defer work.Done()
			body, err := m.capture(ctx, ctx, item)
			if err == nil && !strings.Contains(string(body), `"item_id":"`+item+`"`) {
				err = fmt.Errorf("wrong concurrent result: %s", body)
			}
			results <- err
		}(item)
	}
	work.Wait()
	for i := 0; i < 2; i++ {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	infos, err = chromedp.Targets(driver)
	if err != nil {
		t.Fatal(err)
	}
	pages := 0
	for _, info := range infos {
		if info.Type == "page" {
			pages++
		}
	}
	if pages != 2 {
		t.Fatalf("expected one native tab and one extra concurrent worker, got %d", pages)
	}
	m.Close()
	assertOnlyNative()
	// If an administrator closes the borrowed tab during a check, retry on a
	// replacement without disconnecting the browser or keeping a dead lease.
	m.probe(ctx)
	// Keep another native page open: closing Chrome's last page exits the
	// external process, which is an outage rather than a single-target failure.
	_, err = target.CreateTarget("about:blank").Do(cdp.WithExecutor(ctx, chromedp.FromContext(driver).Browser))
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() { _, err := m.capture(ctx, ctx, "106"); result <- err }()
	select {
	case <-closedStarted:
	case <-ctx.Done():
		t.Fatal("check did not start")
	}
	err = target.CloseTarget(nativeID).Do(cdp.WithExecutor(ctx, chromedp.FromContext(driver).Browser))
	if err != nil {
		t.Fatal(err)
	}
	var failure *Failure
	if err := <-result; !errors.As(err, &failure) || !failure.Retryable {
		t.Fatal("closed native tab was not retryable", err)
	}
	body, err := m.captureAttempt(ctx, ctx, "106", 2)
	if err != nil || !strings.Contains(string(body), `"item_id":"106"`) || closedCalls.Load() != 2 {
		t.Fatal("closed native tab did not recover", string(body), err)
	}
}

func TestRemoteSelectsOnlyAvailableAffiliateTabs(t *testing.T) {
	m, err := NewRemote("http://127.0.0.1:9222")
	if err != nil {
		t.Fatal(err)
	}
	infos := []*target.Info{
		{TargetID: "unrelated", Type: "page", URL: "https://example.com/offer/product_offer/101"},
		{TargetID: "private", Type: "page", URL: m.offerBaseURL + "101", BrowserContextID: "incognito"},
		{TargetID: "worker", Type: "service_worker", URL: m.probeURL},
		{TargetID: "login", Type: "page", URL: "https://affiliate.shopee.vn/login", BrowserContextID: "default-profile"},
		{TargetID: "dashboard", Type: "page", URL: m.probeURL, BrowserContextID: "default-profile"},
		{TargetID: "product", Type: "page", URL: m.offerBaseURL + "102", BrowserContextID: "default-profile"},
	}
	for _, expected := range []target.ID{"product", "dashboard", "login", ""} {
		if selected := m.availableRemoteTarget(infos, []cdp.BrowserContextID{"incognito"}); selected != expected {
			t.Fatalf("selected %s, want %s", selected, expected)
		}
		m.workerTargets[expected] = true
	}
}
