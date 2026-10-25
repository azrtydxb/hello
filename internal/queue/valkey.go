package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Valkey key layout. Everything a queue shares between nodes is here, so the
// two sides (hello-sip and hello-control) read the same records.
const (
	callsPrefix  = "hello:queue:calls:" // hash per queue: call id -> Entry JSON
	leasePrefix  = "hello:queue:lease:" // string per lease, value the holder's token
	agentsKey    = "hello:queue:agents" // hash: extension -> AgentState JSON
	statsPrefix  = "hello:queue:stats:" // hash per queue: extension -> AgentStats JSON
	metaPrefix   = "hello:queue:meta:"  // hash per queue: avgMs, rr
	fullReply    = "FULL"               // Join's reply for a full queue
	scriptTimeMS = `local t = redis.call('TIME'); local now = t[1] * 1000 + math.floor(t[2] / 1000)`
)

// ValkeyStore is the Store on Valkey: waiting callers, leases, agent state
// and statistics shared by every node. The multi-step changes (join with a
// capacity check, heartbeat, statistics) are Lua scripts, so two nodes never
// interleave them, and the clock they use is Valkey's.
type ValkeyStore struct {
	c valkey.Client
}

var _ Store = (*ValkeyStore)(nil)

// NewValkey wraps a Valkey client.
func NewValkey(c valkey.Client) *ValkeyStore { return &ValkeyStore{c: c} }

// joinScript prunes the stale entries, then adds the caller unless it is
// there already or the queue is full. KEYS[1] the queue's hash; ARGV: call
// id, entry JSON, max callers, entry TTL in ms.
var joinScript = valkey.NewLuaScript(scriptTimeMS + `
local ttl = tonumber(ARGV[4])
local all = redis.call('HGETALL', KEYS[1])
local live = 0
local cur = nil
for i = 1, #all, 2 do
  local e = cjson.decode(all[i + 1])
  if now - tonumber(e.heartbeatMs) > ttl then
    redis.call('HDEL', KEYS[1], all[i])
  else
    live = live + 1
    if all[i] == ARGV[1] then cur = all[i + 1] end
  end
end
if cur then return cur end
local max = tonumber(ARGV[3])
if max > 0 and live >= max then return '` + fullReply + `' end
local e = cjson.decode(ARGV[2])
if tonumber(e.enteredMs) == 0 then e.enteredMs = now end
e.heartbeatMs = now
local out = cjson.encode(e)
redis.call('HSET', KEYS[1], ARGV[1], out)
return out`)

// touchScript refreshes an entry's heartbeat and its offering. KEYS[1] the
// queue's hash; ARGV: call id, node, offering.
var touchScript = valkey.NewLuaScript(scriptTimeMS + `
local cur = redis.call('HGET', KEYS[1], ARGV[1])
if not cur then return 0 end
local e = cjson.decode(cur)
e.node = ARGV[2]
e.offering = ARGV[3]
e.heartbeatMs = now
redis.call('HSET', KEYS[1], ARGV[1], cjson.encode(e))
return 1`)

// waitingScript drops the stale entries and returns the live ones. ARGV: the
// entry TTL in ms.
var waitingScript = valkey.NewLuaScript(scriptTimeMS + `
local ttl = tonumber(ARGV[1])
local all = redis.call('HGETALL', KEYS[1])
local out = {}
for i = 1, #all, 2 do
  local e = cjson.decode(all[i + 1])
  if now - tonumber(e.heartbeatMs) > ttl then
    redis.call('HDEL', KEYS[1], all[i])
  else
    out[#out + 1] = all[i + 1]
  end
end
return out`)

