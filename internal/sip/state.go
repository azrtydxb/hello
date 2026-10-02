package sip

import (
	"context"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/valkey-io/valkey-go"
)

// State is the live state the SIP node writes; *livestate.Store satisfies it.
type State interface {
	PutBinding(ctx context.Context, b livestate.Binding) error
	DeleteBinding(ctx context.Context, aor, contactURI string) error
	DeleteAOR(ctx context.Context, aor string) error
	Bindings(ctx context.Context, aor string) ([]livestate.Binding, error)
	AllBindings(ctx context.Context) ([]livestate.Binding, error)
	PutCall(ctx context.Context, c livestate.Call, ttl time.Duration) error
	DeleteCall(ctx context.Context, id string) error
}

// Snapshots yields the current configuration snapshot (nil before the first
// load); *snapshot.Watcher satisfies it.
type Snapshots interface {
	Current() *snapshot.Snapshot
}

// CDRSink accepts finished call records without blocking; *cdr.Writer
// satisfies it.
type CDRSink interface {
	Enqueue(r cdr.Record) bool
}

// Throttle counts failed authentications per source IP across nodes.
type Throttle interface {
	Failures(ctx context.Context, ip string) (int64, error)
	RecordFailure(ctx context.Context, ip string) error
}

// ValkeyThrottle keeps the counters in Valkey at hello:authfail:{ip}; the
// window starts at the first failure (INCR, then EXPIRE NX).
type ValkeyThrottle struct {
	Client valkey.Client
	Window time.Duration
}

const authFailPrefix = "hello:authfail:"

// Failures returns the failures recorded for ip in the current window.
func (t ValkeyThrottle) Failures(ctx context.Context, ip string) (int64, error) {
	n, err := t.Client.Do(ctx, t.Client.B().Get().Key(authFailPrefix+ip).Build()).AsInt64()
	if valkey.IsValkeyNil(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("authfail get: %w", err)
	}
	return n, nil
}

// RecordFailure counts one failure for ip.
func (t ValkeyThrottle) RecordFailure(ctx context.Context, ip string) error {
	key := authFailPrefix + ip
	secs := max(int64(t.Window/time.Second), 1)
	for _, r := range t.Client.DoMulti(ctx,
		t.Client.B().Incr().Key(key).Build(),
		t.Client.B().Expire().Key(key).Seconds(secs).Nx().Build(),
	) {
		if err := r.Error(); err != nil {
			return fmt.Errorf("authfail incr: %w", err)
		}
	}
	return nil
}
