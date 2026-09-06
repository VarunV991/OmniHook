package ratelimit

import (
	"testing"
	"time"
)

func TestBurstThenDenyThenRefill(t *testing.T) {
	now := time.Now()
	l := New(3)
	l.now = func() time.Time { return now }
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("burst %d denied", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("over-burst allowed")
	}
	// Other IPs unaffected.
	if !l.Allow("5.6.7.8") {
		t.Fatal("independent IP denied")
	}
	// Refill after 2s at 3 rps -> capped at burst 3.
	now = now.Add(2 * time.Second)
	for i := 0; i < 3; i++ {
		if !l.Allow("1.2.3.4") {
			t.Fatalf("refill %d denied", i)
		}
	}
	if l.Allow("1.2.3.4") {
		t.Fatal("over-refill allowed")
	}
}

func TestDisabled(t *testing.T) {
	l := New(0)
	for i := 0; i < 100; i++ {
		if !l.Allow("any") {
			t.Fatal("disabled limiter denied")
		}
	}
}
