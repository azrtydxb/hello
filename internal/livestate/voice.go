// Voice agent call slots (spec voice-agents S-35): the shared, cluster-wide
// count of simultaneous calls per agent and in total, kept the way trunk
// call slots are — scored sets whose members expire unless the call's node
// refreshes them, so a dead node's calls stop counting on their own.
package livestate

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// AgentCallKey is the slot set of one voice agent. Agent names are
// `^[A-Za-z0-9._-]{1,64}$`, so they are safe in a key.
func AgentCallKey(agent string) string { return "hello:voiceagent:" + agent + ":calls" }

// TotalAgentCallKey is the slot set over all voice agents.
const TotalAgentCallKey = "hello:voiceagents:calls"

// AgentCallFull says which cap refused an agent call. The zero value means
// it was admitted.
type AgentCallFull uint8

const (
	// AgentCallAdmitted: the call counts from now on.
	AgentCallAdmitted AgentCallFull = 0
	// AgentFull: the agent is at its max_concurrent.
	AgentFull AgentCallFull = 1
	// AgentTotalFull: HELLO_VOICE_MAX_CALLS is reached.
	AgentTotalFull AgentCallFull = 2
)

// AcquireAgentCall counts call against agent and against the total when
// fewer than max (the agent's max_concurrent) and totalMax
// (HELLO_VOICE_MAX_CALLS, 0 = unlimited) calls are active, and reports
// which cap applied when it was refused. Both sets change in one script, so
// a call never counts in one and not the other. Each slot expires after ttl
// unless refreshed.
func (s *Store) AcquireAgentCall(ctx context.Context, agent, call string, max, totalMax int, ttl time.Duration) (AgentCallFull, error) {
	now := time.Now()
	n, err := acquireAgentSlot.Exec(ctx, s.c, []string{AgentCallKey(agent), TotalAgentCallKey}, []string{
		strconv.FormatInt(now.UnixMilli(), 10), strconv.FormatInt(now.Add(ttl).UnixMilli(), 10),
		call, strconv.Itoa(max), strconv.Itoa(totalMax),
	}).AsInt64()
	if err != nil {
		return 0, fmt.Errorf("livestate: voice agent %s slot: %w", agent, err)
	}
	if n < 0 || n > 255 {
		return 0, fmt.Errorf("livestate: voice agent %s slot: unexpected script result %d", agent, n)
	}
	return AgentCallFull(n), nil
}

// AgentSlotRefresh says what refreshing a call's voice slots did, like
// SlotRefresh does for trunks.
type AgentSlotRefresh uint8

const (
	// AgentSlotRefreshed: both slots were held and now live until the new
	// expiry.
	AgentSlotRefreshed AgentSlotRefresh = 0
	// AgentSlotOvercommitted: the agent slot had vanished and was re-added
	// although the agent was full (an established call is never dropped).
	AgentSlotOvercommitted AgentSlotRefresh = 1
	// AgentTotalSlotOvercommitted: the total slot was re-added over the
	// total cap.
	AgentTotalSlotOvercommitted AgentSlotRefresh = 2
)

// RefreshAgentCall moves call's slots on agent and the total to ttl from
// now. A vanished slot is re-added under the same rule as AcquireAgentCall;
// if its set is full the slot is added anyway and reported, since the call
// is established and must keep counting (see RefreshTrunkCall).
func (s *Store) RefreshAgentCall(ctx context.Context, agent, call string, max, totalMax int, ttl time.Duration) (AgentSlotRefresh, error) {
	now := time.Now()
	n, err := refreshAgentSlot.Exec(ctx, s.c, []string{AgentCallKey(agent), TotalAgentCallKey}, []string{
		strconv.FormatInt(now.Add(ttl).UnixMilli(), 10), call, strconv.FormatInt(now.UnixMilli(), 10),
		strconv.Itoa(max), strconv.Itoa(totalMax),
	}).AsInt64()
	if err != nil {
		return 0, fmt.Errorf("livestate: voice agent %s slot refresh: %w", agent, err)
	}
	if n < 0 || n > 255 {
		return 0, fmt.Errorf("livestate: voice agent %s slot refresh: unexpected script result %d", agent, n)
	}
	return AgentSlotRefresh(n), nil
}

// ReleaseAgentCall frees call's slot on agent and on the total.
func (s *Store) ReleaseAgentCall(ctx context.Context, agent, call string) error {
	return releaseAgentCall.Exec(ctx, s.c,
		[]string{AgentCallKey(agent), TotalAgentCallKey}, []string{call}).Error()
}

// releaseAgentCall removes call from both slot sets.
var releaseAgentCall = valkey.NewLuaScript(`
redis.call('ZREM', KEYS[1], ARGV[1])
redis.call('ZREM', KEYS[2], ARGV[1])
return 1`)

// acquireAgentSlot admits call into both sets unless a full set would take
// a new member: 0 admitted, 1 agent full, 2 total full. Expired members are
// pruned before counting, and each set's own expiry follows its
// longest-lived member without ever shortening.
var acquireAgentSlot = valkey.NewLuaScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[1])
local amax, tmax = tonumber(ARGV[4]), tonumber(ARGV[5])
local mine = redis.call('ZSCORE', KEYS[1], ARGV[3])
local total = redis.call('ZSCORE', KEYS[2], ARGV[3])
if mine == false and amax > 0 and redis.call('ZCARD', KEYS[1]) >= amax then return 1 end
if total == false and tmax > 0 and redis.call('ZCARD', KEYS[2]) >= tmax then return 2 end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[3])
redis.call('ZADD', KEYS[2], ARGV[2], ARGV[3])
if tonumber(ARGV[2]) > redis.call('PEXPIRETIME', KEYS[1]) then
  redis.call('PEXPIREAT', KEYS[1], ARGV[2])
end
if tonumber(ARGV[2]) > redis.call('PEXPIRETIME', KEYS[2]) then
  redis.call('PEXPIREAT', KEYS[2], ARGV[2])
end
return 0`)

// refreshAgentSlot moves call's expiry in both sets to ARGV[1], never
// shortening a set's own expiry, re-adding a vanished slot: 0 when both
// held or fit, bit 1 (value 1) when the agent was full and bit 2 (value 2)
// when the total was (added anyway, see RefreshAgentCall).
var refreshAgentSlot = valkey.NewLuaScript(`
local result = 0
for i, max in ipairs({ARGV[4], ARGV[5]}) do
  if redis.call('ZSCORE', KEYS[i], ARGV[2]) == false then
    redis.call('ZREMRANGEBYSCORE', KEYS[i], '-inf', ARGV[3])
    if tonumber(max) > 0 and redis.call('ZCARD', KEYS[i]) >= tonumber(max) then
      result = result + math.floor(2 ^ (i - 1))
    end
  end
  redis.call('ZADD', KEYS[i], ARGV[1], ARGV[2])
  if tonumber(ARGV[1]) > redis.call('PEXPIRETIME', KEYS[i]) then
    redis.call('PEXPIREAT', KEYS[i], ARGV[1])
  end
end
return result`)
