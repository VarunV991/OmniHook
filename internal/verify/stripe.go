package verify

import (
	"fmt"
	"strings"
	"time"
)

// Stripe: header Stripe-Signature: t=<unix>,v1=<hex>
// signed = "<t>.<raw_body>", HMAC_SHA256(secret). Tolerance 5 min.
type Stripe struct{}

func (Stripe) Name() string { return "stripe" }
func (Stripe) Detect(h map[string]string) bool {
	return headerLookup(h, "Stripe-Signature") != ""
}
func (Stripe) Verify(secret string, h map[string]string, raw []byte, now time.Time) Result {
	if secret == "" {
		return errResult("stripe", "missing secret: set endpoint secret to Stripe webhook signing secret (whsec_...)", "In OmniHook endpoint settings paste the secret from Stripe Dashboard > Webhooks > Signing secret.")
	}
	sig := headerLookup(h, "Stripe-Signature")
	var tStr string
	var candidates []string
	for _, part := range strings.Split(sig, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		if kv[0] == "t" {
			tStr = kv[1]
		}
		// Collect every v1 candidate: key rotation sends several signatures
		// and a match on ANY of them accepts (order-independent).
		if kv[0] == "v1" {
			candidates = append(candidates, kv[1])
		}
	}
	if tStr == "" || len(candidates) == 0 {
		return errResult("stripe", "malformed Stripe-Signature header (need t=,v1=)", "Ensure provider is Stripe and header was forwarded intact; proxies must not strip it.")
	}
	ts, err := parseInt(tStr)
	if err != nil {
		return errResult("stripe", "bad timestamp in Stripe-Signature", "")
	}
	if now.Unix()-ts > 300 || ts-now.Unix() > 300 {
		return errResult("stripe", "timestamp outside 5-min tolerance (replay attack guard or clock skew)", "Check server clock (NTP). To debug an old capture, replay it with --resign (CLI), resign:true (API) or the UI re-sign checkbox.")
	}
	want := hmacSHA256Hex(secret, []byte(fmt.Sprintf("%d.%s", ts, string(raw))))
	for _, v1 := range candidates {
		if secureEqual(strings.ToLower(want), strings.ToLower(v1)) {
			return Result{Status: PASS, Provider: "stripe"}
		}
	}
	return errResult("stripe", "signature mismatch: HMAC_SHA256(secret, t.raw_body) != v1 (no candidate matched)",
		"You parsed JSON before verifying. Fix: Express: app.post('/hook', express.raw({type:'application/json'})); Spring Boot: @RequestBody byte[] raw + Mac/HmacSHA256; FastAPI: raw = await request.body(); Django: request.body (never request.POST). Then compare with hmac.compare_digest.")
}
