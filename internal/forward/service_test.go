package forward

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/you/omnihook/internal/db"
)

func TestBoundedPoolDrainsBurst(t *testing.T) {
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug) VALUES('q')`); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // stall: force queue pressure
		w.WriteHeader(200)
	}))
	defer target.Close()
	svc := NewService(sqldb, "9", 2, 4)
	const burst = 12
	for i := 0; i < burst; i++ {
		id := "q-" + string(rune('a'+i))
		if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method) VALUES(?, 'q', 'POST')`, id); err != nil {
			t.Fatal(err)
		}
		svc.Enqueue(id, target.URL)
	}
	close(release)
	svc.Stop(10 * time.Second)
	var delivered, dropped int
	_ = sqldb.QueryRow(`SELECT COUNT(*) FROM replays WHERE status_code=200`).Scan(&delivered)
	_ = sqldb.QueryRow(`SELECT COUNT(*) FROM replays WHERE error LIKE 'dropped%'`).Scan(&dropped)
	if delivered+dropped != burst {
		t.Fatalf("accounted %d+%d, want %d", delivered, dropped, burst)
	}
	if got := svc.Dropped(); got != int64(dropped) {
		t.Fatalf("counter %d != rows %d", got, dropped)
	}
}

func TestSelfTargetRefused(t *testing.T) {
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug) VALUES('s')`); err != nil {
		t.Fatal(err)
	}
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method) VALUES('s-1','s','POST')`); err != nil {
		t.Fatal(err)
	}
	svc := NewService(sqldb, "8080", 1, 4)
	defer svc.Stop(0)
	for _, target := range []string{
		"http://localhost:8080/hook/s",
		"http://127.0.0.1:8080/hook/other/x?y=1",
	} {
		if svc.Enqueue("s-1", target) {
			t.Fatalf("self-target accepted: %s", target)
		}
	}
	if svc.Enqueue("s-1", "http://localhost:3000/hook") {
		// queued (no server needed: refusal is the only assertion here)
	} else {
		t.Fatal("external localhost target refused")
	}
	svc.Stop(2 * time.Second)
	var n int
	_ = sqldb.QueryRow(`SELECT COUNT(*) FROM replays WHERE error LIKE 'refused%'`).Scan(&n)
	if n != 2 {
		t.Fatalf("refusals recorded = %d, want 2", n)
	}
}

func TestMarked(t *testing.T) {
	h := http.Header{"X-Omnihook-Forward": {"true"}}
	if !Marked(h) {
		t.Fatal("forward marker not detected")
	}
	if Marked(http.Header{"X-Other": {"1"}}) {
		t.Fatal("false positive")
	}
}
