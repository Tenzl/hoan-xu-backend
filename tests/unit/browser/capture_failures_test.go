package browser

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

func TestCaptureClassifiesFailuresWithoutExpiringUnrelatedSession(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auxiliary":
			w.WriteHeader(403)
		case "/api/v3/offer/product":
			item := r.URL.Query().Get("item_id")
			switch item {
			case "pending":
				<-r.Context().Done()
			case "unauthorized":
				w.WriteHeader(401)
			case "forbidden":
				w.WriteHeader(403)
			case "limited":
				w.WriteHeader(429)
			case "server":
				w.WriteHeader(500)
			case "network":
				c, _, _ := w.(http.Hijacker).Hijack()
				c.Close()
			case "invalid":
				fmt.Fprint(w, "not-json")
			case "schema":
				fmt.Fprint(w, `{"code":0,"data":null}`)
			case "wrong":
				fmt.Fprint(w, `{"code":0,"data":{"item_id":"other"}}`)
			case "large":
				fmt.Fprint(w, strings.Repeat("x", maxProductBody+1))
			default:
				fmt.Fprintf(w, `{"code":0,"data":{"item_id":"%s"}}`, item)
			}
		case "/offer/absent":
			fmt.Fprint(w, `<body>Page opened without a product request</body>`)
		case "/offer/aux":
			fmt.Fprint(w, `<body><script>fetch('/auxiliary');fetch('/api/v3/offer/product?item_id=aux')</script></body>`)
		default:
			fmt.Fprint(w, remoteSPAHTML)
		}
	}))
	defer server.Close()
	m := workerTestManager(t, "local")
	u, _ := url.Parse(server.URL)
	m.responseHost, m.probeURL, m.offerBaseURL = u.Host, server.URL+"/dashboard", server.URL+"/offer/"
	ctx := context.Background()
	defer m.Close()
	m.probe(ctx)
	for _, tc := range []struct{ item, code string }{
		{"absent", "SHOPEE_RESPONSE_NOT_OBSERVED"}, {"pending", "SHOPEE_TIMEOUT"},
		{"forbidden", "SHOPEE_UPSTREAM_FAILED"}, {"limited", "SHOPEE_RATE_LIMITED"},
		{"server", "SHOPEE_UPSTREAM_FAILED"}, {"network", "SHOPEE_UPSTREAM_FAILED"},
		{"invalid", "SHOPEE_RESPONSE_INVALID"}, {"schema", "SHOPEE_RESPONSE_INVALID"},
		{"wrong", "SHOPEE_RESPONSE_INVALID"}, {"large", "SHOPEE_RESPONSE_INVALID"},
	} {
		t.Run(tc.item, func(t *testing.T) {
			request, stop := context.WithTimeout(ctx, 1200*time.Millisecond)
			defer stop()
			_, err := m.capture(ctx, request, tc.item)
			var f *Failure
			if !errors.As(err, &f) || f.Code != tc.code || f.Retryable {
				t.Fatalf("wrong failure: %+v", err)
			}
			if m.Status()["authenticated"] != true {
				t.Fatal("product failure expired whole session")
			}
			last := m.Status()["lastFailure"].(*LastFailure)
			if last.Code != tc.code || last.Phase == "" || last.At.IsZero() {
				t.Fatal("missing safe diagnostics", last)
			}
			if len(m.workerTabs) != 2 {
				t.Fatal("worker lease leaked")
			}
		})
	}
	request, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	if _, err := m.capture(ctx, request, "aux"); err != nil || m.Status()["authenticated"] != true {
		t.Fatal("unrelated 403 expired session", err)
	}
	if _, err := m.capture(ctx, request, "unauthorized"); err == nil || err.Error() != "SHOPEE_LOGIN_REQUIRED" {
		t.Fatal("product 401 not recognized", err)
	}
}

func TestClosedTargetIsReplacedAndRetriedOnce(t *testing.T) {
	started := make(chan struct{}, 1)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/offer/product" {
			fmt.Fprint(w, remoteSPAHTML)
			return
		}
		if calls.Add(1) == 1 {
			started <- struct{}{}
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, `{"code":0,"data":{"item_id":"101"}}`)
	}))
	defer server.Close()
	m := workerTestManager(t, "local")
	u, _ := url.Parse(server.URL)
	m.responseHost, m.probeURL, m.offerBaseURL = u.Host, server.URL+"/dashboard", server.URL+"/offer/"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.probe(ctx)
	login := chromedp.FromContext(m.root).Target.TargetID
	other := <-m.workerTabs
	w := <-m.workerTabs
	old := chromedp.FromContext(w.ctx).Target.TargetID
	m.workerTabs <- w
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { m.workerTabs <- other; cancel(); <-done }()
	result := make(chan error, 1)
	go func() {
		request, stop := context.WithTimeout(ctx, 10*time.Second)
		defer stop()
		_, err := m.Check(request, "101")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first request did not start")
	}
	closeCtx, stop := context.WithTimeout(m.root, 3*time.Second)
	err := target.CloseTarget(old).Do(cdp.WithExecutor(closeCtx, chromedp.FromContext(m.root).Browser))
	stop()
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal("closed worker did not recover", err)
	}
	w = <-m.workerTabs
	newID := chromedp.FromContext(w.ctx).Target.TargetID
	m.workerTabs <- w
	if calls.Load() != 2 || old == newID || newID == login || m.Status()["authenticated"] != true || m.Status()["starts"] != 1 {
		t.Fatal("incorrect target recovery", calls.Load(), m.Status())
	}
}

func TestProductBodyRequiresValidSchemaAndMatchingIdentity(t *testing.T) {
	for _, tc := range []struct {
		body  string
		valid bool
	}{
		{`{"code":0,"data":{"item_id":"101"}}`, true},
		{`{"code":0,"data":{"batch_item_for_item_card_full":{"itemid":101}}}`, true},
		{`{"code":0,"data":{"item_id":"102"}}`, false},
		{`{"code":0,"data":{"batch_item_for_item_card_full":{"itemid":102}}}`, false},
		{`{"code":1,"data":{}}`, false}, {`{"code":0,"data":[]}`, false},
	} {
		if validProductBody([]byte(tc.body), "101") != tc.valid {
			t.Fatal(tc.body)
		}
	}
}
