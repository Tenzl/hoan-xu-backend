package affiliate

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"testing"
)

func TestSafeDialBlocksPrivateAndPinsResolvedAddress(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "192.168.1.1", "100.64.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "2001:db8::1", "198.18.0.1"} {
		if publicIP(net.ParseIP(ip)) {
			t.Fatal("private/reserved address accepted", ip)
		}
	}
	dialed := false
	blocked := safeDial(func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8"), net.ParseIP("127.0.0.1")}, nil
	}, func(context.Context, string, string) (net.Conn, error) {
		dialed = true
		return nil, errors.New("unexpected connection")
	})
	if _, e := blocked(context.Background(), "tcp", "shopee.vn:443"); e == nil || dialed {
		t.Fatal("mixed public/private DNS was connected")
	}
	lookups := 0
	address := ""
	pinned := safeDial(func(context.Context, string, string) ([]net.IP, error) {
		lookups++
		if lookups > 1 {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}, func(_ context.Context, _ string, a string) (net.Conn, error) {
		address = a
		return nil, errors.New("test connection")
	})
	_, _ = pinned(context.Background(), "tcp", "shopee.vn:443")
	if lookups != 1 || address != "8.8.8.8:443" {
		t.Fatal("DNS rebinding protection lost", lookups, address)
	}
}
func TestEveryRedirectIsValidatedAndBounded(t *testing.T) {
	for _, raw := range []string{"https://127.0.0.1/", "http://shopee.vn/", "https://shopee.vn.evil.invalid/", "https://user@shopee.vn/"} {
		u, _ := url.Parse(raw)
		if safeRedirect(&http.Request{URL: u}, nil) == nil {
			t.Fatal("unsafe redirect", raw)
		}
	}
	u, _ := url.Parse("https://shopee.vn/product/1/2")
	if safeRedirect(&http.Request{URL: u}, make([]*http.Request, 6)) == nil {
		t.Fatal("redirect loop accepted")
	}
	if e := safeRedirect(&http.Request{URL: u}, make([]*http.Request, 5)); e != nil {
		t.Fatal(e)
	}
}

func TestProductURL(t *testing.T) {
	for _, u := range []string{"http://shopee.vn/product/1/2", "https://shopee.vn.evil.com/product/1/2", "https://localhost/", "https://user@shopee.vn/product/1/2", "https://shopee.vn:8443/product/1/2"} {
		if _, e := ValidateURL(u); e == nil {
			t.Fatalf("accepted %s", u)
		}
	}
	for _, u := range []string{"https://shopee.vn/product/590427230/14916526784", "https://shopee.vn/test-i.590427230.14916526784"} {
		shop, item, err := ProductIDs(u)
		if err != nil || shop != "590427230" || item != "14916526784" {
			t.Fatal(shop, item, err)
		}
	}
}
