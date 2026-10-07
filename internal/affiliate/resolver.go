package affiliate

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

func ValidateURL(raw string) (*url.URL, error) {
	if len(raw) > 2048 {
		return nil, errors.New("URL quá dài")
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Host != u.Hostname() {
		return nil, errors.New("Link phải là HTTPS Shopee, không có userinfo/port")
	}
	switch u.Hostname() {
	case "shopee.vn", "www.shopee.vn", "s.shopee.vn", "affiliate.shopee.vn":
	default:
		return nil, errors.New("Domain không được hỗ trợ")
	}
	return u, nil
}

var direct = regexp.MustCompile(`/product/([0-9]+)/([0-9]+)(?:/|$)`)
var slug = regexp.MustCompile(`-i\.([0-9]+)\.([0-9]+)(?:/|$)`)
var landing = regexp.MustCompile(`^/opaanlp/([0-9]+)/([0-9]+)(?:/|$)`)
var shopPath = regexp.MustCompile(`^/(?:shop/[0-9]+|[A-Za-z0-9_][A-Za-z0-9_.-]{0,99})/?$`)
var errNotProduct = errors.New("Link gian hàng không thể ghi nhận hoàn xu")
var errResolveFailed = errors.New("Chưa mở được link Shopee. Bạn thử lại nhé.")

func isShopURL(u *url.URL) bool {
	if u.Hostname() != "shopee.vn" && u.Hostname() != "www.shopee.vn" {
		return false
	}
	// These single-segment routes are not shop usernames.
	switch strings.Trim(u.Path, "/") {
	case "", "login", "verify", "buyer", "search", "cart", "mall", "flash_sale", "product", "opaanlp", "an_redir", "universal-link", "shop", "user", "blog", "m", "api":
		return false
	}
	return shopPath.MatchString(u.Path)
}

func ProductIDs(raw string) (string, string, error) {
	u, e := unwrapAffiliateURL(raw)
	if e != nil {
		return "", "", e
	}
	for _, r := range []*regexp.Regexp{direct, slug, landing} {
		p := r.FindStringSubmatch(u.Path)
		if len(p) == 3 {
			return p[1], p[2], nil
		}
	}
	return "", "", errors.New("Link này chưa có sản phẩm cụ thể. Hãy mở link và sao chép link món bạn muốn mua.")
}

func unwrapAffiliateURL(raw string) (*url.URL, error) {
	for depth := 0; depth <= 5; depth++ {
		u, e := ValidateURL(raw)
		if e != nil {
			return nil, e
		}
		// Decode only Shopee's affiliate wrapper, validating each nested URL.
		// Attribution parameters never enter the resulting product URL.
		if u.Path != "/an_redir" || u.Query().Get("origin_link") == "" {
			return u, nil
		}
		raw = u.Query().Get("origin_link")
	}
	return nil, errors.New("Link có quá nhiều chuyển hướng. Hãy dán link sản phẩm trực tiếp.")
}
func publicIP(ip net.IP) bool {
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	a = a.Unmap()
	if !a.IsGlobalUnicast() || a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(cidr).Contains(a) {
			return false
		}
	}
	return true
}
func safeDial(lookup func(context.Context, string, string) ([]net.IP, error), dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := lookup(ctx, "ip", host)
		if e != nil {
			return nil, e
		}
		for _, ip := range ips {
			if !publicIP(ip) {
				return nil, errors.New("Địa chỉ mạng không được phép")
			}
		}
		for _, ip := range ips {
			conn, e := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return conn, nil
			}
		}
		return nil, errors.New("Không kết nối được Shopee")
	}
}
func safeRedirect(r *http.Request, via []*http.Request) error {
	if len(via) > 5 {
		return errors.New("Quá nhiều redirect")
	}
	_, e := ValidateURL(r.URL.String())
	return e
}
func Resolve(ctx context.Context, raw string) (string, string, string, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: safeDial(net.DefaultResolver.LookupIP, dialer.DialContext)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport}
	// One deadline covers all hops, rather than allowing eight seconds per hop.
	scope, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	return resolveWithClient(scope, raw, client)
}

func resolveWithClient(ctx context.Context, raw string, client *http.Client) (string, string, string, error) {
	noRedirect := *client
	noRedirect.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	for hop := 0; hop <= 5; hop++ {
		if err := ctx.Err(); err != nil {
			return "", "", "", err
		}
		u, e := unwrapAffiliateURL(raw)
		if e != nil {
			return "", "", "", e
		}
		if shop, item, e := ProductIDs(u.String()); e == nil {
			return shop, item, "https://shopee.vn/product/" + shop + "/" + item, nil
		}
		if isShopURL(u) {
			return "", "", "", errNotProduct
		}
		req, e := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
		if e != nil {
			return "", "", "", e
		}
		resp, e := noRedirect.Do(req)
		if e != nil {
			return "", "", "", e
		}
		// The Location header is sufficient; do not download redirect page bodies.
		resp.Body.Close()
		switch resp.StatusCode {
		case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
			next, err := resp.Location()
			if err != nil {
				return "", "", "", errors.New("Chưa mở được link này. Hãy thử link sản phẩm trực tiếp.")
			}
			raw = next.String()
		default:
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return "", "", "", errResolveFailed
			}
			return "", "", "", errors.New("Link này chưa có sản phẩm cụ thể. Hãy mở link và sao chép link món bạn muốn mua.")
		}
	}
	return "", "", "", errors.New("Link có quá nhiều chuyển hướng. Hãy dán link sản phẩm trực tiếp.")
}
