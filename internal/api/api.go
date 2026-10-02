// Package api is hello-control's versioned management API (spec §24).
package api

import (
	_ "embed"
	"encoding/json"
	"net/http"

	"github.com/azrtydxb/hello/internal/version"
)

//go:embed openapi.json
var openAPI []byte

// Handler returns the /api/v1 routes.
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/version", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"version": version.Version,
			"commit":  version.Commit,
			// Configuration revisions arrive with Phase 3; until then the
			// schema_info row stays at 0, so this is reported as 0.
			"configRevision": 0,
		})
	})
	mux.HandleFunc("GET /api/v1/openapi.json", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(openAPI)
	})
	return mux
}
