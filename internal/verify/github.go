package verify

import (
	"strings"
	"time"
)

// GitHub: X-Hub-Signature-256: sha256=<hex>, HMAC_SHA256(secret, raw).
type GitHub struct{}

func (GitHub) Name() string { return "github" }
func (GitHub) Detect(h map[string]string) bool {
	return headerLookup(h, "X-Hub-Signature-256") != "" || headerLookup(h, "X-Hub-Signature") != ""
}
func (GitHub) Verify(secret string, h map[string]string, raw []byte, _ time.Time) Result {
	if secret == "" {
		return errResult("github", "missing secret: set GitHub webhook secret", "Repo Settings > Webhooks > Secret, paste same value into OmniHook endpoint.")
	}
	got := headerLookup(h, "X-Hub-Signature-256")
	if got == "" {
		return errResult("github", "only legacy X-Hub-Signature (SHA1) present; expected X-Hub-Signature-256", "Upgrade webhook to SHA-256 in GitHub settings; keep secret identical.")
	}
	got = strings.TrimPrefix(got, "sha256=")
	want := hmacSHA256Hex(secret, raw)
	if !secureEqual(strings.ToLower(want), strings.ToLower(got)) {
		return errResult("github", "signature mismatch", "Verify with raw bytes: do not JSON-pretty-print before HMAC. Check secret trailing spaces/newlines.")
	}
	return Result{Status: PASS, Provider: "github"}
}
