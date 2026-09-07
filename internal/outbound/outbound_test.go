package outbound

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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

// Dial-time policy: mapped metadata IPs, Alibaba metadata, and DNS aliases
// resolving to metadata are refused; rebinding between connections is
// re-validated; localhost keeps working through the custom transport.
func TestDialPolicy(t *testing.T) {
	old := lookupIP
	defer func() { lookupIP = old }()
	md := net.ParseIP("169.254.169.254")
	lo := net.ParseIP("127.0.0.1")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Literal metadata IPs never dial (incl. mapped IPv6 + Alibaba range).
	for _, addr := range []string{"[::ffff:169.254.169.254]:80", "169.254.169.254:80", "100.100.100.200:80"} {
		if _, err := dialContext(ctx, "tcp", addr); err == nil {
			t.Fatalf("metadata dial allowed: %s", addr)
		}
	}
	// DNS alias to metadata refused without network access.
	lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: md}}, nil
	}
	if _, err := dialContext(ctx, "tcp", "evil.example:80"); err == nil {
		t.Fatal("metadata DNS alias dial allowed")
	}
	// End-to-end through the policy transport: fake hostname answers
	// localhost first (works), then rebinds to metadata (refused).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: lo}}, nil
	}
	client := Client(5 * time.Second)
	resp, err := client.Get("http://app.test:" + port + "/")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("localhost via policy transport: %v %v", resp, err)
	}
	resp.Body.Close()
	lookupIP = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: md}}, nil
	}
	client2 := Client(5 * time.Second)
	client2.Transport.(*http.Transport).DisableKeepAlives = true
	if _, err := client2.Get("http://app.test:" + port + "/"); err == nil {
		t.Fatal("rebound metadata dial allowed")
	}
}
