package affiliate

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"time"
)

func ValidateURL(raw string) (*url.URL, error) {
	if len(raw) > 2048 {
		return nil, errors.New("URL quá dài")
	}
	u, e := url.Parse(raw)
	if e != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
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

func ProductIDs(raw string) (string, string, error) {
	u, e := ValidateURL(raw)
	if e != nil {
		return "", "", e
	}
	for _, r := range []*regexp.Regexp{direct, slug} {
		p := r.FindStringSubmatch(u.Path)
		if len(p) == 3 {
			return p[1], p[2], nil
		}
	}
	return "", "", errors.New("Không tìm thấy shop/item ID")
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
	if _, e := ValidateURL(raw); e != nil {
		return "", "", "", e
	}
	if shop, item, e := ProductIDs(raw); e == nil {
		return shop, item, "https://shopee.vn/product/" + shop + "/" + item, nil
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: safeDial(net.DefaultResolver.LookupIP, dialer.DialContext)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 8 * time.Second, Transport: transport, CheckRedirect: safeRedirect}
	req, e := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if e != nil {
		return "", "", "", e
	}
	resp, e := client.Do(req)
	if e != nil {
		return "", "", "", e
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	shop, item, e := ProductIDs(resp.Request.URL.String())
	return shop, item, resp.Request.URL.String(), e
}
