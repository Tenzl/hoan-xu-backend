package affiliate

import (
	"context"
	"errors"

	"hoanxu/internal/platform"
)

// Only stable codes and actionable messages cross the public API boundary.
func checkError(err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return platform.Fail(504, "SHOPEE_TIMEOUT", "Kiểm tra Shopee quá thời gian.")
	}
	switch err.Error() {
	case "SHOPEE_VERIFICATION_REQUIRED":
		return platform.Fail(503, "SHOPEE_VERIFICATION_REQUIRED", "Shopee yêu cầu xác minh. Mở Chrome trên server để hoàn tất, sau đó kiểm tra phiên lại.")
	case "SHOPEE_LOGIN_REQUIRED":
		return platform.Fail(503, "SHOPEE_LOGIN_REQUIRED", "Phiên Shopee Affiliate chưa đăng nhập hoặc đã hết hạn. Mở Chrome trên server trong trang quản trị để đăng nhập lại.")
	case "SHOPEE_COOKIE_STORAGE_ERROR":
		return platform.Fail(503, "SHOPEE_COOKIE_STORAGE_ERROR", "Không đọc được cookie Shopee đã lưu. Cập nhật lại cookie trong trang quản trị.")
	case "QUEUE_FULL":
		return platform.Fail(429, "QUEUE_FULL", "Có quá nhiều lượt kiểm tra Shopee. Vui lòng thử lại sau.")
	case "QUEUE_TIMEOUT":
		return platform.Fail(503, "QUEUE_TIMEOUT", "Hàng đợi kiểm tra Shopee đang bận. Vui lòng thử lại sau.")
	case "SHOPEE_RESPONSE_INVALID":
		return platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Dữ liệu sản phẩm Shopee không hợp lệ. Vui lòng thử lại sau.")
	default:
		return platform.Fail(503, "BROWSER_UNAVAILABLE", "Chromium hoặc kết nối Shopee chưa sẵn sàng. Kiểm tra lại phiên trong trang quản trị.")
	}
}
