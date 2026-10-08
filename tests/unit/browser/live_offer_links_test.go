package browser

import (
	"context"
	"encoding/base64"
	"hoanxu/internal/platform"
	"hoanxu/internal/tracking"
	"log/slog"
	"os"
	"testing"
	"time"
)

// Explicit acceptance test against an already authenticated local Chrome.
// It generates a disposable short link, without touching the application DB.
func TestLiveShopeeSignedSubIDs(t *testing.T) {
	endpoint := os.Getenv("SHOPEE_LIVE_CDP_URL")
	profile, path := os.Getenv("BROWSER_LIVE_PROFILE"), os.Getenv("BROWSER_LIVE_PATH")
	if endpoint == "" && (profile == "" || path == "") {
		t.Skip("opt-in live acceptance: set SHOPEE_LIVE_CDP_URL to authenticated Chrome loopback endpoint")
	}
	logger := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(logger)
	var m *Manager
	var e error
	if endpoint != "" {
		m, e = NewRemote(endpoint)
	} else {
		m = NewManual(path, profile)
		e = m.OpenInteractive()
	}
	if e != nil {
		t.Fatal(e)
	}
	defer m.Close()
	life, cancel := context.WithCancel(context.Background())
	defer cancel()
	ctx, stop := context.WithTimeout(life, 55*time.Second)
	defer stop()
	if m.RefreshSession(ctx)["authenticated"] != true {
		t.Fatal("Shopee Chrome session is not authenticated")
	}
	done := make(chan struct{})
	go func() { defer close(done); m.Run(life) }()
	defer func() { cancel(); <-done }()
	store, e := platform.New(nil, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if e != nil {
		t.Fatal(e)
	}
	ids, e := tracking.Issue(tracking.Claims{CreatedAt: time.Now().UTC().Truncate(time.Second), Shop: 83496725, Item: 6939920023, Policy: 1, Tier: "bronze", Bps: 6600}, platform.Hash("native-gql-acceptance")[:20], "acceptance-test", "0.63", store.SignTracking)
	if e != nil {
		t.Fatal(e)
	}
	claims, e := tracking.Verify(ids, "acceptance-test", store.SignTracking)
	if e != nil || claims.Version != tracking.CurrentVersion || claims.ExpiresAt().Sub(claims.CreatedAt) != tracking.Lifetime {
		t.Fatal("expected current five-day token", claims, e)
	}
	short, e := m.CreateOfferLink(ctx, "83496725", "6939920023", ids)
	if e != nil {
		t.Fatal(e)
	}
	if short == "" {
		t.Fatal("missing short URL")
	}
	t.Log("Native Shopee GQL accepted all five exact SubIDs, including 49-character token, factor and 32-character signature")
}
