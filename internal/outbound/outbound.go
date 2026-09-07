// Package outbound is the single HTTP client policy for replay and
// forwarding (review #15, #03): fail-closed redirects, hop-by-hop stripping,
// timeouts. DNS-rebinding validation is phase 2 (see review #03).
package outbound

import (
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var blockedHosts = map[string]bool{
	"169.254.169.254":          true,
	"metadata.google.internal": true,
	"metadata.google":          true,
	"instance-data":            true,
}

// Blocked reports whether target is barred by the outbound SSRF policy.
// Localhost and private development destinations remain allowed by design.
// IPv4-mapped IPv6 addresses are normalized before checking metadata ranges.
func Blocked(target string) bool {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return true
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if blockedHosts[host] {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		if v4 := ip.To4(); v4 != nil && v4[0] == 169 && v4[1] == 254 {
			return true
		}
	}
	return false
}

// Client returns an HTTP client that never silently follows redirects:
// 3xx responses are returned to the caller (and recorded) instead of turning
// a POST replay into a GET somewhere else.
func Client(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// hopHeaders are never forwarded: they belong to the original connection
// (RFC 9110 §7.6.1), plus whatever Connection: names.
func hopHeaders(h http.Header) map[string]bool {
	out := map[string]bool{
		"connection": true, "keep-alive": true, "proxy-authenticate": true,
		"proxy-authorization": true, "te": true, "trailer": true,
		"transfer-encoding": true, "upgrade": true,
	}
	for _, v := range strings.Split(h.Get("Connection"), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out[strings.ToLower(v)] = true
		}
	}
	return out
}

// StripHopByHop removes connection-scoped headers in place.
func StripHopByHop(h http.Header) {
	hops := hopHeaders(h)
	for k := range h {
		if hops[strings.ToLower(k)] {
			h.Del(k)
		}
	}
	h.Del("Connection")
}
