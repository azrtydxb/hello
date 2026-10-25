// Package cluster is Hello's node membership (spec .procoder/specs/ha.md
// S-1, S-2, S-4): every hello-sip and hello-control node publishes a record
// in Valkey, refreshed on a heartbeat and expiring when the node stops; an
// expired node is reported OFFLINE from a tombstone for a while; operators
// request drains through it. hello-sip, hello-control and the cluster API
// all use this package, so the record shape and keys live in one place.
package cluster

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
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
	ID          string   `json:"id"`
	Kind        Kind     `json:"kind"`
	State       State    `json:"state"`
	Reason      string   `json:"reason,omitempty"` // why it is not READY
	SIPAddr     string   `json:"sipAddr,omitempty"`
	HTTPAddr    string   `json:"httpAddr,omitempty"`
	Transports  []string `json:"transports,omitempty"`
	ActiveCalls int      `json:"activeCalls"`
	// ConnectedCalls is how many of the active calls were answered (a
	// ringing call is not one): what the zombie reaper counts when the
	// node dies, since a ringing call gets its final response from the
	// edge. Nil from older nodes (ActiveCalls then stands in).
	ConnectedCalls *int      `json:"connectedCalls,omitempty"`
	Registrations  int       `json:"registrations"`
	Version        string    `json:"version"`
	ConfigRevision int64     `json:"configRevision"`
	StartedAt      time.Time `json:"startedAt"`
	Heartbeat      time.Time `json:"heartbeat"`
	// Incarnation identifies one process of the node: a fresh random id
	// per start, so a node restarted in place under the same ID (a crash
	// and a container restart inside the OFFLINE window) is told apart
	// from the process that owned its calls before. Empty from older
	// nodes.
	Incarnation string `json:"incarnation,omitempty"`
	// Left and Superseded are derived for an OFFLINE member, never
	// published. Left: the node shut down cleanly (it called Leave), so its
	// absence is no failure. Superseded: a live node of the same kind and
	// slot (hello-sip-1's replacement pod) started after it. Neither is a
	// problem to alert on; the member stays OFFLINE (its unclaimed calls are
	// still taken over) and listed as history until the tombstone expires.
	Left       bool `json:"left,omitempty"`
	Superseded bool `json:"superseded,omitempty"`
}

// Gone reports an OFFLINE member that is no failure: it left cleanly or was
// replaced by a live node of its slot. A crash nobody replaced is not Gone.
func (m Member) Gone() bool { return m.State == Offline && (m.Left || m.Superseded) }

// podSuffix is what a Deployment appends to its name for a pod:
// "-<replica set hash>-<5 characters>".
var podSuffix = regexp.MustCompile(`-[a-z0-9]{6,10}-[a-z0-9]{5}$`)

// Slot is the stable identity behind a node ID. On kw a node's ID is its pod
// name, and a Deployment's replacement pod gets a new one
// (hello-sip-1-d4f7cfdc8-2ccf7, then hello-sip-1-5c9b7d6f4-k8x2m), so "the
// node that was replaced" needs the ID without the generated suffix: the
// slot is hello-sip-1 for both. IDs without the suffix (a StatefulSet pod,
// a compose service) are their own slot.
func Slot(id string) string { return podSuffix.ReplaceAllString(id, "") }

// NewIncarnation returns a fresh random process incarnation id.
func NewIncarnation() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand never fails on supported platforms; fall back to
		// the clock, which still differs between two starts.
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

// Timings of the membership protocol.
const (
	// TTL is how long a record lives without a heartbeat: four of the
	// default 1s heartbeats (a heartbeat may be at most TTL/3), so a dead
	// node is listed OFFLINE within 4s - the crash detection behind in-call
	// HA's takeover (docs/ha.md).
	TTL = 4 * time.Second
	// TombstoneTTL is how long an expired node is still listed OFFLINE.
	TombstoneTTL = 10 * time.Minute
)

func memberKey(id string) string { return "hello:member:" + id }
func tombKey(id string) string   { return "hello:member:tomb:" + id }
func drainKey(id string) string  { return "hello:drain:" + id }

