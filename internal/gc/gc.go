// Package gc deletes expired data. Shared by the `gc` CLI command and the
// server's hourly scheduler so both enforce identical retention semantics.
package gc

import (
	"database/sql"
	"fmt"
	"time"
)

// layout matches the stored strftime('%Y-%m-%dT%H:%M:%fZ','now') values so
// cutoffs compare correctly (review #11: SQLite datetime('now') renders a
// space-separated form that sorts AFTER same-day T-formatted rows).
const layout = "2006-01-02T15:04:05.000Z"

// cutoff returns the retention boundary in stored-row format.
func cutoff(now time.Time, retentionHrs int) string {
	return now.UTC().Add(-time.Duration(retentionHrs) * time.Hour).Format(layout)
}

// Run deletes requests older than retentionHrs and expired, request-less
// endpoints. Returns (requests, endpoints) deleted counts.
func Run(db *sql.DB, retentionHrs int) (reqs, endpoints int64, err error) {
	return runAt(db, retentionHrs, time.Now())
}

func runAt(db *sql.DB, retentionHrs int, now time.Time) (int64, int64, error) {
	cut := cutoff(now, retentionHrs)
	r1, err := db.Exec(`DELETE FROM requests WHERE received_at < ?`, cut)
	if err != nil {
		return 0, 0, fmt.Errorf("gc requests: %w", err)
	}
	reqs, _ := r1.RowsAffected()
	// Endpoint expiry is explicit (ephemeral endpoints), not retention-bound:
	// delete as soon as expires_at passes, in the same canonical format.
	r2, err := db.Exec(`DELETE FROM endpoints WHERE expires_at IS NOT NULL AND expires_at < ?
		AND NOT EXISTS (SELECT 1 FROM requests WHERE endpoint_slug=slug)`, now.UTC().Format(layout))
	if err != nil {
		return reqs, 0, fmt.Errorf("gc endpoints: %w", err)
	}
	endpoints, _ := r2.RowsAffected()
	return reqs, endpoints, nil
}
