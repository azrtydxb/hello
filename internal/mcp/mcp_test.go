package mcp

import (
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/apispec"
)

// TestNewRequiresAPIAndSpec fails if New builds a server with nothing to
// replay through or no document to derive tools from.
func TestNewRequiresAPIAndSpec(t *testing.T) {
	spec, err := apispec.Load(api.OpenAPI())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Spec: spec}); err == nil {
		t.Fatal("New without API succeeded")
	}
	if _, err := New(Options{API: http.NotFoundHandler()}); err == nil {
		t.Fatal("New without Spec succeeded")
	}
	if h, err := New(Options{API: http.NotFoundHandler(), Spec: spec}); err != nil || h == nil {
		t.Fatalf("New = %v, %v", h, err)
	}
}
