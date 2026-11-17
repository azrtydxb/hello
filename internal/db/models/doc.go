// Package models holds a momo model of every table of Hello's schema. The
// models declare the schema: a change to it edits a model here, then
// `momo generate` (momo_gen.go and the per-model packages, never edited by
// hand) and `momo migrate diff <name>` (the migration in migrations/postgres).
// The model files were first written by `momo generate --from-db` from the
// schema the frozen goose migrations built; they are ordinary source now.
// docs/database.md, "Changing the schema".
package models
