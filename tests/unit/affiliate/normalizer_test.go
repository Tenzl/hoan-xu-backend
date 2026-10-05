package affiliate

import (
	"encoding/json"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func TestNormalizationRequiresConfirmedScale(t *testing.T) {
	var data map[string]any
	_ = json.Unmarshal([]byte(`{"commission":"₫55.005","commission_rate":{"max_commission_rate":"9,5%","seller_commission_rate":"7%","shopee_commission_rate":"2,5%","seller_commission":"₫40.530","shopee_commission":"₫14.475","commission_cap":"₫40.000"},"batch_item_for_item_card_full":{"name":"Test","price":"57900000000"}}`), &data)
	if _, e := Normalize(data, 0); e == nil {
		t.Fatal("unconfirmed scale accepted")
	}
	v, e := Normalize(data, 100000)
	if e != nil || v.Price != 579000 || v.Commission != 55005 || v.CommissionRate != 9.5 || v.CommissionCap == nil || *v.CommissionCap != 40000 {
		t.Fatal(v, e)
	}
	delete(data, "commission")
	if _, e = Normalize(data, 100000); e == nil {
		t.Fatal("missing commission treated as zero")
	}
}

func TestNormalizationMatchesCapturedShopeePages(t *testing.T) {
	fixture, err := os.ReadFile("../../tests/fixtures/affiliate/shopee-products-2026-10-05.json")
	if err != nil {
		t.Fatal(err)
	}
	var capture struct {
		PriceScale int64 `json:"priceScale"`
		Samples    []struct {
			ItemID           string         `json:"itemId"`
			Data             map[string]any `json:"data"`
			DisplayedAmounts []string       `json:"displayedAmounts"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(fixture, &capture); err != nil {
		t.Fatal(err)
	}
	if len(capture.Samples) != 10 || capture.PriceScale != 100000 {
		t.Fatal("expected ten independently captured API/page pairs")
	}
	dong := regexp.MustCompile(`₫[0-9.]+`)
	readDisplayedMoney := func(t *testing.T, line string) int64 {
		t.Helper()
		matches := dong.FindAllString(line, -1)
		if len(matches) == 0 {
			t.Fatal("missing displayed money", line)
		}
		value, err := strconv.ParseInt(strings.ReplaceAll(strings.TrimPrefix(matches[len(matches)-1], "₫"), ".", ""), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	for _, sample := range capture.Samples {
		t.Run(sample.ItemID, func(t *testing.T) {
			product, err := Normalize(sample.Data, capture.PriceScale)
			if err != nil {
				t.Fatal(err)
			}
			if len(sample.DisplayedAmounts) < 2 {
				t.Fatal("missing displayed price/commission")
			}
			if product.Price != readDisplayedMoney(t, sample.DisplayedAmounts[0]) || product.Commission != readDisplayedMoney(t, sample.DisplayedAmounts[1]) {
				t.Fatalf("normalized amounts differ from the rendered Shopee page: %+v", product)
			}
			if sample.ItemID == "25876970260" && (product.Price != 95000 || product.Commission != 2375 || product.CommissionRate != 2.5 || product.SellerCommission != 0) {
				t.Fatal("reported product does not match its live capture", product)
			}
			// Shopee rounds components independently. Preserve its total amount.
			if sample.ItemID == "41011259591" && (product.Commission != 6014 || product.SellerCommission+product.ShopeeCommission != 6015) {
				t.Fatal("Shopee's total was recomputed from rounded components", product)
			}
		})
	}
}
