// Package forward delivers captured webhooks to a configured localhost target.
//
// Design: fire-and-forget from the capture path. The provider response must
// never fail because forwarding failed — Deliver records its outcome into the
// `replays` table and only logs transport errors.
package forward

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/you/omnihook/internal/replay"
)

// Timeout for a single forward attempt (shorter than provider retry windows).
const timeout = 10 * time.Second

// Deliver POSTs the stored request (method, headers, raw body) to target and
// records status/latency into `replays`. Safe to call in a goroutine.
func Deliver(db *sql.DB, requestID, target string) (statusCode int, latencyMs int64, errMsg string) {
	if replay.Blocked(target) {
		errMsg = "blocked: SSRF guard (metadata host)"
		record(db, requestID, target, 0, 0, errMsg)
		return 0, 0, errMsg
	}
	var method, contentType, headersJSON string
	var body []byte
	if err := db.QueryRow(`SELECT method, content_type, headers, body FROM requests WHERE id=?`, requestID).
		Scan(&method, &contentType, &headersJSON, &body); err != nil {
		errMsg = "request not found: " + requestID
		record(db, requestID, target, 0, 0, errMsg)
		return 0, 0, errMsg
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	if err != nil {
		errMsg = err.Error()
		record(db, requestID, target, 0, 0, errMsg)
		return 0, 0, errMsg
	}
	// Restore original headers (stored as net/http Header JSON map), except
	// transport headers that must be recomputed for the new request.
	var stored http.Header
	if headersJSON != "" {
		_ = json.Unmarshal([]byte(headersJSON), &stored)
		for k, vv := range stored {
			if strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") {
				continue
			}
			for _, v := range vv {
				req.Header.Add(k, v)
			}
		}
	}
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("X-Omnihook-Forward", "true")
	req.Header.Set("X-Omnihook-Request-Id", requestID)
	start := time.Now()
	resp, err := client.Do(req)
	latencyMs = time.Since(start).Milliseconds()
	if err != nil {
		errMsg = err.Error()
		record(db, requestID, target, 0, latencyMs, errMsg)
		return 0, latencyMs, errMsg
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	record(db, requestID, target, resp.StatusCode, latencyMs, "")
	return resp.StatusCode, latencyMs, ""
}

func record(db *sql.DB, requestID, target string, code int, lat int64, errMsg string) {
	_, _ = db.Exec(`INSERT INTO replays(id, request_id, target_url, status_code, latency_ms, error) VALUES(?,?,?,?,?,?)`,
		uuid.NewString(), requestID, target, code, lat, errMsg)
}
