package flow

import (
	"bytes"
	"encoding/json"
	"os"
	"testing"
)

// catalogueSnapshot is the catalogue as GET /api/v1/flow/catalogue answers
// it, kept next to the canvas code. The web tests read it to check that the
// canvas can create, edit and delete every stored node kind and wire every
// output the catalogue allows (web/src/pages/flow/coverage.test.ts).
const catalogueSnapshot = "../../web/src/pages/flow/catalogue.snapshot.json"

// TestCatalogueSnapshot fails when the catalogue changed and the snapshot the
// canvas tests read did not. Run with -update to rewrite it, then make the
// canvas cover what was added.
func TestCatalogueSnapshot(t *testing.T) {
	got, err := json.MarshalIndent(Get(), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile(catalogueSnapshot, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(catalogueSnapshot)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/flow -run TestCatalogueSnapshot -update)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("the catalogue changed: run go test ./internal/flow -run TestCatalogueSnapshot -update, and cover the change on the canvas (web/src/pages/flow/objects.ts)")
	}
}
