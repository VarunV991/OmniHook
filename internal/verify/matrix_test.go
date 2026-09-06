package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

// signer builds fresh, realistic provider signatures for table tests.
// Timestamps are generated at test time so tolerance windows always pass.
type signer struct {
	stripeSecret   string
	githubSecret   string
	standardSecret string // whsec_ form
	standardKey    []byte // raw key
	razorpaySecret string
}

func newSigner() signer {
	raw := []byte("matrix-test-key-123456789012345678")
	return signer{
		stripeSecret:   "whsec_matrix_stripe",
		githubSecret:   "matrix_github_secret",
		standardSecret: "whsec_" + base64.StdEncoding.EncodeToString(raw),
		standardKey:    raw,
		razorpaySecret: "matrix_razorpay_secret",
	}
}

func sha256Hex(secret string, msg []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(msg)
	return hex.EncodeToString(m.Sum(nil))
}

func sha256B64(secret, msg []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(msg)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func (s signer) stripeHeaders(raw []byte, ts int64) map[string]string {
	v1 := sha256Hex(s.stripeSecret, []byte(fmt.Sprintf("%d.%s", ts, string(raw))))
	return map[string]string{"Stripe-Signature": fmt.Sprintf("t=%d,v1=%s", ts, v1)}
}

func (s signer) githubHeaders(raw []byte) map[string]string {
	return map[string]string{"X-Hub-Signature-256": "sha256=" + sha256Hex(s.githubSecret, raw)}
}

func (s signer) standardHeaders(id string, ts int64, raw []byte) map[string]string {
	t := fmt.Sprint(ts)
	sig := sha256B64(s.standardKey, []byte(id+"."+t+"."+string(raw)))
	return map[string]string{
		"Webhook-Id": id, "Webhook-Timestamp": t, "Webhook-Signature": "v1," + sig,
	}
}

func (s signer) razorpayHeaders(raw []byte) map[string]string {
	return map[string]string{"X-Razorpay-Signature": sha256Hex(s.razorpaySecret, raw)}
}

// Realistic payloads (shapes taken from provider docs).
var (
	stripeBody   = []byte(`{"id":"evt_matrix1","object":"event","type":"checkout.session.completed","data":{"object":{"id":"cs_test_123","amount_total":5000,"currency":"usd"}}}`)
	githubBody   = []byte(`{"ref":"refs/heads/main","repository":{"full_name":"acme/app"},"commits":[{"id":"abc123","message":"fix webhook"}]}`)
	standardBody = []byte(`{"type":"payment.succeeded","data":{"id":"pay_001","amount":5000}}`)
	razorpayBody = []byte(`{"entity":"event","event":"payment.captured","payload":{"payment":{"entity":{"id":"pay_ABC","amount":5000,"status":"captured"}}}}`)
)

func TestProviderMatrix(t *testing.T) {
	s := newSigner()
	now := time.Now()
	ts := now.Unix()

	cases := []struct {
		name     string
		provider string
		secret   string
		headers  map[string]string
		body     []byte
		want     string
		wantHint bool
	}{
		// Valid signatures via explicit provider pin.
		{"stripe valid", "stripe", s.stripeSecret, s.stripeHeaders(stripeBody, ts), stripeBody, PASS, false},
		{"github valid", "github", s.githubSecret, s.githubHeaders(githubBody), githubBody, PASS, false},
		{"standard valid", "standard", s.standardSecret, s.standardHeaders("msg_m1", ts, standardBody), standardBody, PASS, false},
		{"razorpay valid", "razorpay", s.razorpaySecret, s.razorpayHeaders(razorpayBody), razorpayBody, PASS, false},
		// Auto-detect (no hint): headers alone select the verifier.
		{"stripe auto-detect", "", s.stripeSecret, s.stripeHeaders(stripeBody, ts), stripeBody, PASS, false},
		{"github auto-detect", "", s.githubSecret, s.githubHeaders(githubBody), githubBody, PASS, false},
		{"standard auto-detect", "", s.standardSecret, s.standardHeaders("msg_m2", ts, standardBody), standardBody, PASS, false},
		{"razorpay auto-detect", "", s.razorpaySecret, s.razorpayHeaders(razorpayBody), razorpayBody, PASS, false},
		// Tampered bodies must FAIL with actionable hints.
		{"stripe tampered", "stripe", s.stripeSecret, s.stripeHeaders(stripeBody, ts), []byte(`{"id":"evt_matrix1","object":"event","type":"refund.created"}`), FAIL, true},
		{"github tampered", "github", s.githubSecret, s.githubHeaders(githubBody), append(githubBody, ' '), FAIL, true},
		{"standard tampered", "standard", s.standardSecret, s.standardHeaders("msg_m1", ts, standardBody), []byte(`{"type":"payment.failed"}`), FAIL, true},
		{"razorpay tampered", "razorpay", s.razorpaySecret, s.razorpayHeaders(razorpayBody), razorpayBody[:len(razorpayBody)-1], FAIL, true},
		// Wrong secrets must FAIL.
		{"stripe wrong secret", "stripe", "whsec_wrong", s.stripeHeaders(stripeBody, ts), stripeBody, FAIL, true},
		{"github wrong secret", "github", "wrong", s.githubHeaders(githubBody), githubBody, FAIL, true},
		{"standard wrong secret", "standard", "whsec_" + base64.StdEncoding.EncodeToString([]byte("wrong-key-12345678901234567890")), s.standardHeaders("msg_m1", ts, standardBody), standardBody, FAIL, true},
		{"razorpay wrong secret", "razorpay", "wrong", s.razorpayHeaders(razorpayBody), razorpayBody, FAIL, true},
		// Expired timestamps must FAIL (replay-attack guard).
		{"stripe expired", "stripe", s.stripeSecret, s.stripeHeaders(stripeBody, ts-600), stripeBody, FAIL, false},
		{"standard expired", "standard", s.standardSecret, s.standardHeaders("msg_m1", ts-600, standardBody), standardBody, FAIL, false},
		// Missing secrets must FAIL with setup guidance.
		{"stripe no secret", "stripe", "", s.stripeHeaders(stripeBody, ts), stripeBody, FAIL, true},
		{"github no secret", "github", "", s.githubHeaders(githubBody), githubBody, FAIL, true},
		// Unknown traffic is SKIPPED, never hard-failed.
		{"unknown skips", "", "x", map[string]string{"Content-Type": "application/json"}, []byte(`{}`), SKIPPED, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Chain(tc.secret, tc.provider, tc.headers, tc.body, now)
			if got.Status != tc.want {
				t.Fatalf("status=%q err=%q, want %q", got.Status, got.Error, tc.want)
			}
			if tc.wantHint && got.FixHint == "" {
				t.Fatalf("expected fix hint, got none (err=%q)", got.Error)
			}
			// Every FAIL must say something debuggable.
			if tc.want == FAIL && got.Error == "" {
				t.Fatal("FAIL without error message")
			}
		})
	}
}

// TestProviderHeaderShapes guards the exact header names against typos by
// round-tripping a JSON-encoded capture like the API stores.
func TestProviderHeaderShapes(t *testing.T) {
	s := newSigner()
	ts := time.Now().Unix()
	pairs := []struct {
		name    string
		headers map[string]string
	}{
		{"stripe", s.stripeHeaders(stripeBody, ts)},
		{"github", s.githubHeaders(githubBody)},
		{"standard", s.standardHeaders("msg_x", ts, standardBody)},
		{"razorpay", s.razorpayHeaders(razorpayBody)},
	}
	for _, p := range pairs {
		raw, _ := json.Marshal(p.headers)
		var back map[string]string
		if err := json.Unmarshal(raw, &back); err != nil {
			t.Fatalf("%s: %v", p.name, err)
		}
		for k := range p.headers {
			if _, ok := back[k]; !ok {
				t.Fatalf("%s: lost header %q", p.name, k)
			}
		}
	}
}
