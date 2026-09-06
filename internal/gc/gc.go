// Package gc deletes expired data. Shared by the `gc` CLI command and the
// server's hourly scheduler so both enforce identical retention semantics.
package gc

import (
	"database/sql"
	"fmt"
)

// Run deletes requests older than retentionHrs and expired, request-less
// endpoints. Returns (requests, endpoints) deleted counts.
func Run(db *sql.DB, retentionHrs int) (reqs, endpoints int64, err error) {
	r1, err := db.Exec(`DELETE FROM requests WHERE received_at < datetime('now', ?)`,
		fmt.Sprintf("-%d hours", retentionHrs))
	if err != nil {
		return 0, 0, fmt.Errorf("gc requests: %w", err)
	}
	reqs, _ = r1.RowsAffected()
	r2, err := db.Exec(`DELETE FROM endpoints WHERE expires_at IS NOT NULL AND expires_at < datetime('now')
		AND NOT EXISTS (SELECT 1 FROM requests WHERE endpoint_slug=slug)`)
	if err != nil {
		return reqs, 0, fmt.Errorf("gc endpoints: %w", err)
	}
	endpoints, _ = r2.RowsAffected()
	return reqs, endpoints, nil
}
