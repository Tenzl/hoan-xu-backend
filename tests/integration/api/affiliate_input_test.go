package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"hoanxu/internal/affiliate"
	"hoanxu/internal/auth"
	"hoanxu/internal/tracking"
)

func TestForeignAffiliateInputUsesOwnAttributionAndCanonicalURL(t *testing.T) {
	s, customer, _ := testStore(t)
	ctx := context.Background()
	configureLinkPolicy(t, s)
	if _, err := s.Pool.Exec(ctx, `UPDATE affiliate_channels SET status='available',settings='{"publisher":"123456789"}' WHERE id='shopee'`); err != nil {
		t.Fatal(err)
	}
	f := &offerLinkFixture{}
	aff := &affiliate.Service{Store: s, Enabled: true, TrackingVerified: true, LinkGenerator: f}
	raw := "https://s.shopee.vn/an_redir?affiliate_id=foreign-publisher&sub_id=foreign-customer&origin_link=" + url.QueryEscape("https://shopee.vn/opaanlp/264049024/27783958254?credential_token=discard&utm_source=foreign")
	value, err := aff.CreateLink(ctx, customer, raw)
	if err != nil {
		t.Fatal(err)
	}
	link := value.(map[string]any)
	var original, code string
	if err = s.Pool.QueryRow(ctx, `SELECT original_url FROM affiliate_links WHERE id=$1`, link["id"]).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err = s.Pool.QueryRow(ctx, `SELECT tracking_code FROM users WHERE id=$1`, customer).Scan(&code); err != nil {
		t.Fatal(err)
	}
	if original != "https://shopee.vn/product/264049024/27783958254" || f.shop != "264049024" || f.item != "27783958254" || f.ids[0] != code {
		t.Fatal(original, f.shop, f.item, f.ids[0])
	}
	claims, err := tracking.Verify(f.ids, "123456789", s.SignTracking)
	if err != nil || claims.Shop != 264049024 || claims.Item != 27783958254 {
		t.Fatal(claims, err)
	}
	if _, err = tracking.Verify(f.ids, "foreign-publisher", s.SignTracking); err == nil {
		t.Fatal("foreign publisher accepted")
	}
	for _, id := range f.ids {
		if strings.Contains(id, "foreign") || strings.Contains(id, "discard") {
			t.Fatal("input attribution copied")
		}
	}
}

func TestShopInputRejectedByBothHTTPRoutesWithoutCreatingLink(t *testing.T) {
	s, customer, _ := testStore(t)
	ctx := context.Background()
	a := &auth.Service{Store: s}
	tx, err := s.Pool.Begin(ctx)
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
	handler := New(&Server{Store: s, Auth: a, Affiliate: &affiliate.Service{Store: s}, Origin: "http://localhost:3000", PrivateDir: t.TempDir()})
	for _, endpoint := range []string{"/product-checks", "/affiliate-links"} {
		for _, lang := range []string{"vi", "en"} {
			r := httptest.NewRequest(http.MethodPost, "/api/v1"+endpoint, strings.NewReader(`{"url":"https://shopee.vn/jinbox.vn?utm_source=foreign"}`))
			r.AddCookie(&http.Cookie{Name: "hx_session", Value: token})
			r.Header.Set("Origin", "http://localhost:3000")
			r.Header.Set("X-CSRF-Token", u.CSRF)
			r.Header.Set("Accept-Language", lang)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			var response struct {
				Error struct{ Code, Message string }
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			expected := "Link gian hàng không thể ghi nhận hoàn xu"
			if lang == "en" {
				expected = "Shop links cannot earn cashback Xu"
			}
			if w.Code != 422 || response.Error.Code != "NOT_PRODUCT_LINK" || response.Error.Message != expected {
				t.Fatal(endpoint, lang, w.Code, w.Body.String())
			}
		}
	}
	var count int
	if err = s.Pool.QueryRow(ctx, `SELECT count(*) FROM affiliate_links`).Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}
