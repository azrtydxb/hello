package ldapdir

import (
	"context"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Bind failure limit (spec S-24): 5 per 10 minutes per DN and per source.
const (
	BindFailLimit  = 5
	BindFailWindow = 10 * time.Minute
	limiterTimeout = 50 * time.Millisecond
	limiterPrefix  = "hello:ldap:bindfail:"
	maxMemKeys     = 10000
)

// Limiter counts failed binds. A key is blocked once it has BindFailLimit
// failures inside BindFailWindow.
type Limiter interface {
	Blocked(ctx context.Context, key string) bool
	Fail(ctx context.Context, key string)
}

// NewMemoryLimiter returns a limiter that counts in this process.
func NewMemoryLimiter() Limiter { return &memLimiter{m: map[string][]time.Time{}, now: time.Now} }

type memLimiter struct {
	mu  sync.Mutex
	m   map[string][]time.Time
	now func() time.Time
}

func (l *memLimiter) recent(key string) []time.Time {
	cut := l.now().Add(-BindFailWindow)
	ts := l.m[key]
	i := 0
	for i < len(ts) && !ts[i].After(cut) {
		i++
	}
	ts = ts[i:]
	if len(ts) == 0 {
		delete(l.m, key)
		return nil
	}
	l.m[key] = ts
	return ts
}

func (l *memLimiter) Blocked(_ context.Context, key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.recent(key)) >= BindFailLimit
}

func (l *memLimiter) Fail(_ context.Context, key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) >= maxMemKeys {
		for k := range l.m {
			l.recent(k) // drops what has expired
		}
		if len(l.m) >= maxMemKeys {
			return // a flood of distinct keys: the existing ones keep counting
		}
	}
	l.m[key] = append(l.recent(key), l.now())
}

// valkeyLimiter counts in Valkey, shared by every replica, and falls back
// to memory when Valkey does not answer within 50 ms (it never fails open).
type valkeyLimiter struct {
	client func() valkey.Client
	mem    Limiter
}

// NewValkeyLimiter returns a limiter on the client get returns at each call
// (nil while Valkey is connecting: memory only).
func NewValkeyLimiter(get func() valkey.Client) Limiter {
	return &valkeyLimiter{client: get, mem: NewMemoryLimiter()}
}

func (l *valkeyLimiter) Blocked(ctx context.Context, key string) bool {
	if c := l.client(); c != nil {
		ctx, cancel := context.WithTimeout(ctx, limiterTimeout)
		defer cancel()
		n, err := c.Do(ctx, c.B().Get().Key(limiterPrefix+key).Build()).AsInt64()
		switch {
		case err == nil:
			return n >= BindFailLimit
		case valkey.IsValkeyNil(err):
			return l.mem.Blocked(ctx, key)
		}
	}
	return l.mem.Blocked(ctx, key)
}

func (l *valkeyLimiter) Fail(ctx context.Context, key string) {
	l.mem.Fail(ctx, key) // always, so a Valkey outage between calls loses nothing
	c := l.client()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, limiterTimeout)
	defer cancel()
	k := limiterPrefix + key
	if err := c.Do(ctx, c.B().Incr().Key(k).Build()).Error(); err != nil {
		return
	}
	_ = c.Do(ctx, c.B().Expire().Key(k).Seconds(int64(BindFailWindow/time.Second)).Nx().Build()).Error()
}
