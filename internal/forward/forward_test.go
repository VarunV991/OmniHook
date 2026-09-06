package forward

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/you/omnihook/internal/db"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	return sqldb
}

func seedRequest(t *testing.T, sqldb *sql.DB, slug string) string {
	t.Helper()
	_, err := sqldb.Exec(`INSERT OR IGNORE INTO endpoints(slug) VALUES(?)`, slug)
	if err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	id := uuid.NewString()
	_, err = sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, path, headers, content_type, body, body_size)
		VALUES(?,?,?,?,?,?,?,?)`, id, slug, "POST", "/order/1",
		`{"Content-Type":["application/json"],"X-Sig":["abc"]}`, "application/json",
		[]byte(`{"a":1}`), 7)
	if err != nil {
		t.Fatalf("seed request: %v", err)
	}
	return id
}

func replayCount(t *testing.T, sqldb *sql.DB, reqID string) (int, int, string) {
	t.Helper()
	var n, code int
	var errMsg string
	err := sqldb.QueryRow(`SELECT COUNT(*), COALESCE(MAX(status_code),0), COALESCE(MAX(error),'') FROM replays WHERE request_id=?`, reqID).
		Scan(&n, &code, &errMsg)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	return n, code, errMsg
}

func TestDeliverSuccessForwardsBytesAndHeaders(t *testing.T) {
	sqldb := openTestDB(t)
	id := seedRequest(t, sqldb, "s1")
	var gotMethod, gotBody, gotSig, gotFwd string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		buf := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(buf)
		gotBody = string(buf)
		gotSig = r.Header.Get("X-Sig")
		gotFwd = r.Header.Get("X-Omnihook-Forward")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer target.Close()
	code, _, errMsg := Deliver(sqldb, id, target.URL)
	if errMsg != "" || code != http.StatusAccepted {
		t.Fatalf("deliver = %d, %q", code, errMsg)
	}
	if gotMethod != "POST" || gotBody != `{"a":1}` || gotSig != "abc" || gotFwd != "true" {
		t.Fatalf("forwarded wrong: %q %q %q %q", gotMethod, gotBody, gotSig, gotFwd)
	}
	if n, c, _ := replayCount(t, sqldb, id); n != 1 || c != 202 {
		t.Fatalf("replays = %d x %d", n, c)
	}
}

func TestDeliverFailureIsRecordedNotReturned(t *testing.T) {
	sqldb := openTestDB(t)
	id := seedRequest(t, sqldb, "s2")
	// Unroutable target: must record error, never panic.
	code, _, errMsg := Deliver(sqldb, id, "http://127.0.0.1:1/hook")
	if code != 0 || errMsg == "" {
		t.Fatalf("expected recorded transport error, got %d %q", code, errMsg)
	}
	if n, _, e := replayCount(t, sqldb, id); n != 1 || e == "" {
		t.Fatalf("replays = %d err %q", n, e)
	}
}

func TestDeliverBlockedMetadataHost(t *testing.T) {
	sqldb := openTestDB(t)
	id := seedRequest(t, sqldb, "s3")
	code, _, errMsg := Deliver(sqldb, id, "http://169.254.169.254/latest/meta-data/")
	if code != 0 || errMsg == "" {
		t.Fatalf("expected SSRF block, got %d %q", code, errMsg)
	}
	if n, _, _ := replayCount(t, sqldb, id); n != 1 {
		t.Fatalf("expected 1 recorded block, got %d", n)
	}
}
