package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

// TestComposeWithoutPublicURL fails if any OAuth or MCP path answers other
// than 404 without HELLO_PUBLIC_URL, or if they are not served with it.
func TestComposeWithoutPublicURL(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	st := store.New(nil)
	vk := api.NewLazyValkey(false)
	apiH := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v1/") {
			w.WriteHeader(http.StatusTeapot)
			return
		}
		http.NotFound(w, r)
	})
	paths := []struct{ method, path string }{
		{"GET", "/.well-known/oauth-authorization-server"},
		{"GET", "/.well-known/oauth-protected-resource/mcp"},
		{"GET", "/.well-known/oauth-protected-resource/api/v1"},
		{"GET", "/oauth/authorize"},
		{"POST", "/oauth/token"},
		{"POST", "/oauth/revoke"},
		{"POST", "/oauth/register"},
		{"POST", "/mcp"},
	}
	for _, enabled := range []bool{false, true} {
		cfg := config.AI{}
		if enabled {
			cfg = config.AI{PublicURL: "https://hello.example", DCR: true}
		}
		reg := prometheus.NewRegistry()
		as, err := newAuthServer(cfg, st, vk, reg, log)
		if err != nil {
			t.Fatal(err)
		}
		app, err := composeApp(cfg, apiH, as, st, reg, log)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range paths {
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, httptest.NewRequest(p.method, p.path, strings.NewReader("")))
			if (rec.Code == http.StatusNotFound) == enabled {
				t.Errorf("public URL %v: %s %s = %d", enabled, p.method, p.path, rec.Code)
			}
		}
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/version", nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("public URL %v: /api/v1 not served by the API: %d", enabled, rec.Code)
		}
	}
}

func TestProposalSource(t *testing.T) {
	for in, want := range map[string]string{"assistant": "assistant", "finding:cdr_failures": "finding", "finding": "finding"} {
		if got := proposalSource(in); got != want {
			t.Errorf("proposalSource(%q) = %q, want %q", in, got, want)
		}
	}
}
