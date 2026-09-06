package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/you/omnihook/internal/capture"
	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/db"
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
