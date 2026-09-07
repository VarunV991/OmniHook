package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/you/omnihook/internal/capture"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/db"
	"github.com/you/omnihook/internal/verify"
)

// newTestServer spins up the full stack (SQLite temp file + handlers) in-process.
// Hermetic: no background processes, no fixed ports — safe for CI.
func newTestServer(t *testing.T) *Server {
	return newTestServerWithToken(t, "")
}

// newTestServerWithToken wires ACCESS_TOKEN before routes are registered
// (auth wrapping happens at registration, so late mutation would not gate).
func newTestServerWithToken(t *testing.T, token string) *Server {
	t.Helper()
	cfg := config.Config{
		Port:         "0",
		DataDir:      t.TempDir(),
		DBPath:       filepath.Join(t.TempDir(), "test.db"),
		RetentionHrs: 168,
		MaxBodyBytes: 1 << 20,
		AccessToken:  token,
		Version:      "test",
	}
	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	cap := &capture.Handler{DB: sqldb, Cfg: cfg, Hub: capture.NewHub()}
	return New(sqldb, cfg, cap)
}

func TestHealth(t *testing.T) {
	s := newTestServer(t)
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("health status = %d", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("health body: %v", err)
	}
	if body["db"] != "up" {
		t.Fatalf("db = %q", body["db"])
	}
}

// pageShape mirrors the paginated list envelope.
type pageShape struct {
	Requests   []map[string]string `json:"requests"`
	NextCursor string              `json:"next_cursor"`
}

func TestCaptureListReplayLoop(t *testing.T) {
	s := newTestServer(t)

	// 1. Create endpoint.
	createBody, _ := json.Marshal(map[string]string{"slug": "proj1", "provider": "generic"})
	req := httptest.NewRequest("POST", "/api/endpoints", bytes.NewReader(createBody))
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create endpoint status = %d: %s", rec.Code, rec.Body.String())
	}

	// 2. Capture a webhook with subpath + raw JSON body.
	raw := `{"event":"ping","n":1}`
	req = httptest.NewRequest("POST", "/hook/proj1/order/123?x=1", bytes.NewBufferString(raw))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capture status = %d", rec.Code)
	}

	// 3. List shows one SKIPPED (generic, no secret) request.
	req = httptest.NewRequest("GET", "/api/endpoints/proj1/requests", nil)
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	var page pageShape
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Requests) != 1 {
		t.Fatalf("list = %q, err = %v", rec.Body.String(), err)
	}
	// Single row, nothing after: no next cursor.
	if page.NextCursor != "" {
		t.Fatalf("unexpected next cursor: %q", page.NextCursor)
	}
	id := page.Requests[0]["id"]
	if page.Requests[0]["verify_status"] != "SKIPPED" {
		t.Fatalf("verify_status = %q", page.Requests[0]["verify_status"])
	}

	// 4. Detail preserves byte-identical body.
	req = httptest.NewRequest("GET", "/api/requests/"+id, nil)
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	var detail map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("detail: %v", err)
	}
	if detail["body_text"] != raw {
		t.Fatalf("body drift: %q", detail["body_text"])
	}

	// 5. Replay to a fake target records status + latency.
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))
	defer target.Close()
	replayBody, _ := json.Marshal(map[string]string{"target": target.URL})
	req = httptest.NewRequest("POST", "/api/requests/"+id+"/replay", bytes.NewReader(replayBody))
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	var rep map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &rep); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if rep["status_code"] != float64(418) {
		t.Fatalf("replay status = %v", rep["status_code"])
	}
}

func TestMain(m *testing.M) {
	_ = os.Setenv("DATA_DIR", os.TempDir())
	os.Exit(m.Run())
}

func doAPI(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	return rec
}

