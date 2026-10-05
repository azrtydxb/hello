// Package livestate is the contract for Hello's shared ephemeral state in
// Valkey: registration bindings and active calls. hello-sip writes it;
// hello-control reads it for the live API. Both sides use only this package,
// so the key layout and record shapes live in one place.
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

// Binding is one registered contact of an AOR (spec §8).
type Binding struct {
	AOR          string    `json:"aor"`        // sip:<username>@<domain>
	Extension    string    `json:"extension"`  // extension number the device belongs to
	Device       string    `json:"device"`     // device SIP username
	ContactURI   string    `json:"contactUri"` // as sent in Contact
	Source       string    `json:"source"`     // ip:port the REGISTER came from
	Transport    string    `json:"transport"`  // udp
	UserAgent    string    `json:"userAgent"`
	Path         []string  `json:"path,omitempty"`
	ReceivedNode string    `json:"receivedNode"` // node ID that handled the REGISTER
	Expires      time.Time `json:"expires"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// Call is one active call, published by the node that owns its dialogs.
type Call struct {
	ID         string    `json:"id"` // correlation ID
	SIPCallID  string    `json:"sipCallId"`
	From       string    `json:"from"`  // caller extension number
	To         string    `json:"to"`    // dialled number
	State      string    `json:"state"` // ringing | connected
	Node       string    `json:"node"`
	Media      string    `json:"media"` // direct
	StartedAt  time.Time `json:"startedAt"`
	AnsweredAt time.Time `json:"answeredAt,omitzero"`
	// HA is the call's in-call HA state (incall-ha S-6): "owned" while the
	// node that set the call up still carries it, "taken-over" once a
	// surviving node re-homed it after its owner died. Records written by
	// nodes predating the field read as "" and are reported as owned.
	HA string `json:"ha,omitempty"`
}

// The live call's in-call HA states (Call.HA).
const (
	HAOwned     = "owned"
	HATakenOver = "taken-over"
)

const (
	regPrefix  = "hello:reg:"
	callPrefix = "hello:call:"
)

// Store reads and writes live state. Every method honours ctx's deadline.
type Store struct {
	c valkey.Client
}

// New wraps a Valkey client.
func New(c valkey.Client) *Store { return &Store{c: c} }

// bindingKey is the hash field for one contact of an AOR; the contact URI
// identifies the binding (RFC 3261 §10.3), so a refresh through another node
// overwrites rather than duplicates.
func bindingKey(b Binding) string { return b.ContactURI }

// PutBinding stores or refreshes b; the field expires at b.Expires.
func (s *Store) PutBinding(ctx context.Context, b Binding) error {
	ttl := time.Until(b.Expires)
	if ttl <= 0 {
		return s.DeleteBinding(ctx, b.AOR, b.ContactURI)
	}
	v, err := json.Marshal(b)
	if err != nil {
		return err
	}
	err = putBindingScript.Exec(ctx, s.c, []string{regPrefix + b.AOR},
		[]string{bindingKey(b), string(v), strconv.FormatInt(max(ttl.Milliseconds(), 1), 10)}).Error()
	if err != nil {
		return fmt.Errorf("livestate: put binding: %w", err)
	}
	return nil
}

// putBindingScript sets a binding field and its expiry in one step, so a
// field never exists without a TTL (a pipelined HSET + HPEXPIRE could leave
// one behind if the second command failed).
var putBindingScript = valkey.NewLuaScript(`
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('HPEXPIRE', KEYS[1], ARGV[3], 'FIELDS', 1, ARGV[1])
return 1`)

// DeleteBinding removes one contact of an AOR.
func (s *Store) DeleteBinding(ctx context.Context, aor, contactURI string) error {
	return s.c.Do(ctx, s.c.B().Hdel().Key(regPrefix+aor).Field(contactURI).Build()).Error()
}

// DeleteAOR removes every contact of an AOR (Contact: *, or device removal).
func (s *Store) DeleteAOR(ctx context.Context, aor string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(regPrefix+aor).Build()).Error()
}

// Bindings returns the unexpired contacts of an AOR.
func (s *Store) Bindings(ctx context.Context, aor string) ([]Binding, error) {
	m, err := s.c.Do(ctx, s.c.B().Hgetall().Key(regPrefix+aor).Build()).AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("livestate: bindings: %w", err)
	}
	return decodeBindings(m)
}

// AllBindings returns every unexpired binding in the cluster.
func (s *Store) AllBindings(ctx context.Context) ([]Binding, error) {
	keys, err := s.scan(ctx, regPrefix+"*")
	if err != nil {
		return nil, err
	}
	var out []Binding
	for _, k := range keys {
		bs, err := s.Bindings(ctx, strings.TrimPrefix(k, regPrefix))
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return out, nil
}

func decodeBindings(m map[string]string) ([]Binding, error) {
	now := time.Now()
	out := make([]Binding, 0, len(m))
	for _, v := range m {
		var b Binding
		if err := json.Unmarshal([]byte(v), &b); err != nil {
			return nil, fmt.Errorf("livestate: decode binding: %w", err)
		}
		if b.Expires.After(now) { // field expiry is per-second; never return a stale one
			out = append(out, b)
		}
	}
	return out, nil
}

// PutCall publishes or refreshes c; it disappears after ttl unless refreshed,
// so a dead node's calls vanish on their own.
func (s *Store) PutCall(ctx context.Context, c Call, ttl time.Duration) error {
	v, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, s.c.B().Set().Key(callPrefix+c.ID).Value(string(v)).Px(ttl).Build()).Error()
}

// DeleteCall removes an ended call.
func (s *Store) DeleteCall(ctx context.Context, id string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(callPrefix+id).Build()).Error()
}

// Calls returns every active call in the cluster.
func (s *Store) Calls(ctx context.Context) ([]Call, error) {
	keys, err := s.scan(ctx, callPrefix+"*")
	if err != nil {
		return nil, err
	}
	out := make([]Call, 0, len(keys))
	for _, k := range keys {
		v, err := s.c.Do(ctx, s.c.B().Get().Key(k).Build()).ToString()
		if valkey.IsValkeyNil(err) {
			continue // expired between SCAN and GET
		}
		if err != nil {
			return nil, fmt.Errorf("livestate: get call: %w", err)
		}
		var c Call
		if err := json.Unmarshal([]byte(v), &c); err != nil {
			return nil, fmt.Errorf("livestate: decode call: %w", err)
		}
		out = append(out, c)
	}
	return out, nil
}

// debt: SCAN walks the whole keyspace; fine for lab/small installs (thousands
// of keys). Revisit with a secondary index set when registrations exceed ~50k.
func (s *Store) scan(ctx context.Context, match string) ([]string, error) {
	var keys []string
	var cursor uint64
	for {
		e, err := s.c.Do(ctx, s.c.B().Scan().Cursor(cursor).Match(match).Count(500).Build()).AsScanEntry()
		if err != nil {
			return nil, fmt.Errorf("livestate: scan: %w", err)
		}
		keys = append(keys, e.Elements...)
		if e.Cursor == 0 {
			return keys, nil
		}
		cursor = e.Cursor
	}
}
