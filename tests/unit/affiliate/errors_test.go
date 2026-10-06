package affiliate

import (
	"context"
	"errors"
	"hoanxu/internal/platform"
	"testing"
)

func TestCheckErrorsAreActionableAndDoNotExposeBrowserDetails(t *testing.T) {
	for _, test := range []struct {
		err    error
		status int
		code   string
	}{
		{errors.New("SHOPEE_VERIFICATION_REQUIRED"), 503, "SHOPEE_VERIFICATION_REQUIRED"},
		{errors.New("SHOPEE_LOGIN_REQUIRED"), 503, "SHOPEE_LOGIN_REQUIRED"},
		{context.DeadlineExceeded, 504, "SHOPEE_TIMEOUT"},
		{errors.New("QUEUE_FULL"), 429, "QUEUE_FULL"},
		{errors.New("SHOPEE_RESPONSE_NOT_OBSERVED"), 502, "SHOPEE_RESPONSE_NOT_OBSERVED"},
		{errors.New("SHOPEE_UPSTREAM_FAILED"), 502, "SHOPEE_UPSTREAM_FAILED"},
		{errors.New("SHOPEE_RATE_LIMITED"), 429, "SHOPEE_RATE_LIMITED"},
		{errors.New("SHOPEE_TIMEOUT"), 504, "SHOPEE_TIMEOUT"},
		{errors.New("SHOPEE_RESPONSE_INVALID"), 502, "SHOPEE_RESPONSE_INVALID"},
		{errors.New("local secret details"), 503, "BROWSER_UNAVAILABLE"},
	} {
		var problem *platform.Error
		if !errors.As(checkError(test.err), &problem) || problem.Status != test.status || problem.Code != test.code {
			t.Fatal(problem)
		}
		if problem.Message == "Không lấy được dữ liệu Shopee. Kiểm tra phiên affiliate." || problem.Message == "local secret details" {
			t.Fatal("generic or sensitive error", problem)
		}
	}
}
