package migrations

import "embed"

// FS contains immutable, versioned database migrations.
//
//go:embed *.sql
var FS embed.FS
