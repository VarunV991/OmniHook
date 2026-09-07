package gc

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/you/omnihook/internal/db"
)

func TestRunDeletesOnlyExpired(t *testing.T) {
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	_, err = sqldb.Exec(`INSERT INTO endpoints(slug) VALUES('g1'),('gone'),('busy')`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = sqldb.Exec(`UPDATE endpoints SET expires_at=? WHERE slug='gone'`, time.Now().UTC().Add(-time.Hour).Format(layout))
	_, _ = sqldb.Exec(`UPDATE endpoints SET expires_at=? WHERE slug='busy'`, time.Now().UTC().Add(-time.Hour).Format(layout))
	stmts := []string{
		`INSERT INTO requests(id, endpoint_slug, method, received_at) VALUES('old','g1','POST',datetime('now','-49 hours'))`,
		`INSERT INTO requests(id, endpoint_slug, method) VALUES('fresh','g1','POST')`,
		`INSERT INTO requests(id, endpoint_slug, method) VALUES('keep','busy','POST')`,
	}
	for _, s := range stmts {
		if _, err := sqldb.Exec(s); err != nil {
			t.Fatal(err)
		}
	}
	reqs, eps, err := Run(sqldb, 48)
	if err != nil {
		t.Fatal(err)
	}
	if reqs != 1 || eps != 1 {
		t.Fatalf("deleted %d requests %d endpoints, want 1/1", reqs, eps)
	}
	var n int
	_ = sqldb.QueryRow(`SELECT COUNT(*) FROM requests`).Scan(&n)
	if n != 2 {
		t.Fatalf("remaining requests = %d", n)
	}
	var slug string
	if err := sqldb.QueryRow(`SELECT slug FROM endpoints WHERE slug='busy'`).Scan(&slug); err != nil {
		t.Fatal("busy endpoint (has requests) must survive")
	}
	if err := sqldb.QueryRow(`SELECT slug FROM endpoints WHERE slug='gone'`).Scan(&slug); err == nil {
		t.Fatal("expired empty endpoint must be gone")
	}
}

// #11: cutoff uses the stored T-format, so same-day older rows are deleted.
// Reproduces the review case: stored 01:00 T-form vs noon cutoff must compare
// as older (space-form datetime('now') sorted it as NEWER).
func TestCutoffSameDateBoundary(t *testing.T) {
	sqldb, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer sqldb.Close()
	if _, err := sqldb.Exec(`INSERT INTO endpoints(slug) VALUES('b')`); err != nil {
		t.Fatal(err)
	}
	now, _ := time.Parse("2006-01-02T15:04:05Z", "2026-09-05T12:00:00Z")
	old := "2026-09-05T01:00:00.000Z" // 11h older, same date
	fresh := "2026-09-05T11:00:00.000Z"
	if _, err := sqldb.Exec(`INSERT INTO requests(id, endpoint_slug, method, received_at) VALUES('o','b','POST',?),('f','b','POST',?)`, old, fresh); err != nil {
		t.Fatal(err)
	}
	reqs, _, err := runAt(sqldb, 10, now)
	if err != nil {
		t.Fatal(err)
	}
	if reqs != 1 {
		t.Fatalf("deleted %d, want 1 (same-day 11h-old row must go)", reqs)
	}
	var left string
	_ = sqldb.QueryRow(`SELECT id FROM requests`).Scan(&left)
	if left != "f" {
		t.Fatalf("remaining = %q, want f", left)
	}
}
