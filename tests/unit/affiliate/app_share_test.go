package affiliate

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAppShareShortLink(t *testing.T) {
	for _, target := range []string{"https://shopee.vn/product/123/456", "https://evil.invalid/product/123/456", "https://127.0.0.1/product/123/456", "http://shopee.vn/product/123/456", "https://vn.shp.ee/loop"} {
		calls := 0
		client := &http.Client{Transport: resolverTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		_, _, canonical, err := resolveWithClient(context.Background(), "https://vn.shp.ee/C1Q8E6JS", client)
		if target == "https://shopee.vn/product/123/456" {
			if err != nil || canonical != target || calls != 1 {
				t.Fatal(canonical, calls, err)
			}
		} else if err == nil || calls > 6 {
			t.Fatal("unsafe redirect accepted", target, calls, err)
		}
	}
}
