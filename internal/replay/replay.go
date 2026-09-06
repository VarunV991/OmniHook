package replay

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/you/omnihook/internal/outbound"
	"github.com/you/omnihook/internal/verify"
)

var blockedHosts = []string{"169.254.169.254", "metadata.google.internal", "metadata.google", "instance-data"}

// SSRF guard: block cloud metadata hosts. Localhost is allowed by design.
func blocked(target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return true
	}
	host := strings.ToLower(u.Hostname())
	for _, b := range blockedHosts {
		if host == b {
			return true
		}
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLinkLocalUnicast() {
		// 169.254.x.x link-local (metadata) blocked; 127.0.0.1 allowed.
		if strings.HasPrefix(host, "169.254.") {
			return true
		}
	}
	return false
}

// Blocked reports whether target is barred by the SSRF guard.
// Localhost is allowed by design (forwarding to local dev is the product).
func Blocked(target string) bool { return blocked(target) }

// Send replays a stored request body to target, records result.
func Send(db *sql.DB, requestID, target string, headerOverride map[string]string, bodyOverride []byte) (int, int64, string) {
	return SendWithOptions(db, requestID, target, Options{Headers: headerOverride, Body: bodyOverride})
}

// Options extends Send with re-signing: refresh time-sensitive signature
// headers (Stripe/Standard/...) using the endpoint secret so old captures
// replay as PASS instead of failing timestamp tolerance.
type Options struct {
	Headers map[string]string
	Body    []byte
	Resign  bool
}

// SendWithOptions replays with Options. Explicit Headers win over resigned
// headers; resigned headers win over stored originals.
func SendWithOptions(db *sql.DB, requestID, target string, opts Options) (int, int64, string) {
	var method, contentType, headersJSON, provider, secret, verifiedBy string
	var body []byte
	err := db.QueryRow(`SELECT r.method, r.content_type, r.headers, r.body,
		COALESCE(e.provider,'generic'), COALESCE(e.secret_ref,''),
		COALESCE(r.verified_by,'')
		FROM requests r LEFT JOIN endpoints e ON e.slug=r.endpoint_slug WHERE r.id=?`,
		requestID).Scan(&method, &contentType, &headersJSON, &body, &provider, &secret, &verifiedBy)
	if err != nil {
		return 0, 0, "request not found: " + requestID
	}
	if blocked(target) {
		msg := "blocked: SSRF guard (metadata host)"
		_, _ = db.Exec(`INSERT INTO replays(id, request_id, target_url, status_code, latency_ms, error) VALUES(?,?,?,?,?,?)`,
			uuid.NewString(), requestID, target, 0, 0, msg)
		return 0, 0, msg
	}
	if opts.Body != nil {
		body = opts.Body
	}
	var stored http.Header
	if headersJSON != "" {
		_ = json.Unmarshal([]byte(headersJSON), &stored)
	}
	flat := map[string]string{}
	for k, vv := range stored {
		flat[k] = strings.Join(vv, ", ")
	}
	merged := map[string]string{}
	if opts.Resign {
		// Prefer the provider actually detected at capture; fall back to the
		// endpoint's configured provider (covers rows predating verified_by).
		effective := verifiedBy
		if effective == "" || effective == "generic" {
			effective = provider
		}
		fresh, err := verify.RefreshSignatures(effective, secret, flat, body, time.Now())
		if err != nil {
			msg := "resign unavailable: " + err.Error()
			_, _ = db.Exec(`INSERT INTO replays(id, request_id, target_url, status_code, latency_ms, error) VALUES(?,?,?,?,?,?)`,
				uuid.NewString(), requestID, target, 0, 0, msg)
			return 0, 0, msg
		}
		for k, v := range fresh {
			merged[textproto.CanonicalMIMEHeaderKey(k)] = v
		}
	}
	for k, v := range opts.Headers {
		merged[textproto.CanonicalMIMEHeaderKey(k)] = v
	}
	client := outbound.Client(15 * time.Second)
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err.Error()
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// Restore original headers so the replay is byte-faithful, then apply
	// resigned/explicit overrides on top. Content-Type was set explicitly
	// above (from the stored column) to avoid duplicate header values.
	for k, vv := range stored {
		if strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") ||
			strings.EqualFold(k, "content-type") {
			continue
		}
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}
	req.Header.Set("X-Omnihook-Replay", "true")
	req.Header.Set("X-Omnihook-Request-Id", requestID)
	for k, v := range merged {
		if strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") {
			continue
		}
		req.Header.Set(k, v)
	}
	// Connection-scoped headers belong to the original connection, not the replay.
	outbound.StripHopByHop(req.Header)
	start := time.Now()
	resp, err := client.Do(req)
	lat := time.Since(start).Milliseconds()
	if err != nil {
		_, _ = db.Exec(`INSERT INTO replays(id, request_id, target_url, status_code, latency_ms, error) VALUES(?,?,?,?,?,?)`,
			uuid.NewString(), requestID, target, 0, lat, err.Error())
		return 0, lat, err.Error()
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	_, _ = db.Exec(`INSERT INTO replays(id, request_id, target_url, status_code, latency_ms, error) VALUES(?,?,?,?,?,?)`,
		uuid.NewString(), requestID, target, resp.StatusCode, lat, "")
	return resp.StatusCode, lat, string(respBody)
}
