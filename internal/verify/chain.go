package verify

import (
	"strconv"
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

func (g Generic) Name() string                  { return "generic" }
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

// Known providers accepted by endpoint configuration. "auto" (and "") means
// detect from headers at capture time; the effective provider is recorded
// per request (review #12, #14).
var knownProviders = []string{"stripe", "github", "standard", "razorpay", "shopify", "generic", "auto"}

// ValidProvider reports whether name is an accepted endpoint provider.
func ValidProvider(name string) bool {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" {
		return true
	}
	for _, k := range knownProviders {
		if name == k {
			return true
		}
	}
	return false
}

// Chain runs detectors in order; returns SKIPPED if none match.
// An unknown non-empty hint is a configuration error (FAIL), never silent.
func Chain(secret, providerHint string, headers map[string]string, raw []byte, now time.Time) Result {
	all := []Verifier{Stripe{}, GitHub{}, Standard{}, Razorpay{}, Shopify{}}
	hint := strings.ToLower(strings.TrimSpace(providerHint))
	if hint != "" && hint != "generic" && hint != "auto" {
		for _, v := range all {
			if v.Name() == hint {
				return v.Verify(secret, headers, raw, now)
			}
		}
		return Result{Status: FAIL, Provider: hint,
			Error:   "unknown provider " + strconv.Quote(providerHint),
			FixHint: "Set provider to one of stripe|github|standard|razorpay|shopify|generic|auto."}
	}
	for _, v := range all {
		if v.Detect(headers) {
			return v.Verify(secret, headers, raw, now)
		}
	}
	return Result{Status: SKIPPED, Provider: "generic"}
}
