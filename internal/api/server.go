package api

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/you/omnihook/internal/capture"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/replay"
	"github.com/you/omnihook/internal/slug"
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

// loginHTML serves web/login.html from disk when present, else a minimal
// inline form with the same behavior (POST token to /api/login).
func loginHTML() []byte {
	for _, p := range []string{"web/login.html", "./web/login.html", "../web/login.html"} {
		if b, err := os.ReadFile(p); err == nil {
			return b
		}
	}
	return []byte(`<html><body><form method="post" action="/api/login">Token: <input type="password" name="token"/><button>Sign in</button></form></body></html>`)
}

func New(db *sql.DB, cfg config.Config, cap *capture.Handler) *Server {
	s := &Server{DB: db, Cfg: cfg, Cap: cap, Mux: http.NewServeMux()}
	s.routes()
	return s
}

// authed reports whether r carries the configured access token.
func (s *Server) authed(r *http.Request) bool {
	if s.Cfg.AccessToken == "" {
		return true
	}
	if r.Header.Get("Authorization") == "Bearer "+s.Cfg.AccessToken {
		return true
	}
	if c, err := r.Cookie(cookieName); err == nil && c.Value == s.Cfg.AccessToken {
		return true
	}
	return false
}

// cookieName is the session cookie set by /api/login. SameSite=Lax blocks
// cross-site POSTs; the UI is same-origin so fetch works. Secure is unset
// because local development is plain HTTP — do not expose the UI to the
// public internet and expect cookie confidentiality.
const cookieName = "omnihook_token"

func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	if s.Cfg.AccessToken == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/hook/") || r.URL.Path == "/health" ||
			r.URL.Path == "/login" || r.URL.Path == "/api/login" {
			next(w, r)
			return
		}
		if s.authed(r) {
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

// maxMgmtBody caps management JSON input (endpoints, replay options).
const maxMgmtBody = 64 * 1024

// decodeJSON bounds input size and rejects malformed or trailing JSON so a
// truncated `{` can never create state or trigger a partial action.
func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return fmt.Errorf("empty body")
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, maxMgmtBody+1))
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("trailing data after JSON value")
	}
	return nil
}

func (s *Server) routes() {
	m := s.Mux
	m.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if err := s.DB.Ping(); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(w).Encode(map[string]string{"ok": "false", "db": "down", "version": s.Cfg.Version})
			return
		}
		writeJSON(w, map[string]string{"ok": "true", "db": "up", "version": s.Cfg.Version})
	})
	// Capture (public by design).
	m.HandleFunc("/hook/", s.Cap.ServeHook)

	// UI: serve the login shell (not a 401) when gated and unauthenticated,
	// so a fresh browser can actually sign in (review #04). This route is
	// intentionally outside s.auth: it decides shell-vs-app itself.
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if s.Cfg.AccessToken != "" && !s.authed(r) {
			w.Header().Set("Content-Type", "text/html")
			_, _ = w.Write(loginHTML())
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(indexHTML())
	})
	m.HandleFunc("/login", func(w http.ResponseWriter, r *http.Request) {
		if s.Cfg.AccessToken == "" || s.authed(r) {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write(loginHTML())
	})
	m.HandleFunc("/api/login", s.handleLogin)
	m.HandleFunc("/api/logout", s.handleLogout)

	// Endpoints CRUD.
	m.HandleFunc("/api/endpoints", s.auth(s.handleEndpoints))
	m.HandleFunc("/api/endpoints/", s.auth(s.handleEndpointSub))
	m.HandleFunc("/api/requests/", s.auth(s.handleRequestSub))
}

// handleLogin exchanges the access token for a session cookie (review #04).
// The cookie is HttpOnly + SameSite=Lax with a 12h lifetime. Secure is unset:
// local development is plain HTTP, so this cookie must never cross the public
// internet — tunnel + ACCESS_TOKEN setups rely on the tunnel's TLS, and the
// management surface should not be exposed beyond trusted machines.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if s.Cfg.AccessToken == "" {
		http.Error(w, "login not required (ACCESS_TOKEN unset)", 400)
		return
	}
	var in struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &in); err != nil || in.Token == "" {
		http.Error(w, "invalid login request", 400)
		return
	}
	if in.Token != s.Cfg.AccessToken {
		http.Error(w, "invalid token", 401)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    s.Cfg.AccessToken,
		Path:     "/",
		MaxAge:   12 * 3600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", 405)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handleEndpoints(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case "GET":
		rows, err := s.DB.Query(`SELECT slug, name, provider, target_url FROM endpoints ORDER BY slug`)
		if err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var slug, name, provider, target string
			if err := rows.Scan(&slug, &name, &provider, &target); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			out = append(out, map[string]string{"slug": slug, "name": name, "provider": provider, "target_url": target})
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		writeJSON(w, out)
	case "POST":
		var in struct {
			Slug     string `json:"slug"`
			Provider string `json:"provider"`
			Secret   string `json:"secret"`
			Target   string `json:"target_url"`
		}
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), 400)
			return
		}
		if in.Slug == "" {
			in.Slug = uuid.NewString()[:8]
		}
		if !slug.Valid(in.Slug) {
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
			http.Error(w, "storage unavailable", 500)
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
			if err := decodeJSON(r, &in); err != nil {
				http.Error(w, "invalid JSON: "+err.Error(), 400)
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
				if *in.RespStatus < 200 || *in.RespStatus > 599 {
					http.Error(w, "response_status must be 200-599", 400)
					return
				}
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
				http.Error(w, "storage unavailable", 500)
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
				http.Error(w, "storage unavailable", 500)
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
		rows, err := s.DB.Query(`SELECT id, method, path, verify_status, received_at FROM requests WHERE endpoint_slug=? ORDER BY received_at DESC LIMIT 50`, slug)
		if err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, method, path, vs, at string
			if err := rows.Scan(&id, &method, &path, &vs, &at); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			out = append(out, map[string]string{"id": id, "method": method, "path": path, "verify_status": vs, "received_at": at})
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		writeJSON(w, out)
		return
	}
	if len(parts) == 2 && parts[1] == "requests" && r.Method == "DELETE" {
		res, err := s.DB.Exec(`DELETE FROM requests WHERE endpoint_slug=?`, slug)
		if err != nil {
			http.Error(w, "storage unavailable", 500)
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
		if err := decodeJSON(r, &in); err != nil {
			http.Error(w, "invalid JSON: "+err.Error(), 400)
			return
		}
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
		if in.DelayMs < 0 || in.DelayMs > 60000 {
			http.Error(w, "delay_ms must be 0-60000", 400)
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
			http.Error(w, "storage unavailable", 500)
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
