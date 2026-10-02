// Package migrations embeds Hello's PostgreSQL schema migrations (goose SQL
// files) so hello-control carries its own schema.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
