// REGISTER attempts: a short, capped history of how hello-sip answered each
// enabled device's REGISTER requests, for the Diagnostics view's "why is it
// not registered". hello-sip writes it; hello-control reads it.
package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
)

// RegisterAttempt is one REGISTER and the final response a node sent to it.
type RegisterAttempt struct {
	At     time.Time `json:"at"`
	Device string    `json:"device"` // the SIP username of the AOR registered
	// Source is the client's address (ip:port) and IP the client IP the
	// failed-auth throttle counts against.
	Source string `json:"source"`
	IP     string `json:"ip"`
	Node   string `json:"node"`
	// UserAgent is the request's User-Agent header, "" when absent.
	UserAgent string `json:"userAgent,omitempty"`
	// Credentials reports whether the request carried an Authorization
	// header (a challenge answer) rather than being an unauthenticated try.
	Credentials bool   `json:"credentials"`
	Code        int    `json:"code"`
	Reason      string `json:"reason"`
	// Stale is a 401 whose challenge carried stale=true.
	Stale bool `json:"stale,omitempty"`
}

const regAttemptsPrefix = "hello:regattempts:"

// RegisterAttemptsCap is how many attempts are kept per device, newest
// first; RegisterAttemptsTTL is how long a device's history outlives its
// last attempt.
const (
	RegisterAttemptsCap = 20
	RegisterAttemptsTTL = time.Hour
)

// regAttemptScript pushes ARGV[1] to the head of KEYS[1], trims the list to
// ARGV[2] entries and sets its expiry to ARGV[3] ms, in one step, so the
// list is never left unbounded or without a TTL.
var regAttemptScript = valkey.NewLuaScript(`
redis.call('LPUSH', KEYS[1], ARGV[1])
redis.call('LTRIM', KEYS[1], 0, tonumber(ARGV[2]) - 1)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return 1`)

// RecordRegisterAttempt appends a to its device's capped history.
func (s *Store) RecordRegisterAttempt(ctx context.Context, a RegisterAttempt) error {
	v, err := json.Marshal(a)
	if err != nil {
		return err
	}
	err = regAttemptScript.Exec(ctx, s.c, []string{regAttemptsPrefix + a.Device}, []string{
		string(v), strconv.Itoa(RegisterAttemptsCap), strconv.FormatInt(RegisterAttemptsTTL.Milliseconds(), 10),
	}).Error()
	if err != nil {
		return fmt.Errorf("livestate: record register attempt: %w", err)
	}
	return nil
}

// RegisterAttempts returns a device's recorded attempts, newest first.
func (s *Store) RegisterAttempts(ctx context.Context, device string) ([]RegisterAttempt, error) {
	vs, err := s.c.Do(ctx, s.c.B().Lrange().Key(regAttemptsPrefix+device).Start(0).Stop(RegisterAttemptsCap-1).Build()).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("livestate: register attempts: %w", err)
	}
	out := make([]RegisterAttempt, 0, len(vs))
	for _, v := range vs {
		var a RegisterAttempt
		if err := json.Unmarshal([]byte(v), &a); err != nil {
			return nil, fmt.Errorf("livestate: decode register attempt: %w", err)
		}
		out = append(out, a)
	}
	return out, nil
}

// AuthFailPrefix is hello-sip's failed-auth throttle keyspace
// (sip.ValkeyThrottle): one counter per client IP, expiring when its
// window ends.
const AuthFailPrefix = "hello:authfail:"

// AuthFailure is one source IP's failed-auth counter.
type AuthFailure struct {
	IP           string    `json:"ip"`
	Failures     int64     `json:"failures"`
	WindowEndsAt time.Time `json:"windowEndsAt"`
}

// AuthFailures reads ip's failed-auth counter; ok is false when it has none.
func (s *Store) AuthFailures(ctx context.Context, ip string) (AuthFailure, bool, error) {
	return s.authFailure(ctx, AuthFailPrefix+ip)
}

func (s *Store) authFailure(ctx context.Context, key string) (AuthFailure, bool, error) {
	n, err := s.c.Do(ctx, s.c.B().Get().Key(key).Build()).AsInt64()
	if valkey.IsValkeyNil(err) {
		return AuthFailure{}, false, nil
	}
	if err != nil {
		return AuthFailure{}, false, fmt.Errorf("livestate: authfail get: %w", err)
	}
	ms, err := s.c.Do(ctx, s.c.B().Pttl().Key(key).Build()).AsInt64()
	if err != nil {
		return AuthFailure{}, false, fmt.Errorf("livestate: authfail ttl: %w", err)
	}
	if ms == -2 { // expired between the two reads
		return AuthFailure{}, false, nil
	}
	f := AuthFailure{IP: strings.TrimPrefix(key, AuthFailPrefix), Failures: n}
	if ms > 0 {
		f.WindowEndsAt = time.Now().Add(time.Duration(ms) * time.Millisecond).Truncate(time.Second)
	}
	return f, true, nil
}

// AllAuthFailures lists every source IP with a failed-auth counter.
func (s *Store) AllAuthFailures(ctx context.Context) ([]AuthFailure, error) {
	keys, err := s.scan(ctx, AuthFailPrefix+"*")
	if err != nil {
		return nil, err
	}
	out := make([]AuthFailure, 0, len(keys))
	for _, k := range keys {
		f, ok, err := s.authFailure(ctx, k)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, f)
		}
	}
	return out, nil
}

// ClearAuthFailures deletes ip's failed-auth counter, unblocking it.
func (s *Store) ClearAuthFailures(ctx context.Context, ip string) error {
	if err := s.c.Do(ctx, s.c.B().Del().Key(AuthFailPrefix+ip).Build()).Error(); err != nil {
		return fmt.Errorf("livestate: authfail del: %w", err)
	}
	return nil
}
