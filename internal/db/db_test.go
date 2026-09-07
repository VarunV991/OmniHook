package db

import (
	"database/sql"
	_ "modernc.org/sqlite"
	"path/filepath"
	"testing"
)

func TestOpenUpgradesLegacyDatabase(t *testing.T) {
	p := filepath.Join(t.TempDir(), "legacy.db")
	raw, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	_, err = raw.Exec(`CREATE TABLE endpoints (slug TEXT PRIMARY KEY,name TEXT NOT NULL DEFAULT '',provider TEXT NOT NULL DEFAULT 'generic',secret_ref TEXT NOT NULL DEFAULT '',target_url TEXT NOT NULL DEFAULT '',response_status INTEGER NOT NULL DEFAULT 200,response_body TEXT NOT NULL DEFAULT '{"ok":true}',response_content_type TEXT NOT NULL DEFAULT 'application/json',created_at DATETIME NOT NULL DEFAULT '',expires_at DATETIME); CREATE TABLE requests (id TEXT PRIMARY KEY,endpoint_slug TEXT NOT NULL,method TEXT NOT NULL,path TEXT NOT NULL DEFAULT '/',query TEXT NOT NULL DEFAULT '',headers TEXT NOT NULL DEFAULT '{}',content_type TEXT NOT NULL DEFAULT '',body BLOB,body_size INTEGER NOT NULL DEFAULT 0,truncated INTEGER NOT NULL DEFAULT 0,verify_status TEXT NOT NULL DEFAULT 'SKIPPED',verify_error TEXT NOT NULL DEFAULT '',fix_hint TEXT NOT NULL DEFAULT '',received_at DATETIME NOT NULL DEFAULT ''); CREATE TABLE replays (id TEXT PRIMARY KEY,request_id TEXT NOT NULL,target_url TEXT NOT NULL,status_code INTEGER NOT NULL DEFAULT 0,latency_ms INTEGER NOT NULL DEFAULT 0,error TEXT NOT NULL DEFAULT '',created_at DATETIME NOT NULL DEFAULT '')`)
	if err != nil {
		t.Fatal(err)
	}
	raw.Close()
	db, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err = db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil || n != 2 {
		t.Fatalf("migrations=%d err=%v", n, err)
	}
	var hasVerifiedBy int
	if err = db.QueryRow("SELECT COUNT(*) FROM pragma_table_info('requests') WHERE name='verified_by'").Scan(&hasVerifiedBy); err != nil || hasVerifiedBy != 1 {
		t.Fatalf("verified_by column missing: count=%d err=%v", hasVerifiedBy, err)
	}
	if _, err = db.Exec("INSERT INTO endpoints(slug) VALUES('x')"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec("INSERT INTO requests(id,endpoint_slug,method) VALUES('r','x','POST')"); err != nil {
		t.Fatal(err)
	}
}

func TestOpenRejectsFutureSchema(t *testing.T) {
	p := filepath.Join(t.TempDir(), "future.db")
	raw, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = raw.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at DATETIME NOT NULL); INSERT INTO schema_migrations(version,name,applied_at) VALUES(999,'future','now')`); err != nil {
		t.Fatal(err)
	}
	if err = raw.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = Open(p); err == nil {
		t.Fatal("future schema was accepted")
	}
}
