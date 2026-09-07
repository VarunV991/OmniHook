package api

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/you/omnihook/internal/capture"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/replay"
	"github.com/you/omnihook/internal/slug"
	"github.com/you/omnihook/internal/verify"
	"github.com/you/omnihook/internal/webui"
)

// Server wires capture + management API + embedded UI.
type Server struct {
	DB  *sql.DB
	Cfg config.Config
	Cap *capture.Handler
	Mux *http.ServeMux
}

// indexHTML serves the inbox UI: embedded asset with WEB_DIR override.
func indexHTML() []byte { return webui.Index() }

// loginHTML serves the sign-in shell: embedded asset with WEB_DIR override.
func loginHTML() []byte { return webui.Login() }

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

// cursor is a keyset page position: (received_at, id) of the last row seen.
// Empty cursor starts from the newest.
type cursor struct {
	Time string
	ID   string
}

func parseCursor(r *http.Request) (limit int, c cursor, err error) {
	limit = 50
	if v := r.URL.Query().Get("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n < 1 || n > 100 {
			return 0, cursor{}, fmt.Errorf("limit must be 1-100")
		}
		limit = n
	}
	if v := r.URL.Query().Get("cursor"); v != "" {
		parts := strings.SplitN(v, "|", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return 0, cursor{}, fmt.Errorf("cursor must be received_at|id")
		}
		c = cursor{Time: parts[0], ID: parts[1]}
	} else {
		// Start beyond any RFC3339 timestamp stored by OmniHook.
		c = cursor{Time: "9999-12-31T23:59:59.999999999Z", ID: "~"}
	}
	return limit, c, nil
}

