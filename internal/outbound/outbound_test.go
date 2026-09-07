package outbound

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func httptestServer(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	s := httptest.NewServer(h)
	t.Cleanup(s.Close)
	return s.URL
}

func TestNoRedirectFollow(t *testing.T) {
	var hits int
	srv := httptestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/a" {
			w.Header().Set("Location", "/b")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	req, _ := http.NewRequest("POST", srv+"/a", nil)
	resp, err := Client(5 * time.Second).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound || hits != 1 {
		t.Fatalf("status=%d hits=%d, want 302x1", resp.StatusCode, hits)
	}
}

func TestStripHopByHop(t *testing.T) {
	h := http.Header{
		"Connection":        {"keep-alive, X-Custom-Hop"},
		"Keep-Alive":        {"timeout=5"},
		"X-Custom-Hop":      {"1"},
		"X-End-To-End":      {"keep"},
		"Transfer-Encoding": {"chunked"},
		"Stripe-Signature":  {"v"},
		"Content-Type":      {"application/json"},
	}
	StripHopByHop(h)
	for _, gone := range []string{"Connection", "Keep-Alive", "X-Custom-Hop", "Transfer-Encoding"} {
		if h.Get(gone) != "" {
			t.Fatalf("%s survived", gone)
		}
	}
	for _, want := range []string{"X-End-To-End", "Stripe-Signature", "Content-Type"} {
		if h.Get(want) == "" {
			t.Fatalf("%s stripped", want)
		}
	}
}

func TestBlockedMetadataAddressForms(t *testing.T) {
	for _, target := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[::ffff:169.254.169.254]/latest/meta-data/",
		"http://[::ffff:a9fe:a9fe]/latest/meta-data/",
		"http://metadata.google.internal/",
	} {
		if !Blocked(target) {
			t.Fatalf("expected blocked: %s", target)
		}
	}
	for _, target := range []string{
		"http://localhost:3000/hook/test",
		"http://127.0.0.1:3000/hook/test",
		"http://10.0.0.5:3000/hook/test",
		"https://example.test/hook/test",
	} {
		if Blocked(target) {
			t.Fatalf("unexpectedly blocked allowed target: %s", target)
		}
	}
}

func TestBlockedRejectsUnsupportedTargets(t *testing.T) {
	for _, target := range []string{"file:///etc/passwd", "//localhost/path", "not a url"} {
		if !Blocked(target) {
			t.Fatalf("expected rejected target: %s", target)
		}
	}
}
