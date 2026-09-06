package capture

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/forward"
	"github.com/you/omnihook/internal/verify"
)

// Handler captures ALL /hook/:slug/* preserving raw bytes.
type Handler struct {
	DB  *sql.DB
	Cfg config.Config
	Hub *Hub
}

// Hub broadcasts new request IDs over SSE.
type Hub struct {
	ch chan string
}

func NewHub() *Hub { return &Hub{ch: make(chan string, 256)} }
func (h *Hub) Broadcast(id string) {
	select {
	case h.ch <- id:
	default:
	}
}
func (h *Hub) Chan() <-chan string { return h.ch }

func headersMap(r *http.Request) map[string]string {
	m := map[string]string{}
	for k, vv := range r.Header {
		m[strings.ToLower(k)] = strings.Join(vv, ", ")
	}
	return m
}

// ServeHook handles /hook/...
func (h *Handler) ServeHook(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/hook/")
	if i := strings.Index(slug, "/"); i >= 0 {
		slug = slug[:i]
	}
	if slug == "" {
		http.Error(w, "missing slug", http.StatusBadRequest)
		return
	}
	var ep struct {
		Exists              bool
		Provider, SecretRef string
		Target              string
		RespStatus          int
		RespBody, RespCtype string
	}
	row := h.DB.QueryRow(`SELECT provider, secret_ref, target_url, response_status, response_body, response_content_type FROM endpoints WHERE slug=?`, slug)
	var provider, secret, target, respBody, respCtype string
	var status int
	if err := row.Scan(&provider, &secret, &target, &status, &respBody, &respCtype); err != nil {
		// Auto-create on first hit (frictionless dev UX, per PLAN F1).
		_, _ = h.DB.Exec(`INSERT OR IGNORE INTO endpoints(slug) VALUES(?)`, slug)
		provider, secret, target, status, respBody, respCtype = "generic", "", "", 200, `{"ok":true}`, "application/json"
		ep.Exists = false
	} else {
		ep.Exists = true
	}
	_ = ep

	limited := io.LimitReader(r.Body, h.Cfg.MaxBodyBytes+1)
	raw, _ := io.ReadAll(limited)
	truncated := 0
	if int64(len(raw)) > h.Cfg.MaxBodyBytes {
		raw = raw[:h.Cfg.MaxBodyBytes]
		truncated = 1
	}
	hdrs := headersMap(r)
	hdrsJSON, _ := json.Marshal(r.Header)
	res := verify.Chain(secret, provider, lowerHeaders(r), raw, time.Now())

	id := uuid.NewString()
	subPath := "/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/hook/"+slug), "/")
	if subPath == "/" || strings.HasPrefix(subPath, "//") {
		// normalize
	}
	_, _ = h.DB.Exec(`INSERT INTO requests(id, endpoint_slug, method, path, query, headers, content_type, body, body_size, truncated, verify_status, verify_error, fix_hint)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		id, slug, r.Method, subPath, r.URL.RawQuery, string(hdrsJSON),
		r.Header.Get("Content-Type"), raw, len(raw), truncated,
		res.Status, res.Error, res.FixHint)

	h.Hub.Broadcast(id)

	// Async forward if configured (never blocks capture response).
	if target != "" {
		go forward.Deliver(h.DB, id, target)
	}

	w.Header().Set("Content-Type", respCtype)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(respBody))
	_ = hdrs
}

func lowerHeaders(r *http.Request) map[string]string {
	m := map[string]string{}
	for k, v := range r.Header {
		m[k] = strings.Join(v, ", ")
	}
	return m
}
