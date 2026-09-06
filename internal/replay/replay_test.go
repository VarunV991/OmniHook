package replay

import "testing"

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
