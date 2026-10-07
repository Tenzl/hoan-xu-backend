package affiliate

import (
	"context"
	"errors"
	"hoanxu/internal/platform"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestProductIDsFromWrappedAffiliateURL(t *testing.T) {
	product := "https://shopee.vn/product/590427230/14916526784?utm_source=other&sub_id=other"
	wrapper := "https://s.shopee.vn/an_redir?affiliate_id=other&origin_link=" + url.QueryEscape(product)
	for _, raw := range []string{wrapper, "https://affiliate.shopee.vn/an_redir?origin_link=" + url.QueryEscape(wrapper)} {
		shop, item, err := ProductIDs(raw)
		if err != nil || shop != "590427230" || item != "14916526784" {
			t.Fatal(shop, item, err)
		}
	}
	for _, target := range []string{"https://evil.invalid/product/1/2", "http://shopee.vn/product/1/2", "https://user@shopee.vn/product/1/2", "https://shopee.vn:443/product/1/2"} {
		if _, _, err := ProductIDs("https://s.shopee.vn/an_redir?origin_link=" + url.QueryEscape(target)); err == nil {
			t.Fatal("unsafe wrapped target accepted", target)
		}
	}
}

type resolverTransport func(*http.Request) (*http.Response, error)

func (f resolverTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestResolveAffiliateRedirectStopsBeforeOpeningProduct(t *testing.T) {
	for _, target := range []string{
		"https://shopee.vn/product/590427230/14916526784?utm_source=another&sub_id=another",
		"https://s.shopee.vn/an_redir?affiliate_id=another&origin_link=" + url.QueryEscape("https://shopee.vn/Mon-hang-i.590427230.14916526784?utm_source=another"),
		"https://shopee.vn/opaanlp/590427230/14916526784?credential_token=discard&utm_source=another",
	} {
		calls := 0
		client := &http.Client{Transport: resolverTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls > 1 {
				t.Fatal("opened product or old affiliate wrapper")
			}
			return &http.Response{StatusCode: 301, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		shop, item, canonical, err := resolveWithClient(context.Background(), "https://s.shopee.vn/50ZB7ULNGm", client)
		if err != nil || shop != "590427230" || item != "14916526784" || canonical != "https://shopee.vn/product/590427230/14916526784" || calls != 1 {
			t.Fatal(shop, item, canonical, calls, err)
		}
	}
}

func TestResolveAffiliateRedirectValidatesEveryHopAndBoundsLoops(t *testing.T) {
	for _, target := range []string{"https://evil.invalid/product/1/2", "https://127.0.0.1/product/1/2", "http://shopee.vn/product/1/2", "https://user@shopee.vn/product/1/2", "https://shopee.vn:443/product/1/2", "https://s.shopee.vn/loop"} {
		calls := 0
		client := &http.Client{Transport: resolverTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		if _, _, _, err := resolveWithClient(context.Background(), "https://s.shopee.vn/short", client); err == nil {
			t.Fatal("unsafe target/loop accepted", target)
		}
		if calls > 6 || (target != "https://s.shopee.vn/loop" && calls != 1) {
			t.Fatal("unsafe/too many network requests", target, calls)
		}
	}
}

func TestResolveShopLinkHasActionableMessageWithoutInventingProduct(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: resolverTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: 301, Header: http.Header{"Location": {"https://shopee.vn/jinbox.vn?utm_source=another"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("shop page")), Request: r}, nil
	})}
	shop, item, canonical, err := resolveWithClient(context.Background(), "https://s.shopee.vn/5AqOFtF1C3", client)
	if err == nil || err.Error() != "Link gian hàng không thể ghi nhận hoàn xu" || shop != "" || item != "" || canonical != "" {
		t.Fatal(shop, item, canonical, err)
	}
}

func TestResolveRelativeRedirectAndLimit(t *testing.T) {
	for _, hops := range []int{5, 6} {
		calls := 0
		client := &http.Client{Transport: resolverTransport(func(r *http.Request) (*http.Response, error) {
			calls++
			target := "/next"
			if calls == hops {
				target = "/product/1/2?sub_id=discard"
			}
			return &http.Response{StatusCode: 302, Header: http.Header{"Location": {target}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
		})}
		_, _, canonical, err := resolveWithClient(context.Background(), "https://s.shopee.vn/short", client)
		if (hops == 5 && (err != nil || canonical != "https://shopee.vn/product/1/2")) || (hops == 6 && err == nil) {
			t.Fatal(hops, canonical, err)
		}
	}
}

func TestShopRejectedByBothServicesBeforeBrowserOrDatabase(t *testing.T) {
	s := &Service{}
	for _, raw := range []string{"https://shopee.vn/jinbox.vn?utm_source=another", "https://shopee.vn/shop/264049024", "https://s.shopee.vn/an_redir?origin_link=" + url.QueryEscape("https://shopee.vn/jinbox.vn")} {
		for _, operation := range []func() (any, error){func() (any, error) { return s.Check(context.Background(), raw) }, func() (any, error) { return s.CreateLink(context.Background(), "customer", raw) }} {
			value, err := operation()
			var p *platform.Error
			if value != nil || !errors.As(err, &p) || p.Status != 422 || p.Code != "NOT_PRODUCT_LINK" || p.Message != "Link gian hàng không thể ghi nhận hoàn xu" {
				t.Fatal(value, err)
			}
		}
	}
}

func TestResolveFailureIsNotShopAndDoesNotExposeURL(t *testing.T) {
	for _, status := range []int{403, 429, 503} {
		client := &http.Client{Transport: resolverTransport(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("secret upstream body")), Request: r}, nil
		})}
		_, _, _, err := resolveWithClient(context.Background(), "https://s.shopee.vn/short", client)
		var p *platform.Error
		if !errors.As(resolveError(err), &p) || p.Code != "URL_RESOLVE_FAILED" || strings.Contains(p.Message, "secret") {
			t.Fatal(err, p)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	client := &http.Client{Transport: resolverTransport(func(r *http.Request) (*http.Response, error) { <-r.Context().Done(); return nil, r.Context().Err() })}
	_, _, _, err := resolveWithClient(ctx, "https://s.shopee.vn/short?credential_token=secret", client)
	var p *platform.Error
	if !errors.As(resolveError(err), &p) || p.Status != 504 || p.Code != "SHOPEE_TIMEOUT" || strings.Contains(p.Message, "secret") {
		t.Fatal(err, p)
	}
}

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
	for _, u := range []string{"http://shopee.vn/product/1/2", "https://shopee.vn.evil.com/product/1/2", "https://localhost/", "https://user@shopee.vn/product/1/2", "https://shopee.vn:8443/product/1/2", "https://shopee.vn:/product/1/2"} {
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

type headerOnlyBody struct{ read bool }

func (b *headerOnlyBody) Read([]byte) (int, error) { b.read = true; return 0, io.EOF }
func (*headerOnlyBody) Close() error               { return nil }

func TestResolverDoesNotReadRedirectPagesOrTreatLoginAsShop(t *testing.T) {
	body := &headerOnlyBody{}
	client := &http.Client{Transport: resolverTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": {"https://shopee.vn/product/1/2"}}, Body: body, Request: r}, nil
	})}
	if _, _, _, err := resolveWithClient(context.Background(), "https://s.shopee.vn/short", client); err != nil || body.read {
		t.Fatal("resolver read an unnecessary page body", err)
	}
	for _, path := range []string{"/login", "/login/", "/verify/", "/search/", "/cart/"} {
		u, _ := url.Parse("https://shopee.vn" + path)
		if isShopURL(u) {
			t.Fatal("reserved page treated as a shop", path)
		}
	}
}

func TestProductIDsFromShopeeAffiliateLanding(t *testing.T) {
	shop, item, err := ProductIDs("https://shopee.vn/opaanlp/264049024/27783958254?__mobile__=1&credential_token=discard&affiliate_id=another&utm_source=another")
	if err != nil || shop != "264049024" || item != "27783958254" {
		t.Fatal(shop, item, err)
	}
}

func TestLiveAffiliateInputSamples(t *testing.T) {
	if os.Getenv("SHOPEE_LIVE_RESOLVE_TEST") != "1" {
		t.Skip("opt-in network acceptance")
	}
	ctx := context.Background()
	shop, item, canonical, err := Resolve(ctx, "https://s.shopee.vn/50ZB7ULNGm")
	if err != nil || shop != "264049024" || item != "27783958254" || canonical != "https://shopee.vn/product/264049024/27783958254" {
		t.Fatal("live product link did not resolve to the expected canonical product")
	}
	_, _, _, err = Resolve(ctx, "https://s.shopee.vn/5AqOFtF1C3")
	if !errors.Is(err, errNotProduct) {
		t.Fatal("live shop link did not return the shop warning")
	}
}
