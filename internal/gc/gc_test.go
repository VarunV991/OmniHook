package gc

import (
	"path/filepath"
	"testing"

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
	_, _ = sqldb.Exec(`UPDATE endpoints SET expires_at=datetime('now','-1 hour') WHERE slug='gone'`)
	_, _ = sqldb.Exec(`UPDATE endpoints SET expires_at=datetime('now','-1 hour') WHERE slug='busy'`)
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
