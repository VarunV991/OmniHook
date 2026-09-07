package db

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/you/omnihook/migrations"
	_ "modernc.org/sqlite"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

func Open(dbPath string) (*sql.DB, error) {
	if dir := filepath.Dir(dbPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
	}
	f, err := os.OpenFile(dbPath, os.O_CREATE, 0600)
	if err != nil {
		return nil, fmt.Errorf("create database file: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, fmt.Errorf("close database file: %w", err)
	}
	if err := os.Chmod(dbPath, 0600); err != nil {
		return nil, fmt.Errorf("restrict database permissions: %w", err)
	}
	dsn := "file:" + dbPath + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)"
	sqldb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (*sql.DB, error) { _ = sqldb.Close(); return nil, e }
	if err := sqldb.Ping(); err != nil {
		return fail(fmt.Errorf("open database: %w", err))
	}
	if err := applyMigrations(sqldb); err != nil {
		return fail(err)
	}
	for _, companion := range []string{dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Chmod(companion, 0600); err != nil && !os.IsNotExist(err) {
			return fail(fmt.Errorf("restrict companion permissions: %w", err))
		}
	}
	return sqldb, nil
}

type migration struct {
	version   int
	name, sql string
}

func loadMigrations() ([]migration, error) {
	es, err := migrations.Files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range es {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := strconv.Atoi(strings.SplitN(e.Name(), "_", 2)[0])
		if err != nil || v < 1 {
			return nil, fmt.Errorf("invalid migration filename %q", e.Name())
		}
		b, err := migrations.Files.ReadFile(e.Name())
		if err != nil {
			return nil, err
		}
		out = append(out, migration{v, e.Name(), string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}
func applyMigrations(db *sql.DB) error {
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY,name TEXT NOT NULL,applied_at DATETIME NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')))`); err != nil {
		return fmt.Errorf("create migration table: %w", err)
	}
	ms, err := loadMigrations()
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	var maxApplied int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&maxApplied); err != nil {
		return fmt.Errorf("read migration version: %w", err)
	}
	if len(ms) > 0 && maxApplied > ms[len(ms)-1].version {
		return fmt.Errorf("database schema version %d is newer than supported version %d", maxApplied, ms[len(ms)-1].version)
	}
	for _, m := range ms {
		var n int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM schema_migrations WHERE version=?`, m.version).Scan(&n); err != nil {
			return fmt.Errorf("check migration %03d: %w", m.version, err)
		}
		if n > 0 {
			continue
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %03d: %w", m.version, err)
		}
		if m.version == 2 {
			var hasVerifiedBy bool
			rows, qerr := tx.QueryContext(ctx, `PRAGMA table_info(requests)`)
			if qerr != nil {
				_ = tx.Rollback()
				return fmt.Errorf("inspect migration %03d (%s): %w", m.version, m.name, qerr)
			}
			for rows.Next() {
				var cid int
				var name, typ string
				var notnull, pk int
				var dflt any
				if qerr = rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); qerr != nil {
					_ = rows.Close()
					_ = tx.Rollback()
					return fmt.Errorf("inspect migration %03d (%s): %w", m.version, m.name, qerr)
				}
				if strings.EqualFold(name, "verified_by") {
					hasVerifiedBy = true
				}
			}
			if qerr = rows.Err(); qerr != nil {
				_ = rows.Close()
				_ = tx.Rollback()
				return fmt.Errorf("inspect migration %03d (%s): %w", m.version, m.name, qerr)
			}
			_ = rows.Close()
			if !hasVerifiedBy {
				if _, qerr = tx.ExecContext(ctx, m.sql); qerr != nil {
					_ = tx.Rollback()
					return fmt.Errorf("apply migration %03d (%s): %w", m.version, m.name, qerr)
				}
			}
		} else if _, err = tx.ExecContext(ctx, m.sql); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %03d (%s): %w", m.version, m.name, err)
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO schema_migrations(version,name) VALUES(?,?)`, m.version, m.name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %03d: %w", m.version, err)
		}
		if err = tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %03d: %w", m.version, err)
		}
	}
	return nil
}
