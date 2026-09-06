package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/you/omnihook/internal/capture"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/replay"
)

// Server wires capture + management API + embedded UI.
type Server struct {
	DB  *sql.DB
	Cfg config.Config
	Cap *capture.Handler
	Mux *http.ServeMux
}

// indexHTML serves web/index.html from disk when present (dev + docker),
// falling back to a minimal placeholder so the binary never depends on
// go:embed ../../ paths.
func indexHTML() []byte {
	for _, p := range []string{"web/index.html", "./web/index.html", "../web/index.html"} {
		if b, err := os.ReadFile(p); err == nil {
			return b
		}
	}
	return []byte(`<html><body><h3>OmniHook up. UI file web/index.html not found.</h3></body></html>`)
}

func New(db *sql.DB, cfg config.Config, cap *capture.Handler) *Server {
	s := &Server{DB: db, Cfg: cfg, Cap: cap, Mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	if s.Cfg.AccessToken == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hook/") || r.URL.Path == "/health" {
			next(w, r)
			return
		}
		if r.Header.Get("Authorization") == "Bearer "+s.Cfg.AccessToken {
			next(w, r)
			return
		}
		if c, err := r.Cookie("omnihook_token"); err == nil && c.Value == s.Cfg.AccessToken {
			next(w, r)
			return
		}
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ValidSlug constrains endpoint slugs to URL/router-safe characters so a
// slug can never escape its /hook/:slug or /api/endpoints/:slug scope.
// Shared with the CLI so both enforce identical rules.
func ValidSlug(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		ok := c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_'
		if !ok {
			return false
		}
	}
	// First char must be alphanumeric (no leading -/_).
	c := s[0]
	return c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9'
}

func (s *Server) routes() {
	m := s.Mux
	m.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		dbOK := "up"
		if err := s.DB.Ping(); err != nil {
			dbOK = "down:" + err.Error()
		}
		writeJSON(w, map[string]string{"ok": "true", "db": dbOK, "version": s.Cfg.Version})
	})
	// Capture (public by design).
	m.HandleFunc("/hook/", s.Cap.ServeHook)

	// UI.
	m.HandleFunc("/", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(indexHTML())
	}))

	// Endpoints CRUD.
	m.HandleFunc("/api/endpoints", s.auth(s.handleEndpoints))
	m.HandleFunc("/api/endpoints/", s.auth(s.handleEndpointSub))
	m.HandleFunc("/api/requests/", s.auth(s.handleRequestSub))
}

