package capture

import (
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/forward"
	"github.com/you/omnihook/internal/ratelimit"
	"github.com/you/omnihook/internal/verify"
)

// Handler captures ALL /hook/:slug/* preserving raw bytes.
type Handler struct {
	DB  *sql.DB
	Cfg config.Config
	Hub *Hub
	// Limiter gates responses (store always, answer 429 when empty).
	// Nil means no limiting. NewHandler wires it from Cfg.
	Limiter *ratelimit.Limiter
}

// NewHandler builds a Handler with rate limiting from cfg (0 disables).
func NewHandler(db *sql.DB, cfg config.Config, hub *Hub) *Handler {
	return &Handler{DB: db, Cfg: cfg, Hub: hub, Limiter: ratelimit.New(cfg.RateLimitRPS)}
}

// clientIP prefers X-Forwarded-For (tunnel setups) then RemoteAddr.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i >= 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i >= 0 {
		return host[:i]
	}
	return host
}

// Hub broadcasts new request IDs over SSE. Each subscriber gets its own
// channel so multiple tabs/clients all receive every event (a single shared
// channel would deal messages out to competing readers instead).
type Hub struct {
	mu   sync.Mutex
	subs map[chan string]struct{}
}

func NewHub() *Hub { return &Hub{subs: map[chan string]struct{}{}} }

func (h *Hub) Broadcast(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- id:
		default: // slow reader: drop, live list refreshes anyway
		}
	}
}

// Subscribe returns a channel receiving every broadcast until unsub is called.
func (h *Hub) Subscribe() (chan string, func()) {
	ch := make(chan string, 16)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
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
	row := h.DB.QueryRow(`SELECT provider, secret_ref, target_url, response_status, response_body, response_content_type FROM endpoints WHERE slug=?`, slug)
	var provider, secret, target, respBody, respCtype string
	var status int
	if err := row.Scan(&provider, &secret, &target, &status, &respBody, &respCtype); err != nil {
		// Auto-create on first hit (frictionless dev UX, per PLAN F1).
		_, _ = h.DB.Exec(`INSERT OR IGNORE INTO endpoints(slug) VALUES(?)`, slug)
		provider, secret, target, status, respBody, respCtype = "generic", "", "", 200, `{"ok":true}`, "application/json"
	}

	limited := io.LimitReader(r.Body, h.Cfg.MaxBodyBytes+1)
	raw, _ := io.ReadAll(limited)
	truncated := 0
	if int64(len(raw)) > h.Cfg.MaxBodyBytes {
		raw = raw[:h.Cfg.MaxBodyBytes]
		truncated = 1
	}
	hdrsJSON, _ := json.Marshal(r.Header)
	res := verify.Chain(secret, provider, lowerHeaders(r), raw, time.Now())

	id := uuid.NewString()
	subPath := "/" + strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/hook/"+slug), "/")
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

	// Rate gate on the RESPONSE only: the request is already stored as
	// evidence; 429 + Retry-After tells the provider to back off.
	if h.Limiter != nil && !h.Limiter.Allow(clientIP(r)) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":"rate limited","retry_after":1}`))
		return
	}

	w.Header().Set("Content-Type", respCtype)
	w.WriteHeader(status)
	_, _ = w.Write([]byte(respBody))
}

func lowerHeaders(r *http.Request) map[string]string {
	m := map[string]string{}
	for k, v := range r.Header {
		m[k] = strings.Join(v, ", ")
	}
	return m
}