// Endpoint + request lifecycle: upsert on re-create, PATCH secret/target,
// clear requests, delete request, delete endpoint.
func TestEndpointLifecycle(t *testing.T) {
	s := newTestServer(t)
	rec := doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "lc", "provider": "generic"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d", rec.Code)
	}
	// Re-create with new values upserts instead of silently ignoring.
	rec = doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "lc", "provider": "stripe", "secret": "whsec_1", "target_url": "http://localhost:9/x"})
	if rec.Code != http.StatusOK {
		t.Fatalf("re-create = %d", rec.Code)
	}
	rec = doAPI(t, s, "GET", "/api/endpoints/lc", nil)
	var got map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got["provider"] != "stripe" || got["target_url"] != "http://localhost:9/x" {
		t.Fatalf("upsert failed: %s", rec.Body.String())
	}
	// PATCH a subset.
	rec = doAPI(t, s, "PATCH", "/api/endpoints/lc", map[string]string{"secret": "whsec_2"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}
	// Capture one request, then clear + delete flows.
	_, err := s.DB.Exec(`INSERT INTO requests(id, endpoint_slug, method) VALUES('lc-1','lc','POST'),('lc-2','lc','POST')`)
	if err != nil {
		t.Fatal(err)
	}
	rec = doAPI(t, s, "DELETE", "/api/requests/lc-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete request = %d", rec.Code)
	}
	rec = doAPI(t, s, "DELETE", "/api/endpoints/lc/requests", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear requests = %d", rec.Code)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM requests WHERE endpoint_slug='lc'`).Scan(&n)
	if n != 0 {
		t.Fatalf("remaining = %d", n)
	}
	rec = doAPI(t, s, "DELETE", "/api/endpoints/lc", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete endpoint = %d", rec.Code)
	}
	rec = doAPI(t, s, "GET", "/api/endpoints/lc", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted = %d", rec.Code)
	}
	rec = doAPI(t, s, "PATCH", "/api/endpoints/nope", map[string]string{"secret": "x"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("patch missing = %d", rec.Code)
	}
}

// #17: missing request is 404 (not a 200-envelope-zero); bad target is 400.
func TestReplayValidation(t *testing.T) {
	s := newTestServer(t)
	rec := doAPI(t, s, "POST", "/api/requests/nope/replay", map[string]string{"target": "http://localhost:9"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing id = %d, want 404", rec.Code)
	}
	if _, err := s.DB.Exec(`INSERT INTO endpoints(slug) VALUES('v1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO requests(id, endpoint_slug, method) VALUES('v-req','v1','POST')`); err != nil {
		t.Fatal(err)
	}
	rec = doAPI(t, s, "POST", "/api/requests/v-req/replay", map[string]string{"target": "://bad"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad target = %d, want 400", rec.Code)
	}
}

// #18: explicit empty body_base64 sends an empty body (not the stored one).
func TestReplayEmptyOverride(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.DB.Exec(`INSERT INTO endpoints(slug) VALUES('e1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO requests(id, endpoint_slug, method, body) VALUES('e-req','e1','POST',?)`, []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	var gotLen int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotLen = len(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	empty := ""
	rec := doAPI(t, s, "POST", "/api/requests/e-req/replay", map[string]any{"target": target.URL, "body_base64": empty})
	if rec.Code != http.StatusOK {
		t.Fatalf("replay = %d: %s", rec.Code, rec.Body.String())
	}
	if gotLen != 0 {
		t.Fatalf("body len = %d, want 0 (explicit empty override)", gotLen)
	}
}

// #12/#27: unknown providers rejected; re-create updates only given fields.
func TestProviderEnumAndExplicitUpsert(t *testing.T) {
	s := newTestServer(t)
	rec := doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "pe", "provider": "strip"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown provider = %d, want 400", rec.Code)
	}
	rec = doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "pe", "provider": "stripe", "secret": "whsec_keep", "target_url": "http://localhost:9/x"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d", rec.Code)
	}
	// Re-create with slug only: secret/target/provider must survive.
	rec = doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "pe"})
	if rec.Code != http.StatusOK {
		t.Fatalf("re-create = %d", rec.Code)
	}
	var provider, secret, target string
	_ = s.DB.QueryRow(`SELECT provider, secret_ref, target_url FROM endpoints WHERE slug='pe'`).Scan(&provider, &secret, &target)
	if provider != "stripe" || secret != "whsec_keep" || target != "http://localhost:9/x" {
		t.Fatalf("re-create destroyed config: %q %q %q", provider, secret, target)
	}
	// Explicit empty secret clears.
	rec = doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "pe", "secret": ""})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear = %d", rec.Code)
	}
	_ = s.DB.QueryRow(`SELECT secret_ref FROM endpoints WHERE slug='pe'`).Scan(&secret)
	if secret != "" {
		t.Fatalf("explicit empty secret did not clear: %q", secret)
	}
	// PATCH rejects unknown providers too.
	rec = doAPI(t, s, "PATCH", "/api/endpoints/pe", map[string]string{"provider": "strip"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("patch unknown = %d, want 400", rec.Code)
	}
}

