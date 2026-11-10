// Package originate holds the control-plane rules of click-to-call (spec
// integrations I-37 to I-39): the abuse limits, the destination and caller-ID
// checks. hello-control applies them before a request is queued for a
// hello-sip node; the node enforces the routing rules again when it dials.
package originate

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"
)

// Scopes a limit can be exceeded in (the label of
// hello_integration_rate_limited_total).
const (
	ScopeCredentialMinute = "credential_minute" //nolint:gosec // a limit name, not a credential
	ScopeCredentialDay    = "credential_day"    //nolint:gosec // a limit name, not a credential
	ScopeExtensionSetup   = "extension_setup"
	ScopeExtensionHour    = "extension_hour"
	ScopeDestination      = "destination"
	ScopeGlobal           = "global"
)

// Limits are the click-to-call ceilings (spec I-39).
type Limits struct {
	CredentialPerMinute int
	CredentialPerDay    int
	ExtensionPerHour    int
	DestinationCount    int
	DestinationWindow   time.Duration
	GlobalPerHour       int
	// SetupHold is how long an extension's setup slot is held at most.
	SetupHold time.Duration
}

// DefaultLimits are the spec's defaults: per credential 10 per minute and 200
// per day, per extension 1 call in setup and 30 per hour, per destination 3
// per 10 minutes, 600 per hour in all.
func DefaultLimits() Limits {
	return Limits{
		CredentialPerMinute: 10, CredentialPerDay: 200, ExtensionPerHour: 30,
		DestinationCount: 3, DestinationWindow: 10 * time.Minute, GlobalPerHour: 600,
		SetupHold: 2 * time.Minute,
	}
}

// Backend is the shared counter store; *livestate.Store satisfies it.
type Backend interface {
	// Incr adds one to the counter and returns its value; a new counter
	// expires after ttl.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// AcquireOriginateSetup takes the extension's setup slot for request id;
	// false means another request holds it.
	AcquireOriginateSetup(ctx context.Context, extension, id string, ttl time.Duration) (bool, error)
	ReleaseOriginateSetup(ctx context.Context, extension, id string) error
}

// Denied is a request the limits refuse.
type Denied struct {
	// Scope is the limit that was exceeded.
	Scope string
	// RetryAfter is when the window ends.
	RetryAfter time.Duration
}

func (d *Denied) Error() string { return "rate limited: " + d.Scope }

// Limiter applies Limits over a Backend. Counters are fixed windows: the key
// carries the window's number, so Retry-After is the time left in it.
type Limiter struct {
	B   Backend
	L   Limits
	Now func() time.Time
}

// Check counts one request by credential for extension and destination and
// takes the extension's setup slot for id. It returns a non-nil Denied when a
// limit is exceeded (the slot is then not held); the caller releases the slot
// itself when it cannot queue the request after all.
func (l *Limiter) Check(ctx context.Context, credential, extension, destination, id string) (*Denied, error) {
	now := time.Now
	if l.Now != nil {
		now = l.Now
	}
	ok, err := l.B.AcquireOriginateSetup(ctx, extension, id, l.L.SetupHold)
	if err != nil {
		return nil, err
	}
	if !ok {
		return &Denied{Scope: ScopeExtensionSetup, RetryAfter: 5 * time.Second}, nil
	}
	type counter struct {
		scope  string
		key    string
		window time.Duration
		max    int
	}
	for _, c := range []counter{
		{ScopeCredentialMinute, "cred:" + credential, time.Minute, l.L.CredentialPerMinute},
		{ScopeCredentialDay, "creddy:" + credential, 24 * time.Hour, l.L.CredentialPerDay},
		{ScopeExtensionHour, "ext:" + extension, time.Hour, l.L.ExtensionPerHour},
		{ScopeDestination, "dest:" + destination, l.L.DestinationWindow, l.L.DestinationCount},
		{ScopeGlobal, "all", time.Hour, l.L.GlobalPerHour},
	} {
		if c.max <= 0 {
			continue
		}
		start := now().Truncate(c.window)
		key := fmt.Sprintf("hello:originate:limit:%s:%d", c.key, start.Unix())
		n, err := l.B.Incr(ctx, key, c.window+time.Second)
		if err != nil {
			_ = l.B.ReleaseOriginateSetup(ctx, extension, id)
			return nil, err
		}
		if n > int64(c.max) {
			_ = l.B.ReleaseOriginateSetup(ctx, extension, id)
			retry := max(start.Add(c.window).Sub(now()), time.Second)
			return &Denied{Scope: c.scope, RetryAfter: retry}, nil
		}
	}
	return nil, nil
}

// Release frees the extension's setup slot.
func (l *Limiter) Release(ctx context.Context, extension, id string) error {
	return l.B.ReleaseOriginateSetup(ctx, extension, id)
}

// RetryAfterSeconds is d rounded up to whole seconds, for a Retry-After
// header.
func RetryAfterSeconds(d time.Duration) string {
	return strconv.Itoa(int((d + time.Second - 1) / time.Second))
}

// Memory is a Backend in memory for tests and single-process use.
type Memory struct {
	mu    sync.Mutex
	count map[string]int64
	slots map[string]string
	Now   func() time.Time
}

// NewMemory returns an empty Memory.
func NewMemory() *Memory {
	return &Memory{count: map[string]int64{}, slots: map[string]string{}}
}

// Incr implements Backend; the key's own window number makes expiry moot.
func (m *Memory) Incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.count[key]++
	return m.count[key], nil
}

// AcquireOriginateSetup implements Backend.
func (m *Memory) AcquireOriginateSetup(_ context.Context, extension, id string, _ time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, held := m.slots[extension]; held {
		return false, nil
	}
	m.slots[extension] = id
	return true, nil
}

// ReleaseOriginateSetup implements Backend.
func (m *Memory) ReleaseOriginateSetup(_ context.Context, extension, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.slots[extension] == id {
		delete(m.slots, extension)
	}
	return nil
}