func (s *Server) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		rows, _ := s.DB.Query(`SELECT slug, name, provider, target_url FROM endpoints ORDER BY slug`)
		defer func() {
			if rows != nil {
				rows.Close()
			}
		}()
		out := []map[string]string{}
		if rows != nil {
			for rows.Next() {
				var slug, name, provider, target string
				_ = rows.Scan(&slug, &name, &provider, &target)
				out = append(out, map[string]string{"slug": slug, "name": name, "provider": provider, "target_url": target})
			}
		}
		writeJSON(w, out)
	case "POST":
		var in struct {
			Slug     string `json:"slug"`
			Provider string `json:"provider"`
			Secret   string `json:"secret"`
			Target   string `json:"target_url"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Slug == "" {
			in.Slug = uuid.NewString()[:8]
		}
		if !ValidSlug(in.Slug) {
			http.Error(w, "slug must match ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$", 400)
			return
		}
		if in.Provider == "" {
			in.Provider = "generic"
		}
		_, err := s.DB.Exec(`INSERT INTO endpoints(slug,name,provider,secret_ref,target_url) VALUES(?,?,?,?,?)
			ON CONFLICT(slug) DO UPDATE SET provider=excluded.provider, secret_ref=excluded.secret_ref, target_url=excluded.target_url`,
			in.Slug, in.Slug, in.Provider, in.Secret, in.Target)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		base := s.Cfg.PublicURL
		if base == "" {
			base = "http://localhost:" + s.Cfg.Port
		}
		writeJSON(w, map[string]string{"slug": in.Slug, "capture_url": base + "/hook/" + in.Slug})
	default:
		http.Error(w, "method not allowed", 405)
	}
}

func (s *Server) handleEndpointSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/endpoints/")
	parts := strings.Split(rest, "/")
	slug := parts[0]
	if len(parts) == 1 {
		switch r.Method {
		case "GET":
			var name, provider, target string
			var status int
			if err := s.DB.QueryRow(`SELECT name, provider, target_url, response_status FROM endpoints WHERE slug=?`, slug).
				Scan(&name, &provider, &target, &status); err != nil {
				http.Error(w, "not found", 404)
				return
			}
			writeJSON(w, map[string]any{"slug": slug, "name": name, "provider": provider, "target_url": target, "response_status": status})
			return
		case "PATCH":
			var in struct {
				Name       *string `json:"name"`
				Provider   *string `json:"provider"`
				Secret     *string `json:"secret"`
				Target     *string `json:"target_url"`
				RespStatus *int    `json:"response_status"`
				RespBody   *string `json:"response_body"`
				RespCtype  *string `json:"response_content_type"`
			}
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				http.Error(w, "bad JSON", 400)
				return
			}
			// Targeted updates only; secret can be set after auto-create.
			updates := map[string]any{}
			if in.Name != nil {
				updates["name"] = *in.Name
			}
			if in.Provider != nil {
				updates["provider"] = *in.Provider
			}
			if in.Secret != nil {
				updates["secret_ref"] = *in.Secret
			}
			if in.Target != nil {
				updates["target_url"] = *in.Target
			}
			if in.RespStatus != nil {
				updates["response_status"] = *in.RespStatus
			}
			if in.RespBody != nil {
				updates["response_body"] = *in.RespBody
			}
			if in.RespCtype != nil {
				updates["response_content_type"] = *in.RespCtype
			}
			if len(updates) == 0 {
				http.Error(w, "nothing to update", 400)
				return
			}
			setParts := make([]string, 0, len(updates))
			vals := make([]any, 0, len(updates)+1)
			for col, v := range updates {
				setParts = append(setParts, col+"=?")
				vals = append(vals, v)
			}
			vals = append(vals, slug)
			res, err := s.DB.Exec(`UPDATE endpoints SET `+strings.Join(setParts, ", ")+` WHERE slug=?`, vals...)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if n, _ := res.RowsAffected(); n == 0 {
				http.Error(w, "not found", 404)
				return
			}
			writeJSON(w, map[string]string{"slug": slug, "updated": "true"})
			return
		case "DELETE":
			res, err := s.DB.Exec(`DELETE FROM endpoints WHERE slug=?`, slug)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			if n, _ := res.RowsAffected(); n == 0 {
				http.Error(w, "not found", 404)
				return
			}
			writeJSON(w, map[string]string{"slug": slug, "deleted": "true"})
			return
		}
		http.Error(w, "method not allowed", 405)
		return
	}
	if len(parts) == 2 && parts[1] == "requests" && r.Method == "GET" {
		rows, _ := s.DB.Query(`SELECT id, method, path, verify_status, received_at FROM requests WHERE endpoint_slug=? ORDER BY received_at DESC LIMIT 50`, slug)
		defer func() {
			if rows != nil {
				rows.Close()
			}
		}()
		out := []map[string]string{}
		if rows != nil {
			for rows.Next() {
				var id, method, path, vs, at string
				_ = rows.Scan(&id, &method, &path, &vs, &at)
				out = append(out, map[string]string{"id": id, "method": method, "path": path, "verify_status": vs, "received_at": at})
			}
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "requests" && r.Method == "DELETE" {
		res, err := s.DB.Exec(`DELETE FROM requests WHERE endpoint_slug=?`, slug)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		n, _ := res.RowsAffected()
		writeJSON(w, map[string]any{"slug": slug, "deleted": n})
		return
	}
	if len(parts) == 2 && parts[1] == "stream" {
		// SSE live stream.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fl, _ := w.(http.Flusher)
		ch, unsub := s.Cap.Hub.Subscribe()
		defer unsub()
		ctx := r.Context()
		for {
			select {
			case <-ctx.Done():
				return
			case id := <-ch:
				_, _ = w.Write([]byte("event: request\ndata: " + id + "\n\n"))
				if fl != nil {
					fl.Flush()
				}
			}
		}
	}
	http.NotFound(w, r)
}

func (s *Server) handleRequestSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/requests/")
	if strings.HasSuffix(rest, "/replay") && r.Method == "POST" {
		id := strings.TrimSuffix(rest, "/replay")
		var in struct {
			Target  string            `json:"target"`
			Headers map[string]string `json:"headers"`
			BodyB64 string            `json:"body_base64"`
			Times   int               `json:"times"`
			DelayMs int               `json:"delay_ms"`
			Resign  bool              `json:"resign"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Target == "" {
			http.Error(w, "target required", 400)
			return
		}
		times := in.Times
		if times < 1 {
			times = 1
		}
		if times > 50 {
			http.Error(w, "times capped at 50", 400)
			return
		}
		var bodyOverride []byte
		if in.BodyB64 != "" {
			var err error
			bodyOverride, err = base64.StdEncoding.DecodeString(in.BodyB64)
			if err != nil {
				http.Error(w, "body_base64: invalid base64", 400)
				return
			}
		}
		results := make([]map[string]any, 0, times)
		for i := 0; i < times; i++ {
			if i > 0 && in.DelayMs > 0 {
				select {
				case <-r.Context().Done():
					writeJSON(w, map[string]any{"results": results, "cancelled": true})
					return
				case <-time.After(time.Duration(in.DelayMs) * time.Millisecond):
				}
			}
			code, lat, body := replay.SendWithOptions(s.DB, id, in.Target,
				replay.Options{Headers: in.Headers, Body: bodyOverride, Resign: in.Resign})
			results = append(results, map[string]any{"status_code": code, "latency_ms": lat, "body": body})
		}
		// Backward compat: single replay keeps the flat shape.
		if times == 1 {
			writeJSON(w, results[0])
			return
		}
		writeJSON(w, map[string]any{"results": results})
		return
	}
	id := rest
	if r.Method == "DELETE" {
		res, err := s.DB.Exec(`DELETE FROM requests WHERE id=?`, id)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if n, _ := res.RowsAffected(); n == 0 {
			http.Error(w, "not found", 404)
			return
		}
		writeJSON(w, map[string]string{"id": id, "deleted": "true"})
		return
	}
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	var method, path, headers, ctype, vs, verr, hint, at, query string
	var body []byte
	err := s.DB.QueryRow(`SELECT method, path, headers, content_type, verify_status, verify_error, fix_hint, received_at, query, body FROM requests WHERE id=?`, id).
		Scan(&method, &path, &headers, &ctype, &vs, &verr, &hint, &at, &query, &body)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, map[string]any{
		"id": id, "method": method, "path": path, "headers": headers,
		"content_type": ctype, "verify_status": vs, "verify_error": verr,
		"fix_hint": hint, "received_at": at, "query": query, "body_text": string(body),
	})
}