// releaseScript deletes a lease only while token still holds it.
var releaseScript = valkey.NewLuaScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then return redis.call('DEL', KEYS[1]) end
return 0`)

// answeredScript counts an answered call. KEYS[1] the stats hash; ARGV: ext.
var answeredScript = valkey.NewLuaScript(`
local cur = redis.call('HGET', KEYS[1], ARGV[1])
local s = {calls = 0, lastEndMs = 0, wrapUntilMs = 0}
if cur then s = cjson.decode(cur) end
s.calls = tonumber(s.calls) + 1
redis.call('HSET', KEYS[1], ARGV[1], cjson.encode(s))
return s.calls`)

// endedScript records a call's end. KEYS[1] stats hash, KEYS[2] meta hash;
// ARGV: ext, talk ms, wrap ms.
var endedScript = valkey.NewLuaScript(scriptTimeMS + `
local cur = redis.call('HGET', KEYS[1], ARGV[1])
local s = {calls = 0, lastEndMs = 0, wrapUntilMs = 0}
if cur then s = cjson.decode(cur) end
s.lastEndMs = now
s.wrapUntilMs = now + tonumber(ARGV[3])
redis.call('HSET', KEYS[1], ARGV[1], cjson.encode(s))
local talk = tonumber(ARGV[2])
if talk > 0 then
  local avg = tonumber(redis.call('HGET', KEYS[2], 'avgMs') or '0')
  if avg <= 0 then avg = talk else avg = math.floor((avg * 4 + talk) / 5) end
  redis.call('HSET', KEYS[2], 'avgMs', avg)
