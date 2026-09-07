// Package outbound is the single HTTP client policy for replay and
// forwarding (review #15, #03): fail-closed redirects, hop-by-hop stripping,
// timeouts, and resolved-address metadata validation.
package outbound

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

var blockedHosts = map[string]bool{
	"169.254.169.254":          true,
	"100.100.100.200":          true, // Alibaba Cloud metadata
	"metadata.google.internal": true,
	"metadata.google":          true,
	"instance-data":            true,
}

// isMetadataIP reports whether ip is a cloud metadata address. Only metadata
// ranges are blocked — localhost and private development destinations stay
// allowed by design. IPv4-mapped IPv6 forms normalize via To4.
func isMetadataIP(ip net.IP) bool {
	if v4 := ip.To4(); v4 != nil {
		return v4[0] == 169 && v4[1] == 254 || v4.Equal(net.ParseIP("100.100.100.200"))
	}
	return false
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
		return isMetadataIP(ip)
	}
	return false
}

// lookupIP resolves host to addresses. A variable (not a direct call) so
// tests can simulate DNS aliases/rebinding without network access.
var lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
	resolver := net.DefaultResolver
	addrs, err := resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	out := make([]net.IPAddr, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, net.IPAddr{IP: a.IP})
	}
	return out, nil
}

// dialContext dials only after every resolved address passes the metadata
// policy. Each new connection re-resolves, so a rebinding between deliveries
// is validated again (kept-alive connections reuse their validated address).
func dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	// Literal IPs skip DNS but not the policy.
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		if isMetadataIP(ip) {
			return nil, fmt.Errorf("outbound blocked: metadata address %s", host)
		}
		return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, addr)
	}
	addrs, err := lookupIP(ctx, host)
	if err != nil {
		return nil, err
	}
	var firstErr error
	for _, a := range addrs {
		if isMetadataIP(a.IP) {
			firstErr = fmt.Errorf("outbound blocked: %s resolves to metadata address %s", host, a.IP)
			continue
		}
		conn, derr := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if derr == nil {
			return conn, nil
		}
		firstErr = derr
	}
	if firstErr == nil {
		firstErr = fmt.Errorf("outbound blocked: %s has no usable addresses", host)
	}
	return nil, firstErr
}

// Transport returns a transport with resolved-address validation.
// Proxying follows ProxyFromEnvironment for corporate compatibility; when a
// proxy handles the connection, dial-time validation cannot apply, but the
// URL-level Blocked() policy always does — metadata URLs never leave the host.
func Transport(timeout time.Duration) *http.Transport {
	return &http.Transport{
		DialContext:         dialContext,
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 10 * time.Second,
	}
}

// Client returns an HTTP client that never silently follows redirects:
// 3xx responses are returned to the caller (and recorded) instead of turning
// a POST replay into a GET somewhere else.
func Client(timeout time.Duration) *http.Client {
	tr := Transport(timeout)
	return &http.Client{
		Transport: tr,
		Timeout:   timeout,
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
