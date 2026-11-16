// Package models holds the momo models of the tables Hello's momo code uses
// (introspect.only in momo.yaml). Every other file is generated: the model
// files by `momo generate --from-db` from a database goose migrated, and
// momo_gen.go with the per-model packages by `momo generate`. Do not edit
// them; regenerate them (docs/database.md, "Regenerating the models").
// TestMomoModelsMatchSchema fails while they differ from the goose schema.
package models
