package browser

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestCaptureDoesNotWaitForUnrelatedPageResources(t *testing.T) {
	imageStarted := make(chan struct{})
	var imageOnce sync.Once
	releaseImage := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dashboard":
			fmt.Fprint(w, "<body>fixture session</body>")
		case "/offer/101":
			fmt.Fprint(w, `<body><script>fetch('/api/v3/offer/product?item_id=101')</script><img src="/slow-image"></body>`)
		case "/slow-image":
			imageOnce.Do(func() { close(imageStarted) })
			select {
			case <-releaseImage:
			case <-r.Context().Done():
			}
		case "/api/v3/offer/product":
			select {
			case <-imageStarted:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"code":0,"data":{"item_id":"101"}}`)
		case "/offer/login":
			http.Redirect(w, r, "/login", http.StatusFound)
		case "/offer/verification":
			http.Redirect(w, r, "/verify/captcha", http.StatusFound)
		case "/login", "/verify/captcha":
			fmt.Fprint(w, `<body><img src="/slow-image"></body>`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer func() { close(releaseImage); server.Close() }()
	endpoint, _ := startExternalChrome(t, t.TempDir())
	m, err := NewRemote(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	m.probeURL = server.URL + "/dashboard"
	m.offerBaseURL = server.URL + "/offer/"
	u, _ := url.Parse(server.URL)
	m.responseHost = u.Host
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer m.Close()
	m.probe(ctx)
	if m.Status()["state"] != "authenticated" {
		t.Fatal("fixture session unavailable", m.Status())
	}
	request, stop := context.WithTimeout(ctx, 3*time.Second)
	defer stop()
	started := time.Now()
	body, err := m.capture(ctx, request, "101")
	if err != nil {
		t.Fatal("commission response was delayed by an unrelated image", err)
	}
	select {
	case <-imageStarted:
	default:
		t.Fatal("fixture did not start the blocking image")
	}
	if !strings.Contains(string(body), `"item_id":"101"`) {
		t.Fatalf("wrong commission response: %s", body)
	}
	t.Logf("captured commission in %s while page load remained blocked", time.Since(started))
	for _, fixture := range []struct{ item, code, state string }{
		{"login", "SHOPEE_LOGIN_REQUIRED", "login_required"},
		{"verification", "SHOPEE_VERIFICATION_REQUIRED", "verification_required"},
	} {
		t.Run(fixture.item, func(t *testing.T) {
			m.RefreshSession(ctx)
			request, cancel := context.WithTimeout(ctx, 3*time.Second)
			defer cancel()
			_, err := m.capture(ctx, request, fixture.item)
			if err == nil || err.Error() != fixture.code {
				t.Fatal("redirect was not detected while page load was blocked", err)
			}
			if status := m.Status(); status["state"] != fixture.state || status["authenticated"] != false {
				t.Fatal("redirect did not invalidate session", status)
			}
		})
	}
}