end
return 1`)

func (s *ValkeyStore) Join(ctx context.Context, e Entry, maxCallers int) (Entry, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return Entry{}, err
	}
	out, err := joinScript.Exec(ctx, s.c, []string{callsPrefix + e.Queue},
		[]string{e.CallID, string(raw), strconv.Itoa(maxCallers), strconv.FormatInt(EntryTTL.Milliseconds(), 10)}).ToString()
	if err != nil {
		return Entry{}, fmt.Errorf("queue: join: %w", err)
	}
	if out == fullReply {
		return Entry{}, ErrFull
	}
	var got Entry
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		return Entry{}, fmt.Errorf("queue: join reply: %w", err)
	}
	return got, nil
}

func (s *ValkeyStore) Touch(ctx context.Context, queue, callID, node, offering string) (bool, error) {
	n, err := touchScript.Exec(ctx, s.c, []string{callsPrefix + queue}, []string{callID, node, offering}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("queue: touch: %w", err)
	}
	return n == 1, nil
}

func (s *ValkeyStore) Leave(ctx context.Context, queue, callID string) error {
	if err := s.c.Do(ctx, s.c.B().Hdel().Key(callsPrefix+queue).Field(callID).Build()).Error(); err != nil {
		return fmt.Errorf("queue: leave: %w", err)
	}
	return nil
}

func (s *ValkeyStore) Waiting(ctx context.Context, queue string) ([]Entry, error) {
	raws, err := waitingScript.Exec(ctx, s.c, []string{callsPrefix + queue},
		[]string{strconv.FormatInt(EntryTTL.Milliseconds(), 10)}).AsStrSlice()
	if err != nil {
		return nil, fmt.Errorf("queue: waiting: %w", err)
	}
	out := make([]Entry, 0, len(raws))
	for _, r := range raws {
		var e Entry
		if err := json.Unmarshal([]byte(r), &e); err != nil {
			return nil, fmt.Errorf("queue: decode entry: %w", err)
		}
		out = append(out, e)
	}
	Sort(out)
	return out, nil
}

func (s *ValkeyStore) Acquire(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	err := s.c.Do(ctx, s.c.B().Set().Key(leasePrefix+key).Value(token).Nx().Px(ttl).Build()).Error()
	switch {
	case err == nil:
		return true, nil
	case valkey.IsValkeyNil(err):
		return false, nil
	}
	return false, fmt.Errorf("queue: acquire %s: %w", key, err)
}

func (s *ValkeyStore) Release(ctx context.Context, key, token string) error {
	if err := releaseScript.Exec(ctx, s.c, []string{leasePrefix + key}, []string{token}).Error(); err != nil {
		return fmt.Errorf("queue: release %s: %w", key, err)
	}
	return nil
}

func (s *ValkeyStore) Holder(ctx context.Context, key string) (string, bool, error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(leasePrefix+key).Build()).ToString()
	switch {
	case err == nil:
		return v, true, nil
	case valkey.IsValkeyNil(err):
		return "", false, nil
	}
	return "", false, fmt.Errorf("queue: holder %s: %w", key, err)
}

func (s *ValkeyStore) SetAgent(ctx context.Context, st AgentState) error {
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := s.c.Do(ctx, s.c.B().Hset().Key(agentsKey).FieldValue().FieldValue(st.Ext, string(raw)).Build()).Error(); err != nil {
		return fmt.Errorf("queue: set agent: %w", err)
	}
	return nil
}

func (s *ValkeyStore) Agent(ctx context.Context, ext string) (AgentState, bool, error) {
	v, err := s.c.Do(ctx, s.c.B().Hget().Key(agentsKey).Field(ext).Build()).ToString()
	switch {
	case valkey.IsValkeyNil(err):
		return AgentState{}, false, nil
	case err != nil:
		return AgentState{}, false, fmt.Errorf("queue: agent: %w", err)
	}
	var st AgentState
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		return AgentState{}, false, fmt.Errorf("queue: decode agent: %w", err)
	}
	return st, true, nil
}

func (s *ValkeyStore) Agents(ctx context.Context) (map[string]AgentState, error) {
	m, err := s.c.Do(ctx, s.c.B().Hgetall().Key(agentsKey).Build()).AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("queue: agents: %w", err)
	}
	out := make(map[string]AgentState, len(m))
	for ext, raw := range m {
		var st AgentState
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			return nil, fmt.Errorf("queue: decode agent %s: %w", ext, err)
		}
		out[ext] = st
	}
	return out, nil
}

func (s *ValkeyStore) Stats(ctx context.Context, queue string) (map[string]AgentStats, error) {
	m, err := s.c.Do(ctx, s.c.B().Hgetall().Key(statsPrefix+queue).Build()).AsStrMap()
	if err != nil {
		return nil, fmt.Errorf("queue: stats: %w", err)
	}
	out := make(map[string]AgentStats, len(m))
	for ext, raw := range m {
		var st AgentStats
		if err := json.Unmarshal([]byte(raw), &st); err != nil {
			return nil, fmt.Errorf("queue: decode stats %s: %w", ext, err)
		}
		out[ext] = st
	}
	return out, nil
}

func (s *ValkeyStore) NoteAnswered(ctx context.Context, queue, ext string) error {
	if err := answeredScript.Exec(ctx, s.c, []string{statsPrefix + queue}, []string{ext}).Error(); err != nil {
		return fmt.Errorf("queue: note answered: %w", err)
	}
	return nil
}

func (s *ValkeyStore) NoteEnded(ctx context.Context, queue, ext string, talk, wrap time.Duration) error {
	err := endedScript.Exec(ctx, s.c, []string{statsPrefix + queue, metaPrefix + queue},
		[]string{ext, strconv.FormatInt(talk.Milliseconds(), 10), strconv.FormatInt(wrap.Milliseconds(), 10)}).Error()
	if err != nil {
		return fmt.Errorf("queue: note ended: %w", err)
	}
	return nil
}

func (s *ValkeyStore) AvgHandle(ctx context.Context, queue string) (time.Duration, error) {
	n, err := s.c.Do(ctx, s.c.B().Hget().Key(metaPrefix+queue).Field("avgMs").Build()).AsInt64()
	switch {
	case valkey.IsValkeyNil(err):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("queue: average handling time: %w", err)
	}
	return time.Duration(n) * time.Millisecond, nil
}

func (s *ValkeyStore) NextRotation(ctx context.Context, queue string) (uint64, error) {
	n, err := s.c.Do(ctx, s.c.B().Hincrby().Key(metaPrefix+queue).Field("rr").Increment(1).Build()).AsInt64()
	if err != nil {
		return 0, fmt.Errorf("queue: rotation: %w", err)
	}
	return uint64(n - 1), nil //nolint:gosec // HINCRBY from 1 never goes negative
}
