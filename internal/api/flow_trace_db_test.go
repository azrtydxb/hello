package api

import (
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestTraceEndpoint runs traceFlow against the real handler and database: an
// extension calling another is a 200 with its ring step and the two branches
// the configuration decides, a viewer may use it, and bad input is a 400.
func TestTraceEndpoint(t *testing.T) {
	e := newPBXEnv(t, newMemObjects())
	admin := e.login()
	viewer, _ := e.asRole("victor", auth.RoleViewer)

	for _, c := range []*client{admin, viewer} {
		out := c.must(http.StatusOK, "GET", "/api/v1/flow/trace?from=101&number=102", nil).json(t)
		if out["outcome"] != "internal" {
			t.Fatalf("outcome %v", out["outcome"])
		}
		steps, _ := out["steps"].([]any)
		if len(steps) < 2 {
			t.Fatalf("steps %v", steps)
		}
	}
	admin.must(http.StatusBadRequest, "GET", "/api/v1/flow/trace?from=nope&number=102", nil)
}
