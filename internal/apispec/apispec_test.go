package apispec

import (
	"testing"

	"github.com/azrtydxb/hello/internal/api"
)

// TestLoadShape fails if the embedded document does not load or a
// non-OpenAPI document does.
func TestLoadShape(t *testing.T) {
	if _, err := Load(api.OpenAPI()); err != nil {
		t.Fatalf("embedded document: %v", err)
	}
	for _, bad := range []string{`nope`, `{"swagger":"2.0","paths":{}}`, `{"openapi":"3.1.0"}`} {
		if _, err := Load([]byte(bad)); err == nil {
			t.Errorf("Load(%s) succeeded", bad)
		}
	}
}
