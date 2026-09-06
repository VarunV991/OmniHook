package forward

import (
	"database/sql"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Service bounds automatic forwarding: a fixed worker pool drains a bounded
// queue so a burst against a stalled target cannot spawn unbounded goroutines
// (review #06). Drops are recorded in `replays` — never silent.
type Service struct {
	db      *sql.DB
	ownPort string
	queue   chan job
	quit    chan struct{}
	wg      sync.WaitGroup
	dropped atomic.Int64
	// stopOnce makes Stop idempotent: shutdown paths and test cleanups
	// may both call it, and close(quit) twice would panic.
	stopOnce sync.Once
}

type job struct {
	requestID string
	target    string
}

// NewService starts workers immediately. workers/queueLen <= 0 get safe defaults.
func NewService(db *sql.DB, ownPort string, workers, queueLen int) *Service {
	if workers <= 0 {
		workers = 8
	}
	if queueLen <= 0 {
		queueLen = 128
	}
	s := &Service{db: db, ownPort: ownPort, queue: make(chan job, queueLen), quit: make(chan struct{})}
	for i := 0; i < workers; i++ {
		s.wg.Add(1)
		go s.work()
	}
	return s
}

func (s *Service) work() {
	defer s.wg.Done()
	for {
		select {
		case <-s.quit:
			return
		case j := <-s.queue:
			Deliver(s.db, j.requestID, j.target)
		}
	}
}

// Enqueue schedules delivery. It returns false (and records the drop) when the
// queue is full or the target loops back into this server.
func (s *Service) Enqueue(requestID, target string) bool {
	if selfTarget(s.ownPort, target) {
		record(s.db, requestID, target, 0, 0, "refused: target loops back into this server")
		return false
	}
	select {
	case s.queue <- job{requestID, target}:
		return true
	default:
		s.dropped.Add(1)
		record(s.db, requestID, target, 0, 0, "dropped: forward queue full")
		return false
	}
}

// Dropped counts queue-full drops since start.
func (s *Service) Dropped() int64 { return s.dropped.Load() }

// Stop shuts down workers. It first waits (up to timeout) for queued work to
// drain so acknowledged captures are recorded, then closes the quit channel
// and waits again for workers to exit. Idempotent.
func (s *Service) Stop(timeout time.Duration) {
	s.stopOnce.Do(func() {
		deadline := time.Now().Add(timeout)
		for len(s.queue) > 0 && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		close(s.quit)
	})
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
}

// selfTarget reports whether target addresses this server's own capture
// surface (localhost + own port + /hook/ prefix). Such forwarding would
// capture-and-forward forever (review #08).
func selfTarget(ownPort, target string) bool {
	u, err := url.Parse(target)
	if err != nil {
		return false
	}
	if !strings.HasPrefix(u.Path, "/hook/") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host != "localhost" && host != "127.0.0.1" && host != "::1" {
		return false
	}
	port := u.Port()
	if port == "" {
		port = "80"
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		}
	}
	return port == ownPort
}

// Marked reports whether an incoming request already came from OmniHook
// forwarding/replay. Marked requests are captured as evidence but never
// re-forwarded: the marker is untrusted, so it authorizes nothing — it only
// breaks accidental amplification loops (review #08).
func Marked(h http.Header) bool {
	return h.Get("X-Omnihook-Forward") != "" || h.Get("X-Omnihook-Replay") != ""
}
