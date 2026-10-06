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
	case "SHOPEE_RESPONSE_NOT_OBSERVED":
		return platform.Fail(502, "SHOPEE_RESPONSE_NOT_OBSERVED", "Trang Shopee đã mở nhưng chưa gửi dữ liệu sản phẩm. Thử lại hoặc kiểm tra phiên trong quản trị.")
	case "SHOPEE_UPSTREAM_FAILED":
		return platform.Fail(502, "SHOPEE_UPSTREAM_FAILED", "Shopee không trả được dữ liệu sản phẩm. Vui lòng thử lại sau.")
	case "SHOPEE_RATE_LIMITED":
		return platform.Fail(429, "SHOPEE_RATE_LIMITED", "Shopee đang giới hạn truy cập. Chờ một lúc trước khi thử lại.")
	case "SHOPEE_TIMEOUT":
		return platform.Fail(504, "SHOPEE_TIMEOUT", "Shopee chưa trả xong dữ liệu sản phẩm trong thời gian cho phép. Vui lòng thử lại.")
	case "SHOPEE_SUBID_REJECTED":
		return platform.Fail(502, "SHOPEE_SUBID_REJECTED", "Shopee không chấp nhận định dạng tracking của link. Chưa tạo link hoàn Xu.")
	case "SHOPEE_RESPONSE_INVALID":
		return platform.Fail(502, "SHOPEE_RESPONSE_INVALID", "Dữ liệu sản phẩm Shopee không hợp lệ. Vui lòng thử lại sau.")
	default:
		return platform.Fail(503, "BROWSER_UNAVAILABLE", "Chromium hoặc kết nối Shopee chưa sẵn sàng. Kiểm tra lại phiên trong trang quản trị.")
	}
}
