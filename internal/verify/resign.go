package verify

import (
	"encoding/base64"
	"fmt"
	"strings"
	"time"
)

func decodeB64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
func b64(b []byte) string                { return base64.StdEncoding.EncodeToString(b) }

// RefreshSignatures recomputes time-sensitive signature headers for body using
// secret, returning headers the caller should set (override) on replay.
// This lets old captures (expired timestamps) replay as PASS during debugging
// while keeping the original event identity (Stripe/Github payloads untouched,
// Standard keeps its Webhook-Id for idempotency).
//
// Supported: stripe, github, standard (incl. svix-* header shape), razorpay,
// shopify. Unknown providers return an empty map.
func RefreshSignatures(provider, secret string, headers map[string]string, body []byte, now time.Time) map[string]string {
	if secret == "" {
		return nil
	}
	switch strings.ToLower(provider) {
	case "stripe":
		ts := now.Unix()
		v1 := hmacSHA256Hex(secret, []byte(fmt.Sprintf("%d.%s", ts, string(body))))
		return map[string]string{"Stripe-Signature": fmt.Sprintf("t=%d,v1=%s", ts, v1)}
	case "github":
		return map[string]string{"X-Hub-Signature-256": "sha256=" + hmacSHA256Hex(secret, body)}
	case "standard":
		id := headerLookup(headers, "Webhook-Id")
		if id == "" {
			id = headerLookup(headers, "Svix-Id")
		}
		if id == "" {
			return nil
		}
		ts := fmt.Sprint(now.Unix())
		key := strings.TrimPrefix(secret, "whsec_")
		keyBytes, err := decodeB64(key)
		if err != nil {
			return nil
		}
		sig := hmacSHA256Base64(keyBytes, []byte(id+"."+ts+"."+string(body)))
		out := map[string]string{"Webhook-Timestamp": ts, "Webhook-Signature": "v1," + sig}
		if headerLookup(headers, "Svix-Signature") != "" {
			out = map[string]string{"Svix-Timestamp": ts, "Svix-Signature": "v1," + sig}
		}
		return out
	case "razorpay":
		return map[string]string{"X-Razorpay-Signature": hmacSHA256Hex(secret, body)}
	case "shopify":
		return map[string]string{"X-Shopify-Hmac-Sha256": b64(hmacSHA256Raw(secret, body))}
	default:
		return nil
	}
}
