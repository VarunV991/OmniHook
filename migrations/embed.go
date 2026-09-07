package migrations

import "embed"

// Files contains the canonical numbered migrations embedded in every binary.
//
//go:embed *.sql
var Files embed.FS
