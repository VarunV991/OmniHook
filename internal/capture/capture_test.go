package capture

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/you/omnihook/internal/config"
	"github.com/you/omnihook/internal/db"
)

func testHandler(t *testing.T, rps int) *Handler {
	t.Helper()
	cfg := config.Config{
		Port: "0", DataDir: t.TempDir(), DBPath: filepath.Join(t.TempDir(), "test.db"),
		RetentionHrs: 168, MaxBodyBytes: 1 << 20, RateLimitRPS: rps, Version: "test",
	}
	sqldb, err := db.Open(cfg.DBPath)
	if err != nil {
		t.Fatalf("db open: %v", err)
	}
	h := NewHandler(sqldb, cfg, NewHub())
	t.Cleanup(func() {
		h.Forwarder.Stop(0)
		_ = sqldb.Close()
	})
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug) VALUES('rl')`); err != nil {
		t.Fatal(err)
	}
	return h
}

func post(t *testing.T, h *Handler) int {
	t.Helper()
	req := httptest.NewRequest("POST", "/hook/rl", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHook(rec, req)
	return rec.Code
}

func count(t *testing.T, h *Handler) int {
	t.Helper()
	var n int
	_ = h.DB.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n)
	return n
}

func TestRateLimitStoresButAnswers429(t *testing.T) {
	h := testHandler(t, 2)
	if post(t, h) != 200 || post(t, h) != 200 {
		t.Fatal("burst of 2 must pass")
	}
	if code := post(t, h); code != 429 {
		t.Fatalf("over-burst = %d, want 429", code)
	}
	// Evidence preserved: all three stored despite the 429.
	if n := count(t, h); n != 3 {
		t.Fatalf("stored = %d, want 3", n)
	}
}

func TestRateLimitDisabled(t *testing.T) {
	h := testHandler(t, 0)
	for i := 0; i < 10; i++ {
		if code := post(t, h); code != 200 {
			t.Fatalf("req %d = %d", i, code)
		}
	}
}

func TestHubFansOutToAllSubscribers(t *testing.T) {
	hub := NewHub()
	a, unsubA := hub.Subscribe()
	defer unsubA()
	b, unsubB := hub.Subscribe()
	defer unsubB()
	hub.Broadcast("id-1")
	hub.Broadcast("id-2")
	for _, ch := range []chan string{a, b} {
		for _, want := range []string{"id-1", "id-2"} {
			select {
			case got := <-ch:
				if got != want {
					t.Fatalf("got %q want %q", got, want)
				}
			default:
				t.Fatal("subscriber missed broadcast")
			}
		}
	}
	// Unsubscribed client receives nothing further.
	unsubB()
	hub.Broadcast("id-3")
	select {
	case got := <-a:
		if got != "id-3" {
			t.Fatalf("got %q", got)
		}
	default:
		t.Fatal("remaining subscriber missed broadcast")
	}
	// Unsubscribed client receives nothing further (channel is dropped, not
	// closed, so concurrent Broadcast can never panic on send-to-closed).
	select {
	case got := <-b:
		t.Fatalf("unsubscribed got %q", got)
	default:
	}
}

// Marked incoming requests are captured as evidence but never re-forwarded
// (loop breaker for replay-of-replay and endpoint cycles).
func TestMarkedIncomingSkipsForward(t *testing.T) {
	h := testHandler(t, 0)
	if _, err := h.DB.Exec(`UPDATE endpoints SET target_url='http://127.0.0.1:1/x' WHERE slug='rl'`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/hook/rl", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Omnihook-Forward", "true")
	rec := httptest.NewRecorder()
	h.ServeHook(rec, req)
	if rec.Code != 200 {
		t.Fatalf("capture = %d", rec.Code)
	}
	h.Forwarder.Stop(2 * time.Second)
	var n int
	_ = h.DB.QueryRow(`SELECT COUNT(*) FROM replays`).Scan(&n)
	if n != 0 {
		t.Fatalf("marked request was forwarded (%d attempts)", n)
	}
	if n := count(t, h); n != 1 {
		t.Fatalf("marked request not stored (%d)", n)
	}
}
