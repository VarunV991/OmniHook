package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	t.Helper()
	cfg := config.Config{
		Port:         "0",
		DataDir:      t.TempDir(),
		DBPath:       filepath.Join(t.TempDir(), "test.db"),
		RetentionHrs: 168,
		MaxBodyBytes: 1 << 20,
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
	var list []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list = %q, err = %v", rec.Body.String(), err)
	}
	id := list[0]["id"]
	if list[0]["verify_status"] != "SKIPPED" {
		t.Fatalf("verify_status = %q", list[0]["verify_status"])
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
	var list []map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list) != 1 {
		t.Fatalf("list = %q, err = %v", rec.Body.String(), err)
	}
}
