// Package voice holds what Hello shares with talking-agent, the voice agent
// service (spec voice-agents): the signature on each call to an agent (S-16)
// and the runtime view agents read (S-19). The registry, MCP client and
// runtime handlers of later tasks live here too.
package voice

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/config"
)

// Header names of the INVITE towards talking-agent (spec S-15). Hello sets
// them itself and strips any of them arriving from a caller.
const (
	HeaderTenant       = "X-Hello-Tenant"
	HeaderAgent        = "X-Hello-Agent"
	HeaderCorrelation  = "X-Hello-Correlation-Id"
	HeaderCaller       = "X-Hello-Caller"
	HeaderCalled       = "X-Hello-Called"
	HeaderCallerOrigin = "X-Hello-Caller-Origin"
	HeaderTs           = "X-Hello-Ts"
	HeaderAuth         = "X-Hello-Auth"
)

// The values of X-Hello-Caller-Origin. The origin is sent but is not part of
// the signed string (shared contract 4).
const (
	OriginExtension = "extension"
	OriginTrunk     = "trunk"
)

// Version is the signature scheme in X-Hello-Auth.
const Version = "v1"

// MaxSkew is how far a timestamp may be from the verifier's clock.
const MaxSkew = 30 * time.Second

// ReplayWindow is how long a verifier remembers a correlation id; it covers
// the skew on both sides.
const ReplayWindow = 2 * MaxSkew

// Errors Verify returns; match with errors.Is.
var (
	ErrMalformed    = errors.New("voice: malformed X-Hello-Auth")
	ErrUnknownKey   = errors.New("voice: unknown key id")
	ErrBadSignature = errors.New("voice: signature mismatch")
	ErrSkew         = errors.New("voice: timestamp outside the allowed skew")
	ErrReplay       = errors.New("voice: correlation id replayed")
)

var keyIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// Key is a shared HMAC secret and the id X-Hello-Auth names it by.
type Key struct {
	ID     string
	Secret []byte
}

// LogValue keeps the secret out of logs.
func (k Key) LogValue() slog.Value { return slog.GroupValue(slog.String("id", k.ID)) }

// String never shows the secret.
func (k Key) String() string { return "voice key " + k.ID }

// NewKey makes a key whose id is derived from the secret, so the same secret
// has the same id on Hello and in talking-agent's configuration, whichever
// of HELLO_VOICE_SIP_SECRET and _NEXT holds it.
func NewKey(secret string) Key {
	sum := sha256.Sum256([]byte("hello-voice-key-id|" + secret))
	return Key{ID: hex.EncodeToString(sum[:4]), Secret: []byte(secret)}
}

// Keys returns the key Hello signs with (HELLO_VOICE_SIP_SECRET) and every
// key a verifier accepts (that one, then HELLO_VOICE_SIP_SECRET_NEXT). With
// no secret configured, ok is false.
func Keys(c config.Voice) (sign Key, accept []Key, ok bool) {
	if c.Secret == "" {
		return Key{}, nil, false
	}
	sign = NewKey(c.Secret)
	accept = []Key{sign}
	if c.SecretNext != "" {
		accept = append(accept, NewKey(c.SecretNext))
	}
	return sign, accept, true
}

// signed is the string the HMAC covers: tenant|agent|sip_user|correlation|ts.
func signed(tenant, agent, sipUser, correlation string, ts int64) string {
	return tenant + "|" + agent + "|" + sipUser + "|" + correlation + "|" + strconv.FormatInt(ts, 10)
}

func mac(secret []byte, s string) []byte {
	h := hmac.New(sha256.New, secret)
	h.Write([]byte(s))
	return h.Sum(nil)
}

// Sign returns the X-Hello-Auth value "v1:<key id>:<hex HMAC-SHA256>" over
// tenant|agent|sip_user|correlation|ts (ts in Unix seconds) with key. The
// caller origin header is not signed input. A field holding "|" signs but
// never verifies; names, sip users and tenants cannot hold it.
func Sign(key Key, tenant, agent, sipUser, correlation string, ts time.Time) string {
	return Version + ":" + key.ID + ":" + hex.EncodeToString(mac(key.Secret, signed(tenant, agent, sipUser, correlation, ts.Unix())))
}

// ParseTs reads an X-Hello-Ts value (Unix seconds).
func ParseTs(v string) (time.Time, error) {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return time.Time{}, fmt.Errorf("%w: timestamp", ErrMalformed)
	}
	return time.Unix(n, 0), nil
}

// Verify checks header against the signed fields: the key id selects one of
// keys (two are accepted during rotation), the HMAC must match, and ts must
// be within MaxSkew of now. It does not remember correlation ids; a verifier
// that must refuse replays pairs it with ReplayGuard. Errors never contain
// the header's signature or a secret.
func Verify(keys []Key, header string, now time.Time, tenant, agent, sipUser, correlation string, ts time.Time) error {
	parts := strings.Split(header, ":")
	if len(parts) != 3 || parts[0] != Version || !keyIDRe.MatchString(parts[1]) || len(parts[2]) != sha256.Size*2 {
		return ErrMalformed
	}
	got, err := hex.DecodeString(parts[2])
	if err != nil || parts[2] != strings.ToLower(parts[2]) {
		return ErrMalformed
	}
	for _, f := range []string{tenant, agent, sipUser, correlation} {
		if strings.Contains(f, "|") {
			return ErrMalformed
		}
	}
	var key *Key
	for i := range keys {
		if keys[i].ID == parts[1] {
			key = &keys[i]
			break
		}
	}
	if key == nil {
		return ErrUnknownKey
	}
	if !hmac.Equal(got, mac(key.Secret, signed(tenant, agent, sipUser, correlation, ts.Unix()))) {
		return ErrBadSignature
	}
	if d := now.Sub(ts); d > MaxSkew || d < -MaxSkew {
		return ErrSkew
	}
	return nil
}

// ReplayGuard remembers correlation ids so a verifier refuses a captured
// INVITE replayed inside the skew window. It is safe for concurrent use.
type ReplayGuard struct {
	mu   sync.Mutex
	seen map[string]time.Time
}

// Check records correlation at now and returns ErrReplay if it was already
// recorded within ReplayWindow. Call it only after Verify succeeded.
func (g *ReplayGuard) Check(correlation string, now time.Time) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.seen == nil {
		g.seen = map[string]time.Time{}
	}
	for k, at := range g.seen {
		if now.Sub(at) > ReplayWindow {
			delete(g.seen, k)
		}
	}
	if at, ok := g.seen[correlation]; ok && now.Sub(at) <= ReplayWindow {
		return ErrReplay
	}
	g.seen[correlation] = now
	return nil
}
