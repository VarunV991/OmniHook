package verify

import (
	"strings"
	"time"
)

// Razorpay: X-Razorpay-Signature: <hex>, HMAC_SHA256(secret, raw).
type Razorpay struct{}

func (Razorpay) Name() string { return "razorpay" }
func (Razorpay) Detect(h map[string]string) bool {
	return headerLookup(h, "X-Razorpay-Signature") != ""
}
func (Razorpay) Verify(secret string, h map[string]string, raw []byte, _ time.Time) Result {
	if secret == "" {
		return errResult("razorpay", "missing webhook secret", "Dashboard > Settings > Webhooks > Secret, paste into OmniHook endpoint.")
	}
	got := strings.TrimSpace(headerLookup(h, "X-Razorpay-Signature"))
	want := hmacSHA256Hex(secret, raw)
	if !secureEqual(strings.ToLower(want), strings.ToLower(got)) {
		return errResult("razorpay", "signature mismatch", "Use request.body raw bytes; ensure secret has no trailing whitespace.")
	}
	return Result{Status: PASS, Provider: "razorpay"}
}

// Generic HMAC fallback: configured header, e.g. X-Acme-Sig: sha256=<hex>.
type Generic struct {
	Header string
	Prefix string
}

func (g Generic) Name() string { return "generic" }
func (g Generic) Detect(map[string]string) bool { return false }
func (g Generic) Verify(secret string, h map[string]string, raw []byte, _ time.Time) Result {
	if secret == "" || g.Header == "" {
		return Result{Status: SKIPPED, Provider: "generic"}
	}
	got := strings.TrimSpace(headerLookup(h, g.Header))
	got = strings.TrimPrefix(got, g.Prefix)
	want := hmacSHA256Hex(secret, raw)
	if !secureEqual(strings.ToLower(want), strings.ToLower(got)) {
		return errResult("generic", "generic HMAC mismatch for "+g.Header, "Check header name/prefix config and raw-body handling.")
	}
	return Result{Status: PASS, Provider: "generic"}
}

// Chain runs detectors in order; returns SKIPPED if none match.
func Chain(secret, providerHint string, headers map[string]string, raw []byte, now time.Time) Result {
	all := []Verifier{Stripe{}, GitHub{}, Standard{}, Razorpay{}}
	if providerHint != "" && providerHint != "generic" {
		for _, v := range all {
			if v.Name() == providerHint {
				return v.Verify(secret, headers, raw, now)
			}
		}
	}
	for _, v := range all {
		if v.Detect(headers) {
			return v.Verify(secret, headers, raw, now)
		}
	}
	return Result{Status: SKIPPED, Provider: "generic"}
}
