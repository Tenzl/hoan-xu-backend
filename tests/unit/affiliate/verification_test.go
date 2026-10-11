package affiliate

import (
	"context"
	"encoding/base64"
	"errors"
	"hoanxu/internal/browser"
	"hoanxu/internal/platform"
	"hoanxu/internal/shopeeconfig"
	"hoanxu/internal/tracking"
	"testing"
)

type diagnosticBrowser struct {
	logged bool
	body   []byte
	reject bool
	called bool
	store  *platform.Store
	t      *testing.T
}

func (b *diagnosticBrowser) RefreshSession(context.Context) map[string]any {
	return map[string]any{"authenticated": b.logged}
}
func (b *diagnosticBrowser) Check(context.Context, string) ([]byte, error) { return b.body, nil }
func (b *diagnosticBrowser) CreateCustomLinks(_ context.Context, params []browser.CustomLinkParam) ([]browser.CustomLinkResult, error) {
	if len(params) != 1 {
		b.t.Fatal("diagnostic must send one item through the batch API")
	}
	shop, item, ids := params[0].Shop, params[0].Item, params[0].SubIDs
	b.called = true
	claims, err := b.store.Tracking.Decode(ids)
	if err != nil || shop != "1" || item != "2" || claims.Customer != "00000000-0000-4000-8000-000000000001" || claims.Publisher != "123456789" || claims.EffectiveBps != 6300 || len(ids[0]) > 29 || len(ids[4]) > 29 || claims.Version != tracking.CurrentVersion {
		b.t.Fatal("diagnostic tracking mismatch", err)
	}

	if b.reject {
		return []browser.CustomLinkResult{{Err: errors.New("SHOPEE_SUBID_REJECTED")}}, nil
	}
	return []browser.CustomLinkResult{{URL: "https://s.shopee.vn/abc123"}}, nil
}
func TestDiagnosticVerificationChecksNativeProductAndSignedGQL(t *testing.T) {
	store, err := platform.New(nil, base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	store.Tracking, err = tracking.NewCodec(`{"1":"`+base64.StdEncoding.EncodeToString(make([]byte, 32))+`"}`, "1")
	if err != nil {
		t.Fatal(err)
	}
	cfg := shopeeconfig.Default()
	cfg.Publisher = "123456789"
	body := []byte(`{"data":{"commission":"₫55.005","commission_rate":{"max_commission_rate":"9,5%","seller_commission_rate":"7%","shopee_commission_rate":"2,5%","seller_commission":"₫40.530","shopee_commission":"₫14.475","commission_cap":"₫40.000"},"batch_item_for_item_card_full":{"name":"Test","price":"57900000000"}}}`)
	for _, tc := range []struct {
		name     string
		logged   bool
		body     []byte
		reject   bool
		wantCode string
		tracking bool
	}{{"valid", true, body, false, "", true}, {"logged out", false, body, false, "SHOPEE_LOGIN_REQUIRED", false}, {"malformed", true, []byte(`{"data":{}}`), false, "SHOPEE_RESPONSE_INVALID", false}, {"rejected", true, body, true, "SHOPEE_SUBID_REJECTED", true}} {
		t.Run(tc.name, func(t *testing.T) {
			b := &diagnosticBrowser{logged: tc.logged, body: tc.body, reject: tc.reject, store: store, t: t}
			err := VerifyIntegration(context.Background(), store, b, cfg, "https://shopee.vn/product/1/2", func(string) {})
			if tc.wantCode == "" && err != nil {
				t.Fatal(err)
			}
			if tc.wantCode != "" {
				var p *platform.Error
				if !errors.As(err, &p) || p.Code != tc.wantCode {
					t.Fatal(err)
				}
			}
			if b.called != tc.tracking {
				t.Fatal("tracking attempted before product/session verified")
			}
		})
	}
}
