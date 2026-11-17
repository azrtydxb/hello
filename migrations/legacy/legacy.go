// Package legacy embeds Hello's frozen goose migrations, 00001 to 00107.
//
// They built every schema before the cutover to momo's migration engine
// (migrations/postgres). internal/migrate replays them with goose only to
// bring a pre-cutover database, or a restored pre-cutover backup, up to
// 00107 before it records momo's legacy_seed as applied (the takeover). Never
// add, edit or renumber a file here: a schema change is a momo migration
// (docs/database.md). The set and the takeover ship for one release after
// the cutover (.procoder/ask/decisions.md).
package legacy

import "embed"

// FS holds the goose SQL files at its root, as goose.NewProvider reads them.
//
//go:embed *.sql
var FS embed.FS

// Latest is the version of the newest frozen migration.
const Latest = 107
