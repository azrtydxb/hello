package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVersionAndOpenAPI(t *testing.T) {
	h := Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/version", nil))
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("version = %d %q: %v", rec.Code, rec.Body, err)
	}
	for _, k := range []string{"version", "commit", "configRevision"} {
		if _, ok := v[k]; !ok {
			t.Fatalf("version response missing %q: %v", k, v)
		}
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/openapi.json", nil))
	var doc struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatalf("openapi.json is not JSON: %v", err)
	}
	if !strings.HasPrefix(doc.OpenAPI, "3.") {
		t.Fatalf("openapi version = %q, want 3.x", doc.OpenAPI)
	}
	if _, ok := doc.Paths["/api/v1/version"]; !ok {
		t.Fatal("openapi.json does not describe /api/v1/version")
	}
}
