package api

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"os"
	"strings"

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
		if in.Provider == "" {
			in.Provider = "generic"
		}
		_, err := s.DB.Exec(`INSERT OR IGNORE INTO endpoints(slug,name,provider,secret_ref,target_url) VALUES(?,?,?,?,?)`,
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
	if len(parts) == 2 && parts[1] == "stream" {
		// SSE live stream.
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fl, _ := w.(http.Flusher)
		ch := s.Cap.Hub.Chan()
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
			Target string `json:"target"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Target == "" {
			http.Error(w, "target required", 400)
			return
		}
		code, lat, body := replay.Send(s.DB, id, in.Target, nil, nil)
		writeJSON(w, map[string]any{"status_code": code, "latency_ms": lat, "body": body})
		return
	}
	id := rest
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
