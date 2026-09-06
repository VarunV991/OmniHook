// Package ratelimit is a tiny in-memory token bucket per client IP for the
// capture path. When the bucket is empty the request is still stored (evidence
// for debugging) but answered 429 + Retry-After so providers back off instead
// of piling retries onto a struggling local handler.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter allows rps requests per second with bursts up to rps (min 1).
// rps <= 0 disables limiting (Allow always true). now is injectable for tests.
type Limiter struct {
	mu      sync.Mutex
	rps     int
	now     func() time.Time
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
	seen   time.Time
}

func New(rps int) *Limiter {
	return &Limiter{rps: rps, now: time.Now, buckets: map[string]*bucket{}}
}

func (l *Limiter) Allow(ip string) bool {
	if l.rps <= 0 {
		return true
	}
	t := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	// Opportunistic sweep: drop buckets idle > 60s to bound memory.
	for k, b := range l.buckets {
		if t.Sub(b.seen) > time.Minute {
			delete(l.buckets, k)
		}
	}
	b, ok := l.buckets[ip]
	if !ok {
		b = &bucket{tokens: float64(l.rps), last: t}
		l.buckets[ip] = b
	}
	b.tokens += t.Sub(b.last).Seconds() * float64(l.rps)
	if b.tokens > float64(l.rps) {
		b.tokens = float64(l.rps)
	}
	b.last = t
	b.seen = t
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}
