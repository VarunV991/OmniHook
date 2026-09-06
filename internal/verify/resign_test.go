package verify

import (
	"testing"
	"time"
)

func TestRefreshSignaturesRoundTrip(t *testing.T) {
	now := time.Now()
	old := now.Add(-1 * time.Hour) // stale: would FAIL tolerance as-is
	cases := []struct {
		provider string
		secret   string
		headers  map[string]string
		body     []byte
		pinned   string
	}{
		{"stripe", "whsec_r1", map[string]string{}, []byte(`{"a":1}`), "stripe"},
		{"github", "gh_r1", map[string]string{}, []byte(`{"a":1}`), "github"},
		{"standard", "whsec_" + b64([]byte("resign-key-12345678901234567890")), map[string]string{"Webhook-Id": "msg_r1"}, []byte(`{"a":1}`), "standard"},
		{"standard-svix", "whsec_" + b64([]byte("resign-key-12345678901234567890")), map[string]string{"Svix-Id": "msg_r2"}, []byte(`{"a":1}`), "standard"},
		{"razorpay", "rz_r1", map[string]string{}, []byte(`{"a":1}`), "razorpay"},
		{"shopify", "sh_r1", map[string]string{}, []byte(`{"a":1}`), "shopify"},
	}
	_ = old
	for _, tc := range cases {
		t.Run(tc.provider, func(t *testing.T) {
			provider := tc.provider
			if provider == "standard-svix" {
				provider = "standard"
			}
			fresh, err := RefreshSignatures(provider, tc.secret, tc.headers, tc.body, now)
			if err != nil {
				t.Fatalf("resign: %v", err)
			}
			if len(fresh) == 0 {
				t.Fatal("no refreshed headers")
			}
			merged := map[string]string{}
			for k, v := range tc.headers {
				merged[k] = v
			}
			for k, v := range fresh {
				merged[k] = v
			}
			got := Chain(tc.secret, tc.pinned, merged, tc.body, now)
			if got.Status != PASS {
				t.Fatalf("status=%q err=%q", got.Status, got.Error)
			}
		})
	}
}

func TestRefreshUnknownAndEmpty(t *testing.T) {
	if _, err := RefreshSignatures("nope", "s", nil, []byte(`{}`), time.Now()); err == nil {
		t.Fatal("unknown provider must error")
	}
	if _, err := RefreshSignatures("stripe", "", nil, []byte(`{}`), time.Now()); err == nil {
		t.Fatal("empty secret must error")
	}
	if _, err := RefreshSignatures("generic", "s", nil, []byte(`{}`), time.Now()); err == nil {
		t.Fatal("generic provider must error")
	}
	if _, err := RefreshSignatures("standard", "whsec_"+b64([]byte("resign-key-12345678901234567890")), map[string]string{}, []byte(`{}`), time.Now()); err == nil {
		t.Fatal("missing webhook id must error")
	}
}
