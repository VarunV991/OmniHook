package verify

import (
	"encoding/base64"
	"strings"
	"time"
)

// Shopify: X-Shopify-Hmac-Sha256: <base64>, base64(HMAC_SHA256(secret, raw)).
// Note the base64 encoding — unlike GitHub/Stripe which use hex.
type Shopify struct{}

func (Shopify) Name() string { return "shopify" }
func (Shopify) Detect(h map[string]string) bool {
	return headerLookup(h, "X-Shopify-Hmac-Sha256") != "" ||
		headerLookup(h, "X-Shopify-Topic") != ""
}
func (Shopify) Verify(secret string, h map[string]string, raw []byte, _ time.Time) Result {
	if secret == "" {
		return errResult("shopify", "missing secret: use the secret from Shopify Admin > Settings > Notifications > Webhooks",
			"Paste the same secret into the OmniHook endpoint; Shopify signs with base64(HMAC_SHA256(secret, raw_body)).")
	}
	got := strings.TrimSpace(headerLookup(h, "X-Shopify-Hmac-Sha256"))
	if got == "" {
		return errResult("shopify", "missing X-Shopify-Hmac-Sha256 header", "Ensure the request really came from Shopify and proxies preserve headers.")
	}
	m := hmacSHA256Raw(secret, raw)
	want := base64.StdEncoding.EncodeToString(m)
	if !secureEqual(want, got) {
		return errResult("shopify", "signature mismatch (base64 HMAC-SHA256 over raw body)",
			"Shopify uses base64, not hex. Verify with raw bytes: do not re-serialize JSON. Check for trailing whitespace in the secret.")
	}
	return Result{Status: PASS, Provider: "shopify"}
}
