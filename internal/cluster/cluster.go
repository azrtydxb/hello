// Package cluster is Hello's node membership (spec .procoder/specs/ha.md
// S-1, S-2, S-4): every hello-sip and hello-control node publishes a record
// in Valkey, refreshed on a heartbeat and expiring when the node stops; an
// expired node is reported OFFLINE from a tombstone for a while; operators
// request drains through it. hello-sip, hello-control and the cluster API
// all use this package, so the record shape and keys live in one place.
package cluster

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
)

// State is a node's lifecycle state (spec §17.6).
type State string

const (
	Joining   State = "JOINING"
	Ready     State = "READY"
	Draining  State = "DRAINING"
	Unhealthy State = "UNHEALTHY"
	Offline   State = "OFFLINE" // derived from a tombstone; never published
)

// Kind is what a node runs.
type Kind string

const (
	KindSIP     Kind = "sip"
	KindControl Kind = "control"
)

// Member is one node's membership record.
type Member struct {
	ID             string    `json:"id"`
	Kind           Kind      `json:"kind"`
	State          State     `json:"state"`
	Reason         string    `json:"reason,omitempty"` // why it is not READY
	SIPAddr        string    `json:"sipAddr,omitempty"`
	HTTPAddr       string    `json:"httpAddr,omitempty"`
	Transports     []string  `json:"transports,omitempty"`
	ActiveCalls    int       `json:"activeCalls"`
	Registrations  int       `json:"registrations"`
	Version        string    `json:"version"`
	ConfigRevision int64     `json:"configRevision"`
	StartedAt      time.Time `json:"startedAt"`
	Heartbeat      time.Time `json:"heartbeat"`
}

// Timings of the membership protocol.
const (
	// TTL is how long a record lives without a heartbeat (3 heartbeats).
	TTL = 15 * time.Second
	// TombstoneTTL is how long an expired node is still listed OFFLINE.
	TombstoneTTL = 10 * time.Minute
)

func memberKey(id string) string { return "hello:member:" + id }
func tombKey(id string) string   { return "hello:member:tomb:" + id }
func drainKey(id string) string  { return "hello:drain:" + id }

// Store reads and writes membership.
type Store struct{ c valkey.Client }

// New wraps a Valkey client.
func New(c valkey.Client) *Store { return &Store{c: c} }

// Publish writes m with the record TTL and refreshes its tombstone, which
// outlives the record so an expired node is still listed as OFFLINE. A
// node publishing again (a restart reusing its ID) simply overwrites both.
func (s *Store) Publish(ctx context.Context, m Member) error {
	if m.State == Offline {
		return fmt.Errorf("cluster: OFFLINE is derived, not published")
	}
	v, err := json.Marshal(m)
	if err != nil {
		return err
	}
	for _, r := range s.c.DoMulti(ctx,
		s.c.B().Set().Key(memberKey(m.ID)).Value(string(v)).Px(TTL).Build(),
		s.c.B().Set().Key(tombKey(m.ID)).Value(string(v)).Px(TTL+TombstoneTTL).Build(),
	) {
		if err := r.Error(); err != nil {
			return fmt.Errorf("cluster: publish %s: %w", m.ID, err)
		}
	}
	return nil
}

// Leave removes a node's live record and any drain request for it at clean
// shutdown, so a restart reusing the ID is not drained again; its tombstone
// stays so it is listed OFFLINE.
func (s *Store) Leave(ctx context.Context, id string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(memberKey(id), drainKey(id)).Build()).Error()
}

// Members lists every live member plus tombstoned ones as OFFLINE, sorted
// by kind then ID.
func (s *Store) Members(ctx context.Context) ([]Member, error) {
	live, err := s.scanRecords(ctx, "hello:member:*", func(k string) bool { return !strings.HasPrefix(k, "hello:member:tomb:") })
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]Member, 0, len(live))
	for _, m := range live {
		seen[m.ID] = true
		out = append(out, m)
	}
	tombs, err := s.scanRecords(ctx, "hello:member:tomb:*", nil)
	if err != nil {
		return nil, err
	}
	for _, m := range tombs {
		if !seen[m.ID] {
			m.State, m.Reason, m.ActiveCalls, m.Registrations = Offline, "no heartbeat", 0, 0
			out = append(out, m)
		}
	}
	slices.SortFunc(out, func(a, b Member) int {
		if c := strings.Compare(string(a.Kind), string(b.Kind)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// RequestDrain asks node id to drain; CancelDrain withdraws the request.
// Requests have no TTL: a drain stands until cancelled or the node leaves.
func (s *Store) RequestDrain(ctx context.Context, id string) error {
	return s.c.Do(ctx, s.c.B().Set().Key(drainKey(id)).Value(time.Now().UTC().Format(time.RFC3339)).Build()).Error()
}

// CancelDrain withdraws a drain request.
func (s *Store) CancelDrain(ctx context.Context, id string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(drainKey(id)).Build()).Error()
}

// DrainRequested reports whether a drain is requested for node id.
func (s *Store) DrainRequested(ctx context.Context, id string) (bool, error) {
	n, err := s.c.Do(ctx, s.c.B().Exists().Key(drainKey(id)).Build()).AsInt64()
	if err != nil {
		return false, fmt.Errorf("cluster: drain request %s: %w", id, err)
	}
	return n == 1, nil
}

func (s *Store) scanRecords(ctx context.Context, match string, keep func(string) bool) ([]Member, error) {
	var keys []string
	var cursor uint64
	for {
		e, err := s.c.Do(ctx, s.c.B().Scan().Cursor(cursor).Match(match).Count(200).Build()).AsScanEntry()
		if err != nil {
			return nil, fmt.Errorf("cluster: scan: %w", err)
		}
		for _, k := range e.Elements {
			if keep == nil || keep(k) {
				keys = append(keys, k)
			}
		}
		if e.Cursor == 0 {
			break
		}
		cursor = e.Cursor
	}
	if len(keys) == 0 {
		return nil, nil
	}
	vals, err := s.c.Do(ctx, s.c.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil {
		return nil, fmt.Errorf("cluster: mget: %w", err)
	}
	out := make([]Member, 0, len(keys))
	for i, val := range vals {
		k := keys[i]
		v, err := val.ToString()
		if valkey.IsValkeyNil(err) {
			continue // expired between SCAN and MGET
		}
		if err != nil {
			return nil, fmt.Errorf("cluster: get %s: %w", k, err)
		}
		var m Member
		if err := json.Unmarshal([]byte(v), &m); err != nil {
			return nil, fmt.Errorf("cluster: decode %s: %w", k, err)
		}
		out = append(out, m)
	}
	return out, nil
}
