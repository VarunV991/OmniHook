package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"testing"
	"time"
)

func TestStripePassAndRawBodyMismatch(t *testing.T) {
	secret := "whsec_test123"
	raw := []byte(`{"id":"evt_1","object":"event"}`)
	ts := time.Now().Unix()
	msg := fmt.Sprintf("%d.%s", ts, string(raw))
	m := hmac.New(sha256.New, []byte(secret))
	m.Write([]byte(msg))
	v1 := hex.EncodeToString(m.Sum(nil))
	h := map[string]string{"Stripe-Signature": fmt.Sprintf("t=%d,v1=%s", ts, v1)}
	got := (Stripe{}).Verify(secret, h, raw, time.Now())
	if got.Status != PASS {
		t.Fatalf("expected PASS, got %+v", got)
	}
	// Pretty-printed JSON must FAIL (raw-byte sensitivity) with actionable hint.
	pretty := []byte("{\n  \"id\": \"evt_1\",\n  \"object\": \"event\"\n}")
	got2 := (Stripe{}).Verify(secret, h, pretty, time.Now())
	if got2.Status != FAIL || got2.FixHint == "" {
		t.Fatalf("expected FAIL with hint, got %+v", got2)
	}
}

func TestStandardWebhooksRoundTrip(t *testing.T) {
	rawSecret := []byte("test-secret-123456789012345678")
	secret := "whsec_" + base64.StdEncoding.EncodeToString(rawSecret)
	id := "msg_test1"
	ts := fmt.Sprint(time.Now().Unix())
	raw := []byte(`{"type":"payment.succeeded"}`)
	m := hmac.New(sha256.New, rawSecret)
	m.Write([]byte(id + "." + ts + "." + string(raw)))
	sig := base64.StdEncoding.EncodeToString(m.Sum(nil))
	h := map[string]string{
		"Webhook-Id":        id,
		"Webhook-Timestamp": ts,
		"Webhook-Signature": "v1," + sig,
	}
	got := (Standard{}).Verify(secret, h, raw, time.Now())
	if got.Status != PASS {
		t.Fatalf("expected PASS, got %+v", got)
	}
}

func TestChainSkippedWhenNoHeaders(t *testing.T) {
	got := Chain("", "generic", map[string]string{}, []byte(`{}`), time.Now())
	if got.Status != SKIPPED {
		t.Fatalf("expected SKIPPED, got %+v", got)
	}
}