// rolloutKey is the one-per-kind restart token (AcquireRollout).
func rolloutKey(kind Kind) string { return "hello:rollout:" + string(kind) }

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
	m.Left, m.Superseded = false, false // derived, never published
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
// shutdown, so a restart reusing the ID is not drained again. Its tombstone
// stays, marked Left, so it is listed OFFLINE as history but does not alert:
// a crash (no Leave) keeps an unmarked tombstone and does.
func (s *Store) Leave(ctx context.Context, id string) error {
	if err := s.c.Do(ctx, s.c.B().Del().Key(memberKey(id), drainKey(id)).Build()).Error(); err != nil {
		return err
	}
	raw, err := s.c.Do(ctx, s.c.B().Get().Key(tombKey(id)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return nil // never published: nothing to list
	}
	if err != nil {
		return fmt.Errorf("cluster: leave %s: %w", id, err)
	}
	var m Member
	if err := json.Unmarshal([]byte(raw), &m); err != nil {
		return nil // unreadable tombstone: left as a crash would leave it
	}
	m.Left = true
	v, err := json.Marshal(m)
	if err != nil {
		return err
	}
	// The clock restarts: the node left now, so it is history for the full
	// tombstone period from now.
	return s.c.Do(ctx, s.c.B().Set().Key(tombKey(id)).Value(string(v)).Px(TombstoneTTL).Build()).Error()
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
			m.ConnectedCalls = nil
			out = append(out, m)
		}
	}
	markSuperseded(out)
	slices.SortFunc(out, func(a, b Member) int {
		if c := strings.Compare(string(a.Kind), string(b.Kind)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out, nil
}

// markSuperseded flags every OFFLINE member that a live node of the same
// kind and slot has replaced (a Deployment's new pod has a new name) and that
// started no earlier: no failure, the node is back. A member that left
// cleanly is already Left.
func markSuperseded(ms []Member) {
	for i, m := range ms {
		if m.State != Offline || m.Left {
			continue
		}
		for _, l := range ms {
			if l.State != Offline && l.Kind == m.Kind && l.ID != m.ID && Slot(l.ID) == Slot(m.ID) && !l.StartedAt.Before(m.StartedAt) {
				ms[i].Superseded = true
				break
			}
		}
	}
}

// AcquireRollout takes (or, when id already holds it, renews) the restart
// token of a node kind: one node at a time may start a planned shutdown,
// so a rollout that stops every node of a kind at once (Kuvryn Sync applies
// hello-sip-1 and hello-sip-2 together) cannot leave a draining node with
// no READY survivor. The token expires after ttl, so a holder that died
// does not block its peers for ever. It returns the node that holds the
// token afterwards: id on success, the other node when it is taken.
func (s *Store) AcquireRollout(ctx context.Context, kind Kind, id string, ttl time.Duration) (string, error) {
	key := rolloutKey(kind)
	if err := s.c.Do(ctx, s.c.B().Set().Key(key).Value(id).Nx().Px(ttl).Build()).Error(); err == nil {
		return id, nil
	} else if !valkey.IsValkeyNil(err) {
		return "", fmt.Errorf("cluster: rollout %s: %w", kind, err)
	}
	// Taken: by this node (renew) or another. The renewal is a compare and
	// extend, so a token that changed hands in between is not stolen back.
	n, err := renewRollout.Exec(ctx, s.c, []string{key}, []string{id, strconv.FormatInt(ttl.Milliseconds(), 10)}).AsInt64()
	if err != nil {
		return "", fmt.Errorf("cluster: rollout %s: %w", kind, err)
	}
	if n == 1 {
		return id, nil
	}
	holder, err := s.c.Do(ctx, s.c.B().Get().Key(key).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return s.AcquireRollout(ctx, kind, id, ttl) // released meanwhile
	}
	if err != nil {
		return "", fmt.Errorf("cluster: rollout %s: %w", kind, err)
	}
	return holder, nil
}

// ReleaseRollout gives the restart token back, only if id holds it: the
// restarted node calls it once it is READY, which lets the next node go.
func (s *Store) ReleaseRollout(ctx context.Context, kind Kind, id string) error {
	return releaseRollout.Exec(ctx, s.c, []string{rolloutKey(kind)}, []string{id}).Error()
}

var (
	renewRollout   = valkey.NewLuaScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) end return 0`)
	releaseRollout = valkey.NewLuaScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("DEL", KEYS[1]) end return 0`)
)

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