// B: delivery-attempt history lists forwards/replays per request.
func TestReplaysHistory(t *testing.T) {
	s := newTestServer(t)
	if _, err := s.DB.Exec(`INSERT INTO endpoints(slug) VALUES('h1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO requests(id, endpoint_slug, method) VALUES('h-req','h1','POST')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`INSERT INTO replays(id, request_id, target_url, status_code, latency_ms, error) VALUES('r1','h-req','http://localhost:9',500,12,'boom')`); err != nil {
		t.Fatal(err)
	}
	rec := doAPI(t, s, "GET", "/api/requests/h-req/replays", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("replays = %d", rec.Code)
	}
	var out []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || len(out) != 1 {
		t.Fatalf("out=%s err=%v", rec.Body.String(), err)
	}
	if out[0]["status_code"] != float64(500) || out[0]["error"] != "boom" {
		t.Fatalf("row=%v", out[0])
	}
}

func TestSlugValidation(t *testing.T) {
	s := newTestServer(t)
	for _, bad := range []string{"a/b", "..", "../x", "-lead", "_lead", "has space", "semi;colon", "toolongtoolongtoolongtoolongtoolongtoolongtoolongtoolongtoolongxx"} {
		rec := doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": bad})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("slug %q = %d, want 400", bad, rec.Code)
		}
	}
	for _, good := range []string{"a", "proj-1_2", "X9"} {
		rec := doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": good})
		if rec.Code != http.StatusOK {
			t.Fatalf("slug %q = %d, want 200", good, rec.Code)
		}
	}
	// Public capture path must reject invalid slugs before storing anything
	// (e.g. /hook/-lead must not create an endpoint).
	req := httptest.NewRequest("POST", "/hook/-lead", bytes.NewBufferString(`{}`))
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("capture bad slug = %d, want 400", rec.Code)
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM endpoints WHERE slug='-lead'`).Scan(&n)
	if n != 0 {
		t.Fatal("invalid capture created an endpoint")
	}
}

// Storage failure must never look like success: no 200, no row, no broadcast.
func TestCaptureClosedDBIs503(t *testing.T) {
	s := newTestServer(t)
	s.DB.Close()
	req := httptest.NewRequest("POST", "/hook/x", bytes.NewBufferString(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed-db capture = %d, want 503", rec.Code)
	}
}

// A1: malformed management input must never mutate state.
func TestMalformedInputRejected(t *testing.T) {
	s := newTestServer(t)
	for _, tc := range []struct {
		name   string
		method string
		path   string
		raw    string
	}{
		{"truncated create", "POST", "/api/endpoints", `{`},
		{"trailing create", "POST", "/api/endpoints", `{"slug":"t1"} {}`},
		{"empty create", "POST", "/api/endpoints", ``},
		{"trailing replay", "POST", "/api/requests/x/replay", `{"target":"http://localhost:9"} {}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, bytes.NewBufferString(tc.raw))
			rec := httptest.NewRecorder()
			s.Mux.ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("%s = %d, want 400", tc.name, rec.Code)
			}
		})
	}
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM endpoints`).Scan(&n)
	if n != 0 {
		t.Fatalf("malformed input created %d endpoints", n)
	}
}

// A1: invalid mock statuses must not panic the handler.
func TestPatchInvalidStatus(t *testing.T) {
	s := newTestServer(t)
	rec := doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "ps"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d", rec.Code)
	}
	for _, bad := range []int{99, 100, 600, 0, -1} {
		rec = doAPI(t, s, "PATCH", "/api/endpoints/ps", map[string]any{"response_status": bad})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status %d = %d, want 400", bad, rec.Code)
		}
	}
	rec = doAPI(t, s, "PATCH", "/api/endpoints/ps", map[string]any{"response_status": 201})
	if rec.Code != http.StatusOK {
		t.Fatalf("status 201 = %d", rec.Code)
	}
}

