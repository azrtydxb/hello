package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// TrunkRegistration is the shared registration state of one trunk.
type TrunkRegistration struct {
	State     string    `json:"state"` // "registered" | "registering" | "failed" | "misconfigured" | "disabled"
	Node      string    `json:"node"`  // node ID holding the lease
	LastCode  int       `json:"lastCode,omitempty"`
	Expires   time.Time `json:"expires,omitzero"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// DestinationHealth is the OPTIONS health of one trunk destination.
type DestinationHealth struct {
	Destination string        `json:"destination"` // host:port as configured
	Up          bool          `json:"up"`
	LastCode    int           `json:"lastCode,omitempty"`
	Latency     time.Duration `json:"latencyNs,omitempty"`
	CheckedAt   time.Time     `json:"checkedAt"`
}

// TrunkStatus is everything shared about one trunk.
type TrunkStatus struct {
	TrunkID      int64               `json:"trunkId"`
	Registration *TrunkRegistration  `json:"registration,omitempty"` // nil for IP trunks
	Destinations []DestinationHealth `json:"destinations"`
	ActiveCalls  int                 `json:"activeCalls"`
}

func trunkKey(id int64, suffix string) string {
	return "hello:trunk:" + strconv.FormatInt(id, 10) + ":" + suffix
}

// AcquireLease takes or renews the lease named key for node, valid for ttl.
// It returns true while node holds it; another node's unexpired lease is
// never taken over.
func (s *Store) AcquireLease(ctx context.Context, key, node string, ttl time.Duration) (bool, error) {
	r := acquireLease.Exec(ctx, s.c, []string{key}, []string{node, strconv.FormatInt(ttl.Milliseconds(), 10)})
	n, err := r.AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: lease %s: %w", key, err)
	}
	return n == 1, nil
}

// ReleaseLease drops the lease if node holds it.
func (s *Store) ReleaseLease(ctx context.Context, key, node string) error {
	return releaseLease.Exec(ctx, s.c, []string{key}, []string{node}).Error()
}

// TrunkLeaseKey is the registration and health-check lease for a trunk.
func TrunkLeaseKey(id int64) string { return "hello:trunkreg:" + strconv.FormatInt(id, 10) }

var acquireLease = valkey.NewLuaScript(`
local cur = redis.call('GET', KEYS[1])
if cur == false or cur == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
  return 1
end
return 0`)

var releaseLease = valkey.NewLuaScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)

// PutTrunkRegistration publishes a trunk's registration state for ttl.
func (s *Store) PutTrunkRegistration(ctx context.Context, id int64, r TrunkRegistration, ttl time.Duration) error {
	v, err := json.Marshal(r)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, s.c.B().Set().Key(trunkKey(id, "reg")).Value(string(v)).Px(ttl).Build()).Error()
}

// PutDestinationHealth publishes one destination's health for ttl.
func (s *Store) PutDestinationHealth(ctx context.Context, id int64, h DestinationHealth, ttl time.Duration) error {
	v, err := json.Marshal(h)
	if err != nil {
		return err
	}
	key := trunkKey(id, "health")
	return putFieldWithTTL.Exec(ctx, s.c, []string{key}, []string{h.Destination, string(v), strconv.FormatInt(ttl.Milliseconds(), 10)}).Error()
}

var putFieldWithTTL = valkey.NewLuaScript(`
redis.call('HSET', KEYS[1], ARGV[1], ARGV[2])
redis.call('HPEXPIRE', KEYS[1], ARGV[3], 'FIELDS', 1, ARGV[1])
return 1`)

// AcquireTrunkCall counts call against trunk id when fewer than max calls
// are active (max 0 = unlimited) and reports whether it was admitted. Each
// call's slot expires after ttl unless refreshed, so a dead node's calls
// stop counting on their own.
func (s *Store) AcquireTrunkCall(ctx context.Context, id int64, call string, max int, ttl time.Duration) (bool, error) {
	now := time.Now()
	r := acquireSlot.Exec(ctx, s.c, []string{trunkKey(id, "calls")}, []string{
		strconv.FormatInt(now.UnixMilli(), 10), strconv.FormatInt(now.Add(ttl).UnixMilli(), 10), call, strconv.Itoa(max),
	})
	n, err := r.AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: trunk %d slot: %w", id, err)
	}
	return n == 1, nil
}

// RefreshTrunkCall extends a held slot; ReleaseTrunkCall frees it.
func (s *Store) RefreshTrunkCall(ctx context.Context, id int64, call string, ttl time.Duration) error {
	return s.c.Do(ctx, s.c.B().Zadd().Key(trunkKey(id, "calls")).Xx().ScoreMember().ScoreMember(float64(time.Now().Add(ttl).UnixMilli()), call).Build()).Error()
}

// ReleaseTrunkCall frees a call's slot.
func (s *Store) ReleaseTrunkCall(ctx context.Context, id int64, call string) error {
	return s.c.Do(ctx, s.c.B().Zrem().Key(trunkKey(id, "calls")).Member(call).Build()).Error()
}

// The slot set holds call IDs scored by their expiry (ms); expired members
// are pruned before counting, so the count only covers live calls.
var acquireSlot = valkey.NewLuaScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local max = tonumber(ARGV[4])
if max > 0 and redis.call('ZSCORE', KEYS[1], ARGV[3]) == false and redis.call('ZCARD', KEYS[1]) >= max then
  return 0
end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[3])
redis.call('PEXPIREAT', KEYS[1], ARGV[2])
return 1`)

// TrunkStatus reads one trunk's shared state.
func (s *Store) TrunkStatus(ctx context.Context, id int64) (TrunkStatus, error) {
	st := TrunkStatus{TrunkID: id, Destinations: []DestinationHealth{}}
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	res := s.c.DoMulti(ctx,
		s.c.B().Get().Key(trunkKey(id, "reg")).Build(),
		s.c.B().Hgetall().Key(trunkKey(id, "health")).Build(),
		s.c.B().Zcount().Key(trunkKey(id, "calls")).Min("("+now).Max("+inf").Build(),
	)
	if v, err := res[0].ToString(); err == nil {
		var r TrunkRegistration
		if err := json.Unmarshal([]byte(v), &r); err != nil {
			return st, fmt.Errorf("livestate: decode trunk registration: %w", err)
		}
		st.Registration = &r
	} else if !valkey.IsValkeyNil(err) {
		return st, fmt.Errorf("livestate: trunk registration: %w", err)
	}
	m, err := res[1].AsStrMap()
	if err != nil {
		return st, fmt.Errorf("livestate: trunk health: %w", err)
	}
	for _, v := range m {
		var h DestinationHealth
		if err := json.Unmarshal([]byte(v), &h); err != nil {
			return st, fmt.Errorf("livestate: decode trunk health: %w", err)
		}
		st.Destinations = append(st.Destinations, h)
	}
	n, err := res[2].AsInt64()
	if err != nil {
		return st, fmt.Errorf("livestate: trunk calls: %w", err)
	}
	st.ActiveCalls = int(n)
	return st, nil
}
