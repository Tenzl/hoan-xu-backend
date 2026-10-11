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
	ids, e := store.Tracking.Issue(tracking.Claims{Customer: "00000000-0000-4000-8000-000000000001", Publisher: os.Getenv("SHOPEE_LIVE_PUBLISHER"), CreatedAt: time.Now().UTC().Truncate(time.Second), Shop: 83496725, Item: 6939920023, Policy: 1, EffectiveBps: 6300})
	if e != nil {
		t.Fatal(e)
	}
	claims, e := store.Tracking.Decode(ids)
	if e != nil || claims.Version != tracking.CurrentVersion || claims.ExpiresAt().Sub(claims.CreatedAt) != tracking.Lifetime {
		t.Fatal("expected current five-day token", claims, e)
	}
	links, e := m.CreateCustomLinks(ctx, []CustomLinkParam{{Shop: "83496725", Item: "6939920023", SubIDs: ids}})
	if e != nil {
		t.Fatal(e)
	}
	if len(links) != 1 || links[0].URL == "" || links[0].Err != nil {
		t.Fatal("missing short URL", links)
	}
	t.Log("Native Shopee GQL accepted v4 carriers; real CSV round-trip still requires confirmation")
}