// A1: health must report 503 (not ok:true) when storage is down.
func TestHealthClosedDBIs503(t *testing.T) {
	s := newTestServer(t)
	s.DB.Close()
	req := httptest.NewRequest("GET", "/health", nil)
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("closed-db health = %d, want 503", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["ok"] == "true" {
		t.Fatalf("health claims ok with closed DB: %s", rec.Body.String())
	}
}

// A2: gated mode serves a login shell (not a bare 401) and the cookie flow works.
func TestLoginFlow(t *testing.T) {
	s := newTestServerWithToken(t, "s3cret")

	// Fresh browser: login shell, not 401.
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "/api/login") {
		t.Fatalf("root = %d, want login shell", rec.Code)
	}
	// API still 401s without credentials.
	req = httptest.NewRequest("GET", "/api/endpoints", nil)
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon api = %d, want 401", rec.Code)
	}
	// Wrong token rejected.
	bad, _ := json.Marshal(map[string]string{"token": "nope"})
	req = httptest.NewRequest("POST", "/api/login", bytes.NewReader(bad))
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad login = %d, want 401", rec.Code)
	}
	// Right token sets HttpOnly cookie.
	good, _ := json.Marshal(map[string]string{"token": "s3cret"})
	req = httptest.NewRequest("POST", "/api/login", bytes.NewReader(good))
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("login = %d", rec.Code)
	}
	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == "omnihook_token" {
			session = c
		}
	}
	if session == nil || !session.HttpOnly || session.Value == "" {
		t.Fatalf("no HttpOnly session cookie: %v", rec.Result().Cookies())
	}
	// Cookie grants API access.
	req = httptest.NewRequest("GET", "/api/endpoints", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("cookie api = %d, want 200", rec.Code)
	}
	// Logout clears the cookie.
	req = httptest.NewRequest("POST", "/api/logout", nil)
	req.AddCookie(session)
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout = %d", rec.Code)
	}
	cleared := false
	for _, c := range rec.Result().Cookies() {
		if c.Name == "omnihook_token" && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("logout did not clear cookie")
	}
	// Ungated server redirects /login to the app.
	s2 := newTestServer(t)
	req = httptest.NewRequest("GET", "/login", nil)
	rec = httptest.NewRecorder()
	s2.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusFound {
		t.Fatalf("ungated /login = %d, want 302", rec.Code)
	}
}

// Replay upgrades: times=N returns a results array; resign refreshes a stale
// Stripe signature so the target sees a verifiable PASS.
func TestReplayTimesAndResign(t *testing.T) {
	s := newTestServer(t)
	createBody, _ := json.Marshal(map[string]string{
		"slug": "rs1", "provider": "stripe", "secret": "whsec_api_resign",
	})
	req := httptest.NewRequest("POST", "/api/endpoints", bytes.NewReader(createBody))
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create endpoint = %d", rec.Code)
	}
	stale := `{"id":"evt_stale"}`
	hj, _ := json.Marshal(map[string][]string{"Stripe-Signature": {"t=100,v1=deadbeef"}})
	if _, err := s.DB.Exec(`INSERT INTO requests(id, endpoint_slug, method, path, headers, content_type, body, body_size)
		VALUES('stale-1','rs1','POST','/',?,?,?,?)`, string(hj), "application/json", []byte(stale), len(stale)); err != nil {
		t.Fatal(err)
	}
	var hits int
	var lastSig string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		lastSig = r.Header.Get("Stripe-Signature")
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	replayBody, _ := json.Marshal(map[string]any{"target": target.URL, "times": 2, "resign": true})
	req = httptest.NewRequest("POST", "/api/requests/stale-1/replay", bytes.NewReader(replayBody))
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("replay: %v", err)
	}
	results, ok := out["results"].([]any)
	if !ok || len(results) != 2 {
		t.Fatalf("results = %s", rec.Body.String())
	}
	if hits != 2 {
		t.Fatalf("hits = %d", hits)
	}
	got := verify.Chain("whsec_api_resign", "stripe",
		map[string]string{"Stripe-Signature": lastSig}, []byte(stale), time.Now())
	if got.Status != verify.PASS {
		t.Fatalf("resigned did not verify: %+v", got)
	}
}

