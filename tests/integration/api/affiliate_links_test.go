package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
)

func TestCreatedLinkHistoryWithoutSaving(t *testing.T) {
	store, customer, other := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: store}
	tx, err := store.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	token, err := a.NewSession(ctx, tx, customer)
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	u, err := a.Session(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	for i, owner := range []string{customer, customer, customer, other} {
		_, err = store.Pool.Exec(ctx, `INSERT INTO affiliate_links(user_id,channel,original_url,affiliate_url,tracking_code,policy_id,tier_code,min_share_bps,max_share_bps,created_at) SELECT $1,'shopee','https://shopee.vn/product/1/2','https://s.shopee.vn/test',$2,id,'bronze',2222,2224,'2026-10-05T00:00:00Z' FROM cashback_policies WHERE mode='tiered'`, owner, fmt.Sprintf("history-%d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	srv := New(&Server{Store: store, Auth: a, Affiliate: &affiliate.Service{Store: store}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	request := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/api/v1"+path, strings.NewReader(body))
		r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
		r.Header.Set("Origin", "http://localhost:3000")
		r.Header.Set("X-CSRF-Token", u.CSRF)
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	seen := map[string]bool{}
	path := "/affiliate-links?perPage=2"
	for _, count := range []int{2, 1} {
		w := request(http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatal(w.Code, w.Body.String())
		}
		var response struct {
			Data []map[string]any `json:"data"`
			Meta struct {
				NextCursor string `json:"nextCursor"`
			} `json:"meta"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if len(response.Data) != count {
			t.Fatalf("expected %d links, got %d", count, len(response.Data))
		}
		for _, link := range response.Data {
			id := link["id"].(string)
			if seen[id] || link["trackingCode"] == "history-3" {
				t.Fatal("duplicate or another user's link", link)
			}
			seen[id] = true
			if _, exists := link["saved"]; exists {
				t.Fatal("history still exposes saved state")
			}
			if link["tierCode"] != "bronze" || link["minSharePercent"] != 22.22 || link["maxSharePercent"] != 22.24 {
				t.Fatal("link snapshot changed", link)
			}
		}
		if count == 2 && response.Meta.NextCursor == "" {
			t.Fatal("missing history cursor")
		}
		path = "/affiliate-links?perPage=2&cursor=" + response.Meta.NextCursor
	}
	for id := range seen {
		if w := request(http.MethodPatch, "/affiliate-links/"+id, `{"saved":true}`); w.Code != http.StatusMethodNotAllowed {
			t.Fatal("removed save endpoint is still available", w.Code, w.Body.String())
		}
		break
	}
	var exists bool
	if err = store.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='affiliate_links' AND column_name='saved')`).Scan(&exists); err != nil || exists {
		t.Fatal("saved column remains", exists, err)
	}
}
