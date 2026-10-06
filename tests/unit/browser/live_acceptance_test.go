package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
)

// Explicit opt-in only: the profile must belong to HoanXu and must not be in
// use by another local controller. This never creates orders, links or money.
func TestLiveShopeeAcceptance(t *testing.T) {
	profile, path := os.Getenv("BROWSER_LIVE_PROFILE"), os.Getenv("BROWSER_LIVE_PATH")
	if profile == "" || path == "" {
		t.Skip("Set BROWSER_LIVE_PROFILE and BROWSER_LIVE_PATH for real Shopee acceptance")
	}
	m := NewManual(path, profile)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.OpenInteractive(); err != nil {
		t.Fatal("live browser could not start")
	}
	m.probe(ctx)
	if m.Status()["authenticated"] != true {
		m.Close()
		t.Fatalf("LIVE_ACCEPTANCE_BLOCKED: state=%s; manual login/verification or upstream recovery is required", m.Status()["state"])
	}
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	var fixture struct {
		Samples []struct {
			ItemID string `json:"itemId"`
		} `json:"samples"`
	}
	raw, err := os.ReadFile("../../tests/fixtures/affiliate/shopee-products-2026-10-05.json")
	if err != nil || json.Unmarshal(raw, &fixture) != nil || len(fixture.Samples) < 5 {
		t.Fatal("live acceptance product list is missing")
	}
	items := fixture.Samples[:5]
	check := func(item string) ([]byte, error) {
		request, stop := context.WithTimeout(ctx, 40*time.Second)
		defer stop()
		return m.Check(request, item)
	}
	verify := func(item string, body []byte) {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.UseNumber()
		var envelope struct {
			Data map[string]any `json:"data"`
		}
		if decoder.Decode(&envelope) != nil {
			t.Fatal("invalid live JSON")
		}
		card, ok := envelope.Data["batch_item_for_item_card_full"].(map[string]any)
		if !ok {
			t.Fatal("live product card schema changed")
		}
		name, ok := card["name"].(string)
		price, err := strconv.ParseInt(fmt.Sprint(card["price"]), 10, 64)
		if !ok || name == "" || err != nil || price%100000 != 0 {
			t.Fatal("live product units changed")
		}
		amount := strconv.FormatInt(price/100000, 10)
		for i := len(amount) - 3; i > 0; i -= 3 {
			amount = amount[:i] + "." + amount[i:]
		}
		commission := fmt.Sprint(envelope.Data["commission"])
		commission = strings.TrimSpace(strings.TrimPrefix(commission, "₫"))
		var matched bool
		for i := 0; i < 2; i++ {
			w := <-m.workerTabs
			if w != nil && w.item == item {
				arguments, _ := json.Marshal([]string{name, amount, commission})
				script := fmt.Sprintf(`(() => {const [name,price,commission]=%s;const clean=s=>s.replace(/\s+/g,' ').trim();const text=clean(document.body.innerText);return text.includes(clean(name))&&text.includes(price)&&text.includes(commission)})()`, arguments)
				err := boundedCommand(w.ctx, 3*time.Second, chromedp.Poll(script, &matched, chromedp.WithPollingTimeout(2*time.Second)))
				m.workerTabs <- w
				if err != nil || !matched {
					t.Fatal("live rendered product does not match response", item)
				}
				break
			}
			m.workerTabs <- w
		}
		if !matched {
			t.Fatal("live worker was not retained", item)
		}
		t.Logf("live product %s: name/price/commission match rendered Affiliate page", item)
	}
	// One pair overlaps; the remaining requests are spaced to stay below 10/min.
	for round := 0; round < 2; round++ {
		for i := 0; i < 5; i++ {
			if round == 0 && i == 0 {
				type capture struct {
					item string
					body []byte
					err  error
				}
				results := make(chan capture, 2)
				for _, product := range items[:2] {
					go func(item string) { body, err := check(item); results <- capture{item, body, err} }(product.ItemID)
				}
				for j := 0; j < 2; j++ {
					r := <-results
					if r.err != nil {
						t.Fatalf("LIVE_ACCEPTANCE_BLOCKED: %s", r.err)
					}
					verify(r.item, r.body)
				}
				i++
			} else {
				body, err := check(items[i].ItemID)
				if err != nil {
					t.Fatalf("LIVE_ACCEPTANCE_BLOCKED: %s", err)
				}
				verify(items[i].ItemID, body)
			}
			select {
			case <-ctx.Done():
				t.Fatal("live acceptance canceled")
			case <-time.After(7 * time.Second):
			}
		}
	}
	if m.Status()["starts"] != 1 {
		t.Fatal("live acceptance restarted browser")
	}
}
