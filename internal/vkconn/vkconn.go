// Package vkconn connects Hello's services to Valkey, either a single
// instance or, for high availability, a Sentinel-managed primary (spec
// .procoder/specs/ha.md S-8). Every Valkey client in Hello comes from here.
package vkconn

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Config selects the topology: Sentinels plus Master, or Addr.
type Config struct {
	Addr      string   // single Valkey, host:port
	Sentinels []string // Sentinel host:port list
	Master    string   // Sentinel master set name
}

// Sentinel reports whether c uses Sentinel.
func (c Config) Sentinel() bool { return len(c.Sentinels) > 0 }

// topologyRefresh reconciles the Sentinel client with the sentinels in
// addition to +switch-master events; without it one missed event binds the
// client to a demoted primary until restart.
const topologyRefresh = 5 * time.Second

// retryEvery is the wait between attempts to reach Sentinel at startup.
const retryEvery = 2 * time.Second

// New returns a client for c.
//
// Single mode returns at once, even when Valkey is down: the client redials
// on every command (ForceSingleClient), and the first dial error is returned
// alongside the usable client for the caller to log.
//
// Sentinel mode cannot create a client without asking a sentinel for the
// primary, so New retries until one answers or ctx ends. With three
// sentinels, all being unreachable at startup is a deployment failure, and
// the node stays JOINING (not ready) meanwhile.
func New(ctx context.Context, c Config, log *slog.Logger) (valkey.Client, error) {
	if !c.Sentinel() {
		return valkey.NewClient(valkey.ClientOption{InitAddress: []string{c.Addr}, ForceSingleClient: true})
	}
	opt := valkey.ClientOption{
		InitAddress: c.Sentinels,
		Sentinel:    valkey.SentinelOption{MasterSet: c.Master, TopologyRefreshInterval: topologyRefresh},
	}
	for attempt := 1; ; attempt++ {
		cl, err := valkey.NewClient(opt)
		if err == nil {
			if attempt > 1 {
				log.Info("connected to valkey through sentinel", "master", c.Master, "attempts", attempt)
			}
			return cl, nil
		}
		if attempt == 1 || attempt%15 == 0 {
			log.Warn("waiting for valkey sentinel", "master", c.Master, "sentinels", c.Sentinels, "error", err)
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), err)
		case <-time.After(retryEvery):
		}
	}
}
