package ai

import (
	"context"
	"net"
	"net/netip"
	"time"
)

// Test hooks for the external ai_test package.

// WithTiming shortens the background loops.
func WithTiming(o Options, heartbeat, stale, tick, prune time.Duration) Options {
	o.timing = timing{heartbeat: heartbeat, stale: stale, tick: tick, prune: prune}
	return o
}

// WithNet replaces DNS and the dialer under the privacy check.
func WithNet(o Options, r func(context.Context, string) ([]netip.Addr, error), dial func(context.Context, string, string) (net.Conn, error)) Options {
	o.resolver, o.dial = r, dial
	return o
}

// Beat and FailStale run one heartbeat and one stale-task check.
func (s *Service) Beat(ctx context.Context)      { s.beat(ctx) }
func (s *Service) FailStale(ctx context.Context) { s.failStale(ctx) }

// StripAnswer is stripAnswer.
var StripAnswer = stripAnswer
