package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/azrtydxb/hello/internal/api"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/azrtydxb/hello/internal/mcp"
	"github.com/azrtydxb/hello/internal/oauth"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// oauthPruneLeaseKey makes one replica prune expired OAuth rows a day.
	oauthPruneLeaseKey = "hello:oauth:lease:prune"
	// oauthRetention is how long expired or revoked OAuth rows are kept.
	oauthRetention = 7 * 24 * time.Hour
)

// newAuthServer returns the OAuth authorization server, or nil without
// HELLO_PUBLIC_URL (spec ai-external-access S-7).
func newAuthServer(cfg config.AI, st *store.Store, vk *api.LazyValkey, reg prometheus.Registerer, log *slog.Logger) (*oauth.Server, error) {
	if !cfg.Enabled() {
		return nil, nil
	}
	return oauth.New(oauth.Options{
		Store: st, PublicURL: cfg.PublicURL,
		AccessTTL: cfg.AccessTTL, RefreshIdle: cfg.RefreshIdle, RefreshMax: cfg.RefreshMax,
		DCR: cfg.DCR, CIMDAllowPrivate: cfg.CIMDAllowPrivate,
		Limiter: oauth.NewLimiter(vk.Client, log),
		Metrics: oauth.NewMetrics(reg),
		Log:     log,
	})
}

// composeApp is hello-control's HTTP surface: /api/v1 always; with
// HELLO_PUBLIC_URL also the authorization server's well-known documents
// and /oauth/*, and /mcp. Without it those paths answer 404 as before.
func composeApp(cfg config.AI, apiHandler http.Handler, as *oauth.Server, st *store.Store, log *slog.Logger) (http.Handler, error) {
	if as == nil {
		return apiHandler, nil
	}
	spec, err := apispec.Load(api.OpenAPI())
	if err != nil {
		return nil, fmt.Errorf("openapi.json: %w", err)
	}
	_, mcpResource := as.Resources()
	mcpHandler, err := mcp.New(mcp.Options{
		API: apiHandler, Spec: spec, PublicURL: cfg.PublicURL, AllowedOrigins: cfg.MCPAllowedOrigins,
		Lookup: st, MetadataURL: as.MetadataURL(mcpResource), Log: log,
	})
	if err != nil {
		return nil, err
	}
	oh := as.Handler()
	mux := http.NewServeMux()
	mux.Handle("/", apiHandler)
	mux.Handle("/mcp", mcpHandler)
	mux.Handle("/oauth/", oh)
	mux.Handle("/.well-known/oauth-authorization-server", oh)
	mux.Handle("/.well-known/oauth-protected-resource/", oh)
	return mux, nil
}

// pruneOAuth deletes expired and revoked OAuth rows and stale dynamically
// registered clients once a day on the replica holding the lease (each
// replica while Valkey is down; the deletes are idempotent).
func pruneOAuth(ctx context.Context, as *oauth.Server, l lease, node string, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		held, err := l.AcquireLease(ctx, oauthPruneLeaseKey, node, pruneEveryDay-time.Hour)
		if err != nil {
			log.Warn("oauth pruner: lease unavailable; pruning here", "error", err)
		}
		if held || err != nil {
			if n, err := as.Prune(ctx, oauthRetention); err != nil {
				log.Warn("prune oauth rows", "error", err)
			} else if n > 0 {
				log.Info("pruned oauth rows", "count", n)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
