package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"
)

func proxySignature(key, timestamp, ip, method, uri string) string {
	secret, err := hex.DecodeString(key)
	if err != nil || len(secret) != 32 {
		return ""
	}
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(strings.Join([]string{timestamp, ip, method, uri}, "\n")))
	return hex.EncodeToString(h.Sum(nil))
}
func clientIP(r *http.Request, key string, now time.Time) string {
	fallback, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		fallback = r.RemoteAddr
	}
	ip, stamp, proof := r.Header.Get("X-HX-Client-IP"), r.Header.Get("X-HX-Proxy-Time"), r.Header.Get("X-HX-Proxy-Signature")
	seconds, err := strconv.ParseInt(stamp, 10, 64)
	if err != nil || net.ParseIP(ip) == nil || seconds < now.Unix()-60 || seconds > now.Unix()+60 {
		return fallback
	}
	expected := proxySignature(key, stamp, ip, r.Method, r.URL.RequestURI())
	if expected == "" || !hmac.Equal([]byte(expected), []byte(proof)) {
		return fallback
	}
	return ip
}
