package affiliate

import (
	"context"
	"encoding/json"
	"hoanxu/internal/platform"
	"hoanxu/internal/shopeeconfig"
	"hoanxu/internal/tracking"
	"strconv"
	"time"
)

type VerificationBrowser interface {
	RefreshSession(context.Context) map[string]any
	Check(context.Context, string) ([]byte, error)
	CreateOfferLink(context.Context, string, string, [5]string) (string, error)
}

// VerifyIntegration exercises the native browser and real GQL without creating
// customer links, orders, or ledger entries. The diagnostic customer is reserved.
func VerifyIntegration(ctx context.Context, s *platform.Store, b VerificationBrowser, cfg shopeeconfig.Config, raw string, stage func(string)) error {
	stage("session")
	if b == nil {
		return platform.Fail(503, "BROWSER_UNAVAILABLE", "Chrome Shopee chưa sẵn sàng.")
	}
	if session := b.RefreshSession(ctx); session["authenticated"] != true {
		return platform.Fail(503, "SHOPEE_LOGIN_REQUIRED", "Phiên Shopee Affiliate chưa đăng nhập hoặc đã hết hạn. Mở Chrome trên server trong trang quản trị để đăng nhập lại.")
	}
	resolve, stop := context.WithTimeout(ctx, 8*time.Second)
	shop, item, _, err := Resolve(resolve, raw)
	stop()
	if err != nil {
		return platform.Fail(422, "INVALID_URL", err.Error())
	}
	stage("product")
	body, err := b.Check(ctx, item)
	if err != nil {
		return checkError(err)
	}
	var response struct {
		Data map[string]any
		Code int
	}
	if json.Unmarshal(body, &response) != nil || response.Code != 0 || response.Data == nil {
		return platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Dữ liệu sản phẩm Shopee không hợp lệ. Vui lòng thử lại sau.")
	}
	product, err := Normalize(response.Data, cfg.PriceScale)
	if err != nil {
		return platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Dữ liệu sản phẩm Shopee không hợp lệ. Vui lòng thử lại sau.")
	}
	if product.Price <= 0 || product.Commission <= 0 {
		return platform.Fail(422, "PRODUCT_NOT_ELIGIBLE", "Chọn sản phẩm có giá và hoa hồng hợp lệ để kiểm tra.")
	}
	shopID, _ := strconv.ParseUint(shop, 10, 64)
	itemID, _ := strconv.ParseUint(item, 10, 64)
	ids, err := tracking.Issue(tracking.Claims{CreatedAt: time.Now().UTC().Truncate(time.Second), Shop: shopID, Item: itemID, Policy: 1, Tier: "bronze", Bps: 6600}, "hxverify", cfg.Publisher, "0.63", s.SignTracking)
	if err != nil {
		return err
	}
	stage("tracking")
	short, err := b.CreateOfferLink(ctx, shop, item, ids)
	if err != nil {
		return checkError(err)
	}
	if short == "" {
		return platform.Fail(502, "SHOPEE_SUBID_REJECTED", "Shopee không chấp nhận định dạng tracking của link. Chưa tạo link hoàn Xu.")
	}
	return nil
}
