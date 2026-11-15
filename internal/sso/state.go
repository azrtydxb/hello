package sso

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// StateTTL is how long a sign-in or consent may take (I-27).
const StateTTL = 10 * time.Minute

// State is what a sign-in or a directory consent keeps server-side between
// the redirect to the IdP and the callback. It is single use.
type State struct {
	// Kind is login, link or consent.
	Kind     string `json:"kind"`
	Provider string `json:"provider,omitempty"`
	Nonce    string `json:"nonce,omitempty"`
	Verifier string `json:"verifier"`
	// Binding is the SHA-256 of the hello_oidc cookie value that ties the
	// callback to the browser that started.
	Binding string `json:"binding"`
	// UserID is the signed-in user of a link; ConnectorID and Actor the
	// connector and administrator of a consent.
	UserID      int64  `json:"userId,omitempty"`
	ConnectorID int64  `json:"connectorId,omitempty"`
	Actor       string `json:"actor,omitempty"`
	// Next is the console path to return to after a login.
	Next string `json:"next,omitempty"`
}

// State kinds.
const (
	KindLogin   = "login"
	KindLink    = "link"
	KindConsent = "consent"
)

// StateStore keeps states. Take removes and returns one atomically, so a
// state is used at most once (T-6).
type StateStore interface {
	Put(ctx context.Context, state string, s State, ttl time.Duration) error
	Take(ctx context.Context, state string) (State, error)
}

func stateKey(state string) string {
	h := sha256.Sum256([]byte(state))
	return "hello:sso:state:" + hex.EncodeToString(h[:])
}

// ValkeyStates is the StateStore in Valkey. Without a client it fails, and
// SSO is then unavailable rather than kept in one replica's memory.
type ValkeyStates struct{ Client func() valkey.Client }

// Put implements StateStore.
func (v ValkeyStates) Put(ctx context.Context, state string, s State, ttl time.Duration) error {
	c := v.client()
	if c == nil {
		return ErrUnavailable
	}
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	ok, err := c.Do(ctx, c.B().Set().Key(stateKey(state)).Value(string(b)).Nx().Px(ttl).Build()).AsBool()
	if err != nil && !valkey.IsValkeyNil(err) {
		return err
	}
	if !ok {
		return errors.New("sso: state collision")
	}
	return nil
}

// Take implements StateStore.
func (v ValkeyStates) Take(ctx context.Context, state string) (State, error) {
	var s State
	c := v.client()
	if c == nil {
		return s, ErrUnavailable
	}
	raw, err := c.Do(ctx, c.B().Getdel().Key(stateKey(state)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return s, ErrState
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		return s, ErrState
	}
	return s, nil
}

func (v ValkeyStates) client() valkey.Client {
	if v.Client == nil {
		return nil
	}
	return v.Client()
}

// MemStates is a StateStore in memory (tests).
type MemStates struct {
	mu  sync.Mutex
	m   map[string]State
	exp map[string]time.Time
	Now func() time.Time
}

func (m *MemStates) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Put implements StateStore.
func (m *MemStates) Put(_ context.Context, state string, s State, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.m == nil {
		m.m, m.exp = map[string]State{}, map[string]time.Time{}
	}
	k := stateKey(state)
	if _, dup := m.m[k]; dup {
		return errors.New("sso: state collision")
	}
	m.m[k], m.exp[k] = s, m.now().Add(ttl)
	return nil
}

// Take implements StateStore.
func (m *MemStates) Take(_ context.Context, state string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := stateKey(state)
	s, ok := m.m[k]
	exp := m.exp[k]
	delete(m.m, k)
	delete(m.exp, k)
	if !ok || !m.now().Before(exp) {
		return State{}, ErrState
	}
	return s, nil
}

// Throttle counts events per key in a fixed window.
type Throttle interface {
	// Count returns how many hits key has in its window (0 when unknown).
	Count(ctx context.Context, key string) (int, error)
	// Hit adds one hit to key, starting its window when it is the first,
	// and returns the new count.
	Hit(ctx context.Context, key string, window time.Duration) (int, error)
}

// ValkeyThrottle is the Throttle in Valkey (INCR with an expiry), so every
// replica sees the same counts. Without Valkey it fails closed.
type ValkeyThrottle struct{ Client func() valkey.Client }

func throttleKey(k string) string { return "hello:sso:rl:" + k }

// Count implements Throttle.
func (v ValkeyThrottle) Count(ctx context.Context, key string) (int, error) {
	c := v.client()
	if c == nil {
		return 0, ErrUnavailable
	}
	n, err := c.Do(ctx, c.B().Get().Key(throttleKey(key)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	i, err := strconv.Atoi(n)
	if err != nil {
		return 0, nil
	}
	return i, nil
}

// Hit implements Throttle.
func (v ValkeyThrottle) Hit(ctx context.Context, key string, window time.Duration) (int, error) {
	c := v.client()
	if c == nil {
		return 0, ErrUnavailable
	}
	k := throttleKey(key)
	res := c.DoMulti(ctx,
		c.B().Incr().Key(k).Build(),
		c.B().Pexpire().Key(k).Milliseconds(window.Milliseconds()).Nx().Build())
	n, err := res[0].AsInt64()
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

func (v ValkeyThrottle) client() valkey.Client {
	if v.Client == nil {
		return nil
	}
	return v.Client()
}

// MemThrottle is a Throttle in memory (tests).
type MemThrottle struct {
	mu  sync.Mutex
	n   map[string]int
	exp map[string]time.Time
	Now func() time.Time
}

func (m *MemThrottle) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Count implements Throttle.
func (m *MemThrottle) Count(_ context.Context, key string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.now().Before(m.exp[key]) {
		return 0, nil
	}
	return m.n[key], nil
}

// Hit implements Throttle.
func (m *MemThrottle) Hit(_ context.Context, key string, window time.Duration) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.n == nil {
		m.n, m.exp = map[string]int{}, map[string]time.Time{}
	}
	if !m.now().Before(m.exp[key]) {
		m.n[key], m.exp[key] = 0, m.now().Add(window)
	}
	m.n[key]++
	return m.n[key], nil
}
