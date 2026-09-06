package verify

import (
	"encoding/base64"
	"strings"
	"time"
)

// Standard Webhooks (Svix/OpenAI/Anthropic/Clerk/Resend/Supabase shape):
// Webhook-Id, Webhook-Timestamp, Webhook-Signature: v1,<base64> [v1,<base64> ...]
// signed = "<id>.<timestamp>.<raw>", key = base64decode(secret after whsec_).
type Standard struct{}

func (Standard) Name() string { return "standard" }
func (Standard) Detect(h map[string]string) bool {
	return headerLookup(h, "Webhook-Signature") != "" || headerLookup(h, "Svix-Signature") != ""
}
func (Standard) Verify(secret string, h map[string]string, raw []byte, now time.Time) Result {
	if secret == "" {
		return errResult("standard", "missing secret (whsec_...)", "Paste endpoint signing secret; OmniHook strips the whsec_ prefix and base64-decodes the rest.")
	}
	id := headerLookup(h, "Webhook-Id")
	if id == "" {
		id = headerLookup(h, "Svix-Id")
	}
	tsStr := headerLookup(h, "Webhook-Timestamp")
	if tsStr == "" {
		tsStr = headerLookup(h, "Svix-Timestamp")
	}
	sigH := headerLookup(h, "Webhook-Signature")
	if sigH == "" {
		sigH = headerLookup(h, "Svix-Signature")
	}
	if id == "" || tsStr == "" || sigH == "" {
		return errResult("standard", "missing Webhook-Id/Timestamp/Signature headers", "Ensure sender follows standardwebhooks.com spec; check proxy preserves headers.")
	}
	ts, err := parseInt(tsStr)
	if err != nil {
		return errResult("standard", "bad Webhook-Timestamp (need seconds since epoch)", "")
	}
	if now.Unix()-ts > 300 || ts-now.Unix() > 300 {
		return errResult("standard", "timestamp outside 5-min tolerance", "Sync clock via NTP. Old replays will FAIL by design.")
	}
	key := strings.TrimPrefix(secret, "whsec_")
	keyBytes, err := base64.StdEncoding.DecodeString(key)
	if err != nil {
		return errResult("standard", "secret is not valid base64 after whsec_ prefix", "Copy full secret including whsec_ from provider dashboard.")
	}
	msg := id + "." + strings.TrimSpace(tsStr) + "." + string(raw)
	want := hmacSHA256Base64(keyBytes, []byte(msg))
	for _, part := range strings.Fields(sigH) {
		sig := strings.TrimPrefix(strings.TrimSpace(part), "v1,")
		if secureEqual(sig, want) {
			return Result{Status: PASS, Provider: "standard"}
		}
	}
	return errResult("standard", "no v1 signature matched", "Use raw body bytes; JSON re-serialization breaks the signature. See https://standardwebhooks.com checklists.")
}