// nextCursor builds the cursor for the following page. rows is the raw
// limit+1 fetch: a cursor exists only when an extra row proves another page.
// The caller must trim rows to limit itself.
func nextCursor(rows []map[string]string, limit int) (page []map[string]string, cursor string) {
	if len(rows) > limit {
		last := rows[limit-1]
		return rows[:limit], last["received_at"] + "|" + last["id"]
	}
	return rows, ""
}

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
	// Aggregated inbox: recent requests across endpoints in ONE call, so the
	// UI does not fan out 1+N requests per refresh (review #25).
	m.HandleFunc("/api/requests", s.auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		limit, cursor, err := parseCursor(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rows, qerr := s.DB.Query(`SELECT id, endpoint_slug, method, path, verify_status, strftime('%Y-%m-%dT%H:%M:%fZ', received_at) FROM requests
			WHERE (strftime('%Y-%m-%dT%H:%M:%fZ', received_at) < ? OR (strftime('%Y-%m-%dT%H:%M:%fZ', received_at) = ? AND id < ?))
			ORDER BY strftime('%Y-%m-%dT%H:%M:%fZ', received_at) DESC, id DESC LIMIT ?`, cursor.Time, cursor.Time, cursor.ID, limit+1)
		if qerr != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var id, slug, method, path, vs, at string
			if err := rows.Scan(&id, &slug, &method, &path, &vs, &at); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			out = append(out, map[string]string{"id": id, "endpoint": slug, "method": method, "path": path, "verify_status": vs, "received_at": at})
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		out, cur := nextCursor(out, limit)
		writeJSON(w, map[string]any{"requests": out, "next_cursor": cur})
	}))
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
			Slug     string  `json:"slug"`
			Provider *string `json:"provider"`
			Secret   *string `json:"secret"`
			Target   *string `json:"target_url"`
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
		provider := "generic"
		if in.Provider != nil {
			provider = *in.Provider
		}
		if !verify.ValidProvider(provider) {
			http.Error(w, "unknown provider (want stripe|github|standard|razorpay|shopify|generic|auto)", 400)
			return
		}
		// Create never destroys (review #27): insert defaults, then apply
		// only explicitly supplied fields. Explicit "" clears secret/target.
		if _, err := s.DB.Exec(`INSERT OR IGNORE INTO endpoints(slug,name,provider) VALUES(?,?,?)`,
			in.Slug, in.Slug, provider); err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		if in.Provider != nil {
			if _, err := s.DB.Exec(`UPDATE endpoints SET provider=? WHERE slug=?`, provider, in.Slug); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
		}
		if in.Secret != nil {
			if _, err := s.DB.Exec(`UPDATE endpoints SET secret_ref=? WHERE slug=?`, *in.Secret, in.Slug); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
		}
		if in.Target != nil {
			if _, err := s.DB.Exec(`UPDATE endpoints SET target_url=? WHERE slug=?`, *in.Target, in.Slug); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
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
			err := s.DB.QueryRow(`SELECT name, provider, target_url, response_status FROM endpoints WHERE slug=?`, slug).
				Scan(&name, &provider, &target, &status)
			if err == sql.ErrNoRows {
				http.Error(w, "not found", 404)
				return
			}
			if err != nil {
				http.Error(w, "storage unavailable", 500)
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
				if !verify.ValidProvider(*in.Provider) {
					http.Error(w, "unknown provider (want stripe|github|standard|razorpay|shopify|generic|auto)", 400)
					return
				}
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
		limit, cursor, err := parseCursor(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		rows, qerr := s.DB.Query(`SELECT id, method, path, verify_status, strftime('%Y-%m-%dT%H:%M:%fZ', received_at) FROM requests
			WHERE endpoint_slug=? AND (strftime('%Y-%m-%dT%H:%M:%fZ', received_at) < ? OR (strftime('%Y-%m-%dT%H:%M:%fZ', received_at) = ? AND id < ?))
			ORDER BY strftime('%Y-%m-%dT%H:%M:%fZ', received_at) DESC, id DESC LIMIT ?`, slug, cursor.Time, cursor.Time, cursor.ID, limit+1)
		if qerr != nil {
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
		out, cur := nextCursor(out, limit)
		writeJSON(w, map[string]any{"requests": out, "next_cursor": cur})
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
		// SSE live stream, filtered to this endpoint (review #23).
		var exists bool
		if err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM endpoints WHERE slug=?)`, slug).Scan(&exists); err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		if !exists {
			http.Error(w, "unknown endpoint", 404)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fl, _ := w.(http.Flusher)
		ch, unsub := s.Cap.Hub.Subscribe(slug)
		defer unsub()
		ctx := r.Context()
		// Initial flush + heartbeat keep idle proxies from closing the stream.
		_, _ = w.Write([]byte("event: ready\ndata: " + slug + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
		beat := time.NewTicker(20 * time.Second)
		defer beat.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-beat.C:
				if _, err := w.Write([]byte(": ping\n\n")); err != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			case ev := <-ch:
				if _, err := w.Write([]byte("event: request\ndata: " + ev.ID + "\n\n")); err != nil {
					return // slow/dead reader: stop, drops are documented
				}
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
			BodyB64 *string           `json:"body_base64"`
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
		// Validate before doing any work: malformed target or missing
		// request fail fast with a clear status (review #17).
		if _, err := url.ParseRequestURI(in.Target); err != nil {
			http.Error(w, "invalid target URL", 400)
			return
		}
		var exists bool
		if err := s.DB.QueryRow(`SELECT EXISTS(SELECT 1 FROM requests WHERE id=?)`, id).Scan(&exists); err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		if !exists {
			http.Error(w, "request not found", 404)
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
		var haveOverride bool
		if in.BodyB64 != nil {
			// Pointer presence distinguishes omitted from explicitly empty:
			// "" is a valid override meaning an empty body (review #18).
			var err error
			bodyOverride, err = base64.StdEncoding.DecodeString(*in.BodyB64)
			if err != nil {
				http.Error(w, "body_base64: invalid base64", 400)
				return
			}
			haveOverride = true
		}
		if !haveOverride {
			bodyOverride = nil
		}
		// Overall deadline so a 50x batch cannot run for 12 minutes while the
		// caller watches; per-send timeouts still apply inside.
		batchCtx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
		defer cancel()
		results := make([]map[string]any, 0, times)
		for i := 0; i < times; i++ {
			if i > 0 && in.DelayMs > 0 {
				select {
				case <-batchCtx.Done():
					writeJSON(w, map[string]any{"results": results, "cancelled": true})
					return
				case <-time.After(time.Duration(in.DelayMs) * time.Millisecond):
				}
			}
			select {
			case <-batchCtx.Done():
				writeJSON(w, map[string]any{"results": results, "cancelled": true})
				return
			default:
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
	if strings.HasSuffix(rest, "/body") && r.Method == "GET" {
		rid := strings.TrimSuffix(rest, "/body")
		var body []byte
		var ctype string
		if err := s.DB.QueryRow(`SELECT body, content_type FROM requests WHERE id=?`, rid).Scan(&body, &ctype); err != nil {
			http.Error(w, "not found", 404)
			return
		}
		// Raw bytes, byte-identical to what the provider sent (review #16).
		if ctype == "" {
			ctype = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ctype)
		_, _ = w.Write(body)
		return
	}
	if strings.HasSuffix(rest, "/replays") && r.Method == "GET" {
		rid := strings.TrimSuffix(rest, "/replays")
		rows, err := s.DB.Query(`SELECT target_url, status_code, latency_ms, error, created_at FROM replays WHERE request_id=? ORDER BY created_at DESC LIMIT 50`, rid)
		if err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var target, errMsg, at string
			var code int
			var lat int64
			if err := rows.Scan(&target, &code, &lat, &errMsg, &at); err != nil {
				http.Error(w, "storage unavailable", 500)
				return
			}
			out = append(out, map[string]any{"target_url": target, "status_code": code, "latency_ms": lat, "error": errMsg, "created_at": at})
		}
		if err := rows.Err(); err != nil {
			http.Error(w, "storage unavailable", 500)
			return
		}
		writeJSON(w, out)
		return
	}
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
	err := s.DB.QueryRow(`SELECT method, path, headers, content_type, verify_status, verify_error, fix_hint, strftime('%Y-%m-%dT%H:%M:%fZ', received_at), query, body FROM requests WHERE id=?`, id).
		Scan(&method, &path, &headers, &ctype, &vs, &verr, &hint, &at, &query, &body)
	if err != nil {
		http.Error(w, "not found", 404)
		return
	}
	writeJSON(w, map[string]any{
		"id": id, "method": method, "path": path, "headers": headers,
		"content_type": ctype, "verify_status": vs, "verify_error": verr,
		"fix_hint": hint, "received_at": at, "query": query, "body_text": string(body),
		"body_base64": base64.StdEncoding.EncodeToString(body),
	})
}
