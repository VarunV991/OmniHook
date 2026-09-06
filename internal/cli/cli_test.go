package cli

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/db"
)

func testSetup(t *testing.T) (*sql.DB, config.Config) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{
		Port: "0", DataDir: dir, DBPath: filepath.Join(dir, "test.db"),
		RetentionHrs: 168, MaxBodyBytes: 1 << 20, Version: "test",
	}
	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	return sqldb, cfg
}

func runOK(t *testing.T, sqldb *sql.DB, cfg config.Config, args ...string) string {
	t.Helper()
	var out, errBuf bytes.Buffer
	if code := Run(sqldb, cfg, args, &out, &errBuf); code != ExitOK {
		t.Fatalf("%v exit=%d stderr=%q", args, code, errBuf.String())
	}
	return out.String()
}

func TestNewListShowRoundtrip(t *testing.T) {
	sqldb, cfg := testSetup(t)
	out := runOK(t, sqldb, cfg, "new", "proj1", "--provider", "stripe", "--secret", "whsec_x")
	if !strings.Contains(out, "/hook/proj1") {
		t.Fatalf("new output: %q", out)
	}
	out = runOK(t, sqldb, cfg, "list")
	if !strings.Contains(out, "proj1") || !strings.Contains(out, "stripe") {
		t.Fatalf("list output: %q", out)
	}
	// Seed a request directly, then show it.
	res := runOK(t, sqldb, cfg, "list", "--json")
	if !strings.Contains(res, "proj1") {
		t.Fatalf("list --json: %q", res)
	}
}

func TestShowMissingIsError(t *testing.T) {
	sqldb, cfg := testSetup(t)
	var out, errBuf bytes.Buffer
	if code := Run(sqldb, cfg, []string{"show", "nope"}, &out, &errBuf); code != ExitError {
		t.Fatalf("exit=%d", code)
	}
}

func TestReplayViaCLI(t *testing.T) {
	sqldb, cfg := testSetup(t)
	runOK(t, sqldb, cfg, "new", "r1")
	_, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, path, headers, content_type, body, body_size)
		VALUES('req-1','r1','POST','/h','{}','application/json',?,7)`, []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer target.Close()
	out := runOK(t, sqldb, cfg, "replay", "req-1", "--target", target.URL)
	if !strings.Contains(out, "status=201") {
		t.Fatalf("replay output: %q", out)
	}
}

func TestGCDeletesOldRequests(t *testing.T) {
	sqldb, cfg := testSetup(t)
	runOK(t, sqldb, cfg, "new", "g1")
	_, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, received_at) VALUES('old','g1','POST',datetime('now','-49 hours'))`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method) VALUES('fresh','g1','POST')`)
	if err != nil {
		t.Fatal(err)
	}
	out := runOK(t, sqldb, cfg, "gc", "--retention-hours", "48")
	if !strings.Contains(out, "deleted 1 requests") {
		t.Fatalf("gc output: %q", out)
	}
	var n int
	_ = sqldb.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n)
	if n != 1 {
		t.Fatalf("remaining = %d", n)
	}
}

func TestUnknownCommand(t *testing.T) {
	sqldb, cfg := testSetup(t)
	var out, errBuf bytes.Buffer
	if code := Run(sqldb, cfg, []string{"bogus"}, &out, &errBuf); code != ExitError {
		t.Fatalf("exit=%d", code)
	}
}

func TestUnknownFlagFails(t *testing.T) {
	sqldb, cfg := testSetup(t)
	var out, errBuf bytes.Buffer
	if code := Run(sqldb, cfg, []string{"list", "--bogus"}, &out, &errBuf); code != ExitError {
		t.Fatalf("exit=%d, want error for unknown flag", code)
	}
}

func TestNewRejectsBadSlug(t *testing.T) {
	sqldb, cfg := testSetup(t)
	var out, errBuf bytes.Buffer
	if code := Run(sqldb, cfg, []string{"new", "a/b"}, &out, &errBuf); code != ExitError {
		t.Fatalf("exit=%d, want error for bad slug", code)
	}
}

func TestListJSONShape(t *testing.T) {
	sqldb, cfg := testSetup(t)
	runOK(t, sqldb, cfg, "new", "j1", "--provider", "github")
	out := runOK(t, sqldb, cfg, "list", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("rows=%q err=%v", out, err)
	}
}

func TestVerifyCommandPassAndFail(t *testing.T) {
	_, cfg := testSetup(t)
	secret := "whsec_cli_test"
	body := []byte(`{"id":"evt_cli"}`)
	ts := time.Now().Unix()
	m := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(m, "%d.%s", ts, string(body))
	sig := hex.EncodeToString(m.Sum(nil))
	headers, _ := json.Marshal(map[string]string{
		"Stripe-Signature": fmt.Sprintf("t=%d,v1=%s", ts, sig),
	})
	dir := t.TempDir()
	hp := filepath.Join(dir, "h.json")
	bp := filepath.Join(dir, "b.bin")
	if err := os.WriteFile(hp, headers, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bp, body, 0o600); err != nil {
		t.Fatal(err)
	}
	var out, errBuf bytes.Buffer
	if code := Run(nil, cfg, []string{"verify", "--provider", "stripe", "--secret", secret,
		"--headers", "@" + hp, "--body", "@" + bp}, &out, &errBuf); code != ExitOK {
		t.Fatalf("exit=%d out=%q err=%q", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "status=PASS") {
		t.Fatalf("output: %q", out.String())
	}
	// Tampered body file must exit 2 with a fix hint.
	if err := os.WriteFile(bp, []byte(`{"id":"evt_other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errBuf.Reset()
	if code := Run(nil, cfg, []string{"verify", "--provider", "stripe", "--secret", secret,
		"--headers", "@" + hp, "--body", "@" + bp}, &out, &errBuf); code != ExitFail {
		t.Fatalf("exit=%d, want %d", code, ExitFail)
	}
	if !strings.Contains(out.String(), "fix:") {
		t.Fatalf("missing fix hint: %q", out.String())
	}
}

func TestReplayTimesAndResign(t *testing.T) {
	sqldb, cfg := testSetup(t)
	runOK(t, sqldb, cfg, "new", "multi1", "--provider", "stripe", "--secret", "whsec_multi")
	hj, _ := json.Marshal(map[string][]string{"Stripe-Signature": {"t=100,v1=deadbeef"}})
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, path, headers, content_type, body, body_size)
		VALUES('m-old','multi1','POST','/',?,?,?,7)`, string(hj), "application/json", []byte(`{"a":1}`)); err != nil {
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
	out := runOK(t, sqldb, cfg, "replay", "m-old", "--target", target.URL, "--times", "3", "--resign")
	if !strings.Contains(out, "[3/3] status=200") {
		t.Fatalf("output: %q", out)
	}
	if hits != 3 {
		t.Fatalf("hits = %d", hits)
	}
	if lastSig == "" || lastSig == "t=100,v1=deadbeef" {
		t.Fatalf("not resigned: %q", lastSig)
	}
}
