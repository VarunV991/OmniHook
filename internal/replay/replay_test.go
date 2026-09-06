package replay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/you/omnihook/internal/db"
	"github.com/you/omnihook/internal/verify"
)

func mustDB(t *testing.T) *sql.DB {
	t.Helper()
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	t.Cleanup(func() { _ = sqldb.Close() })
	return sqldb
}

func verifyChain(secret string, headers map[string]string, body []byte) string {
	res := verify.Chain(secret, "stripe", headers, body, time.Now())
	return res.Status
}

var _ = fmt.Sprintf // keep fmt referenced if helpers evolve

func TestBlockedHosts(t *testing.T) {
	for _, u := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/",
		"http://metadata.google/",
	} {
		if !blocked(u) {
			t.Fatalf("expected blocked: %s", u)
		}
	}
	if blocked("http://localhost:3000/webhooks/stripe") {
		t.Fatal("localhost must be allowed by design")
	}
}

func TestResignMakesStaleStripePass(t *testing.T) {
	sqldb := mustDB(t)
	secret := "whsec_resign_e2e"
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug,provider,secret_ref) VALUES('rs1','stripe',?)`, secret); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"id":"evt_old"}`)
	hj, _ := json.Marshal(map[string][]string{"Stripe-Signature": {"t=100,v1=deadbeef"}})
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, path, headers, content_type, body, body_size) VALUES('r-old','rs1','POST','/',?,?,?,7)`,
		string(hj), "application/json", raw); err != nil {
		t.Fatal(err)
	}
	var gotSig string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("Stripe-Signature")
		w.WriteHeader(200)
	}))
	defer target.Close()
	code, _, _ := SendWithOptions(sqldb, "r-old", target.URL, Options{Resign: true})
	if code != 200 {
		t.Fatalf("code=%d", code)
	}
	if gotSig == "" || gotSig == "t=100,v1=deadbeef" {
		t.Fatalf("not resigned: %q", gotSig)
	}
	if st := verifyChain(secret, map[string]string{"Stripe-Signature": gotSig}, raw); st != "PASS" {
		t.Fatalf("resigned did not verify: %q", st)
	}
}

func TestBlockedReplayIsRecorded(t *testing.T) {
	sqldb := mustDB(t)
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug) VALUES('b1')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method) VALUES('b-req','b1','POST')`); err != nil {
		t.Fatal(err)
	}
	code, _, msg := Send(sqldb, "b-req", "http://169.254.169.254/x", nil, nil)
	if code != 0 || msg == "" {
		t.Fatalf("code=%d msg=%q", code, msg)
	}
	var n int
	_ = sqldb.QueryRow(`SELECT COUNT(*) FROM replays WHERE request_id='b-req'`).Scan(&n)
	if n != 1 {
		t.Fatalf("blocked attempt not recorded (n=%d)", n)
	}
}

func TestReplayRestoresOriginalHeadersOnce(t *testing.T) {
	sqldb := mustDB(t)
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug) VALUES('h1')`); err != nil {
		t.Fatal(err)
	}
	hj, _ := json.Marshal(map[string][]string{
		"Content-Type": {"application/json"},
		"X-Custom":     {"keep-me"},
	})
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, headers, content_type, body, body_size)
		VALUES('h-req','h1','POST',?,?,?,7)`, string(hj), "application/json", []byte(`{"a":1}`)); err != nil {
		t.Fatal(err)
	}
	var custom string
	var ctValues []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		custom = r.Header.Get("X-Custom")
		ctValues = r.Header["Content-Type"]
		w.WriteHeader(200)
	}))
	defer target.Close()
	if code, _, _ := Send(sqldb, "h-req", target.URL, nil, nil); code != 200 {
		t.Fatalf("code=%d", code)
	}
	if custom != "keep-me" {
		t.Fatalf("original header lost: %q", custom)
	}
	if len(ctValues) != 1 || ctValues[0] != "application/json" {
		t.Fatalf("content-type duplicated/lost: %q", ctValues)
	}
}

// #14: failed re-sign is an explicit recorded error, never silent stale send.
func TestResignFailureRecorded(t *testing.T) {
	sqldb := mustDB(t)
	// Generic endpoint: nothing concrete to re-sign with.
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug,provider,secret_ref) VALUES('g1','generic','s')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method) VALUES('g-req','g1','POST')`); err != nil {
		t.Fatal(err)
	}
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("target must not be hit when re-sign fails")
	}))
	defer target.Close()
	code, _, msg := SendWithOptions(sqldb, "g-req", target.URL, Options{Resign: true})
	if code != 0 || !strings.Contains(msg, "resign") {
		t.Fatalf("code=%d msg=%q", code, msg)
	}
	var n int
	_ = sqldb.QueryRow(`SELECT COUNT(*) FROM replays WHERE request_id='g-req' AND error LIKE 'resign%'`).Scan(&n)
	if n != 1 {
		t.Fatalf("resign failure not recorded (n=%d)", n)
	}
}

// Effective provider wins: autodetected Stripe on a generic endpoint re-signs.
func TestResignUsesDetectedProvider(t *testing.T) {
	sqldb := mustDB(t)
	secret := "whsec_detected"
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug,provider,secret_ref) VALUES('d1','generic',?)`, secret); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"id":"evt_old"}`)
	hj, _ := json.Marshal(map[string][]string{"Stripe-Signature": {"t=100,v1=deadbeef"}})
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, headers, body, verified_by) VALUES('d-req','d1','POST',?,?,'stripe')`,
		string(hj), raw); err != nil {
		t.Fatal(err)
	}
	var gotSig string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("Stripe-Signature")
		w.WriteHeader(200)
	}))
	defer target.Close()
	if code, _, _ := SendWithOptions(sqldb, "d-req", target.URL, Options{Resign: true}); code != 200 {
		t.Fatalf("code=%d", code)
	}
	if st := verifyChain(secret, map[string]string{"Stripe-Signature": gotSig}, raw); st != "PASS" {
		t.Fatalf("detected-provider resign did not verify: sig=%q", gotSig)
	}
}

// #32: explicit overrides win regardless of case — a lowercase
// stripe-signature must replace the resigned canonical one, exactly once.
func TestExplicitOverrideWinsAnyCase(t *testing.T) {
	sqldb := mustDB(t)
	secret := "whsec_case"
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug,provider,secret_ref) VALUES('c1','stripe',?)`, secret); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"id":"evt_c"}`)
	hj, _ := json.Marshal(map[string][]string{"Stripe-Signature": {"t=100,v1=old"}})
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, headers, body) VALUES('c-req','c1','POST',?,?)`,
		string(hj), raw); err != nil {
		t.Fatal(err)
	}
	var sigs []string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sigs = r.Header["Stripe-Signature"]
		w.WriteHeader(200)
	}))
	defer target.Close()
	opts := Options{Resign: true, Headers: map[string]string{"stripe-signature": "t=1,v1=explicit"}}
	if code, _, _ := SendWithOptions(sqldb, "c-req", target.URL, opts); code != 200 {
		t.Fatalf("code=%d", code)
	}
	if len(sigs) != 1 || sigs[0] != "t=1,v1=explicit" {
		t.Fatalf("headers sent: %q", sigs)
	}
}
