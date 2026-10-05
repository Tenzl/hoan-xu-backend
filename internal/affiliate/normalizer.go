package affiliate

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type Product struct {
	ProductName          string  `json:"productName"`
	Price                int64   `json:"price"`
	Commission           int64   `json:"commission"`
	CommissionRate       float64 `json:"commissionRate"`
	SellerCommissionRate float64 `json:"sellerCommissionRate"`
	ShopeeCommissionRate float64 `json:"shopeeCommissionRate"`
	SellerCommission     int64   `json:"sellerCommission"`
	ShopeeCommission     int64   `json:"shopeeCommission"`
	CommissionCap        *int64  `json:"commissionCap"`
}

func integer(value any) (int64, error) {
	if value == nil {
		return 0, errors.New("missing amount")
	}
	s := strings.TrimSpace(fmt.Sprint(value))
	s = strings.NewReplacer("₫", "", "đ", "", ".", "", ",", "", " ", "").Replace(s)
	n, e := strconv.ParseInt(s, 10, 64)
	if e != nil || n < 0 || n > 1e15 {
		return 0, errors.New("invalid amount")
	}
	return n, nil
}
func percent(value any) (float64, error) {
	if value == nil {
		return 0, errors.New("missing rate")
	}
	s := strings.TrimSpace(strings.TrimSuffix(fmt.Sprint(value), "%"))
	s = strings.ReplaceAll(s, ",", ".")
	n, e := strconv.ParseFloat(s, 64)
	if e != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 {
		return 0, errors.New("invalid percentage")
	}
	return n, nil
}
func Normalize(data map[string]any, scale int64) (Product, error) {
	p := Product{}
	if scale <= 0 {
		return p, errors.New("price scale has not been confirmed")
	}
	product, ok := data["batch_item_for_item_card_full"].(map[string]any)
	if !ok {
		return p, errors.New("missing product card")
	}
	p.ProductName, _ = product["name"].(string)
	if p.ProductName == "" {
		return p, errors.New("missing product name")
	}
	rates, ok := data["commission_rate"].(map[string]any)
	if !ok {
		return p, errors.New("missing rates")
	}
	var e error
	raw, e := integer(product["price"])
	if e != nil {
		return p, e
	}
	if raw%scale != 0 {
		return p, errors.New("unexpected price precision")
	}
	p.Price = raw / scale
	if p.Commission, e = integer(data["commission"]); e != nil {
		return p, e
	}
	if p.CommissionRate, e = percent(rates["max_commission_rate"]); e != nil {
		return p, e
	}
	if p.SellerCommissionRate, e = percent(rates["seller_commission_rate"]); e != nil {
		return p, e
	}
	if p.ShopeeCommissionRate, e = percent(rates["shopee_commission_rate"]); e != nil {
		return p, e
	}
	if p.SellerCommission, e = integer(rates["seller_commission"]); e != nil {
		return p, e
	}
	if p.ShopeeCommission, e = integer(rates["shopee_commission"]); e != nil {
		return p, e
	}
	if v, ok := rates["commission_cap"]; ok && v != nil && fmt.Sprint(v) != "" {
		cap, e := integer(v)
		if e != nil {
			return p, e
		}
		p.CommissionCap = &cap
	}
	return p, nil
}
