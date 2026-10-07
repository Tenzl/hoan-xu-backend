package api

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyIdentityRequiresSignatureBoundToRequest(t *testing.T) {
	key := strings.Repeat("a", 64)
	r := httptest.NewRequest("POST", "http://api/api/v1/product-checks?x=1", nil)
	r.RemoteAddr = "127.0.0.1:4321"
	r.Header.Set("X-HX-Client-IP", "203.0.113.7")
	now := time.Unix(1800000000, 0)
	if clientIP(r, key, now) != "127.0.0.1" {
		t.Fatal("accepted unsigned client IP")
	}
	r.Header.Set("X-HX-Proxy-Time", "1800000000")
	r.Header.Set("X-HX-Proxy-Signature", proxySignature(key, "1800000000", "203.0.113.7", r.Method, r.URL.RequestURI()))
	if clientIP(r, key, now) != "203.0.113.7" {
		t.Fatal("rejected trusted identity")
	}
	r.URL.RawQuery = "x=2"
	if clientIP(r, key, now) != "127.0.0.1" {
		t.Fatal("signature not bound to request path")
	}
	r.URL.RawQuery = "x=1"
	if clientIP(r, key, now.Add(61*time.Second)) != "127.0.0.1" {
		t.Fatal("accepted stale proxy proof")
	}
}
