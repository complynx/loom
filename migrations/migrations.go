package migrations

import "embed"

// Files contains the versioned schema migrations.
//
//go:embed *.sql
var Files embed.FS
