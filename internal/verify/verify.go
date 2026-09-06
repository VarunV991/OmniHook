package verify

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Status values stored in requests.verify_status.
const (
	PASS    = "PASS"
	FAIL    = "FAIL"
	SKIPPED = "SKIPPED"
	ERROR   = "ERROR"
)

// Result is the outcome of one verification attempt.
type Result struct {
	Status   string
	Provider string
	Error    string
	FixHint  string
}

// Verifier verifies raw body bytes against headers + secret.
type Verifier interface {
	Name() string
	// Detect returns true if headers look like this provider.
	Detect(headers map[string]string) bool
	Verify(secret string, headers map[string]string, rawBody []byte, now time.Time) Result
}

func secureEqual(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func hmacSHA256Hex(secret string, msg []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(msg)
	return hex.EncodeToString(m.Sum(nil))
}

func hmacSHA256Raw(secret string, msg []byte) []byte {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(msg)
	return m.Sum(nil)
}

func hmacSHA256Base64(secret []byte, msg []byte) string {
	m := hmac.New(sha256.New, secret)
	m.Write(msg)
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

func headerLookup(h map[string]string, name string) string {
	for k, v := range h {
		if strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func parseInt(s string) (int64, error) { return strconv.ParseInt(strings.TrimSpace(s), 10, 64) }

func errResult(provider, msg, hint string) Result {
	return Result{Status: FAIL, Provider: provider, Error: msg, FixHint: hint}
}

var _ = fmt.Sprintf // keep fmt if unused during scaffold
