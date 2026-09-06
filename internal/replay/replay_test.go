package replay

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
