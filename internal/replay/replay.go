package replay

import (
	"bytes"
	"database/sql"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
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
	if blocked(target) {
		return 0, 0, "blocked: SSRF guard (metadata host)"
	}
	var method, contentType string
	var body []byte
	err := db.QueryRow(`SELECT method, content_type, body FROM requests WHERE id=?`, requestID).Scan(&method, &contentType, &body)
	if err != nil {
		return 0, 0, "request not found: " + requestID
	}
	if bodyOverride != nil {
		body = bodyOverride
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		return 0, 0, err.Error()
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Omnihook-Replay", "true")
	req.Header.Set("X-Omnihook-Request-Id", requestID)
	for k, v := range headerOverride {
		if strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") {
			continue
		}
		req.Header.Set(k, v)
	}
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