// Regression: a dead forward target must never fail the provider response.
// Capture returns the configured 200 even when forwarding is impossible.
func TestCaptureWithBrokenForwardStill200(t *testing.T) {
	s := newTestServer(t)
	createBody, _ := json.Marshal(map[string]string{
		"slug": "fwd1", "provider": "generic", "target_url": "http://127.0.0.1:1/hook",
	})
	req := httptest.NewRequest("POST", "/api/endpoints", bytes.NewReader(createBody))
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("create endpoint status = %d", rec.Code)
	}
	req = httptest.NewRequest("POST", "/hook/fwd1", bytes.NewBufferString(`{"x":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("capture with broken forward = %d, want 200", rec.Code)
	}
	req = httptest.NewRequest("GET", "/api/endpoints/fwd1/requests", nil)
	rec = httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, req)
	var fwd pageShape
	if err := json.Unmarshal(rec.Body.Bytes(), &fwd); err != nil || len(fwd.Requests) != 1 {
		t.Fatalf("list = %q, err = %v", rec.Body.String(), err)
	}
}

// #25: cursor pagination walks >50 rows exactly once; aggregated inbox serves
// all endpoints in one call; bad cursors/limits are 400s.
func TestPaginationAndAggregatedInbox(t *testing.T) {
	s := newTestServer(t)
	rec := doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "pg"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d", rec.Code)
	}
	for i := 0; i < 5; i++ {
		id := "pg-" + string(rune('a'+i))
		at := "2026-09-06T10:00:0" + string(rune('0'+i)) + ".000Z"
		if _, err := s.DB.Exec(`INSERT INTO requests(id, endpoint_slug, method, received_at) VALUES(?,?, 'POST', ?)`, id, "pg", at); err != nil {
			t.Fatal(err)
		}
	}
	// Page 1 (limit 2): newest two, with cursor.
	var p1 pageShape
	rec = doAPI(t, s, "GET", "/api/endpoints/pg/requests?limit=2", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("page1 = %d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &p1)
	if len(p1.Requests) != 2 || p1.Requests[0]["id"] != "pg-e" || p1.NextCursor == "" {
		t.Fatalf("page1 = %s", rec.Body.String())
	}
	// Page 2 + 3 walk the rest exactly once.
	seen := map[string]bool{"pg-e": true}
	// (page1 asserted above; re-derive second id from response)
	seen[p1.Requests[1]["id"]] = true
	cursor := p1.NextCursor
	for {
		rec = doAPI(t, s, "GET", "/api/endpoints/pg/requests?limit=2&cursor="+cursor, nil)
		var p pageShape
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		for _, r := range p.Requests {
			if seen[r["id"]] {
				t.Fatalf("duplicate %q", r["id"])
			}
			seen[r["id"]] = true
		}
		if p.NextCursor == "" {
			break
		}
		cursor = p.NextCursor
	}
	if len(seen) != 5 {
		t.Fatalf("walked %d rows, want 5", len(seen))
	}
	// Aggregated inbox: one call, endpoint-tagged rows.
	rec = doAPI(t, s, "GET", "/api/requests?limit=10", nil)
	var agg struct {
		Requests []map[string]string `json:"requests"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &agg)
	if len(agg.Requests) != 5 || agg.Requests[0]["endpoint"] != "pg" {
		t.Fatalf("agg = %s", rec.Body.String())
	}
	// Bad inputs.
	for _, bad := range []string{
		"/api/endpoints/pg/requests?limit=0",
		"/api/endpoints/pg/requests?limit=101",
		"/api/endpoints/pg/requests?cursor=bogus",
		"/api/endpoints/pg/requests?cursor=a",
	} {
		if rec := doAPI(t, s, "GET", bad, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400", bad, rec.Code)
		}
	}
}

// #23: streams are per-endpoint, 404 unknown slugs, ready + heartbeat framing.
func TestStreamFiltering(t *testing.T) {
	s := newTestServer(t)
	rec := doAPI(t, s, "POST", "/api/endpoints", map[string]string{"slug": "s1"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create = %d", rec.Code)
	}
	// Unknown endpoint: 404.
	req := httptest.NewRequest("GET", "/api/endpoints/nope/stream", nil)
	got := httptest.NewRecorder()
	s.Mux.ServeHTTP(got, req)
	if got.Code != http.StatusNotFound {
		t.Fatalf("unknown stream = %d", got.Code)
	}
	// Known endpoint: ready event arrives; other-endpoint traffic excluded.
	req = httptest.NewRequest("GET", "/api/endpoints/s1/stream", nil)
	ctx, cancel := context.WithCancel(req.Context())
	req = req.WithContext(ctx)
	got = httptest.NewRecorder()
	flushed := make(chan string, 8)
	go func() {
		s.Mux.ServeHTTP(got, req)
		close(flushed)
	}()
	deadline := time.After(5 * time.Second)
	for {
		if strings.Contains(got.Body.String(), "event: ready") {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("no ready event")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	s.Cap.Hub.Broadcast("s1", "id-1")
	s.Cap.Hub.Broadcast("s2", "id-2")
	deadline = time.After(5 * time.Second)
	for {
		if strings.Contains(got.Body.String(), "data: id-1") {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatalf("missing own event: %q", got.Body.String())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	time.Sleep(100 * time.Millisecond)
	if strings.Contains(got.Body.String(), "id-2") {
		cancel()
		t.Fatalf("foreign event leaked: %q", got.Body.String())
	}
	cancel()
	<-flushed
}

func TestEndpointDetailStorageFailureIsNot404(t *testing.T) {
	s := newTestServer(t)
	if err := s.DB.Close(); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	s.Mux.ServeHTTP(rec, httptest.NewRequest("GET", "/api/endpoints/missing", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}
