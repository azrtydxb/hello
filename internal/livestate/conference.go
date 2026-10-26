package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Shared state of the server conference rooms (spec S-34). A room is mixed
// on exactly one hello-sip node, its owner, recorded as a short lease that
// the owner renews while the room has participants. The owner publishes the
// room's live state (who is in it, who is muted, whether it is locked) for
// hello-control to read, and hello-control queues control commands (mute,
// kick, lock) for the owner to take.

const (
	confOwnerPrefix = "hello:conf:owner:"
	confStatePrefix = "hello:conf:state:"
	confCmdPrefix   = "hello:conf:cmd:"

	// ConfOwnerTTL is how long an owner lease lives without a renewal. The
	// owner renews every ConfOwnerTTL/3, so a dead owner's room is free for
	// another node to take over within this time.
	ConfOwnerTTL = 6 * time.Second
	// ConfStateTTL is how long a published room state lives without a
	// refresh; a room whose owner died drops out of the live view by itself.
	ConfStateTTL = 6 * time.Second
	// ConfCommandTTL bounds how long a queued command waits for its owner.
	ConfCommandTTL = 30 * time.Second
)

// ConfOwnerKey is the owner lease key of a room; AcquireLease and
// ReleaseLease take it.
func ConfOwnerKey(number string) string { return confOwnerPrefix + number }

// ConfParticipant is one participant in a published room state.
type ConfParticipant struct {
	// ID identifies the participant for control commands: the correlation id
	// of its call.
	ID string `json:"id"`
	// Number and Name are the participant's extension or caller number and
	// its display name.
	Number string `json:"number"`
	Name   string `json:"name"`
	Leader bool   `json:"leader"`
	Muted  bool   `json:"muted"`
	// MutedByLeader is set when a leader (or an admin) muted the participant,
	// so the participant cannot unmute itself.
	MutedByLeader bool `json:"mutedByLeader"`
	// Waiting is set while the participant waits for a leader.
	Waiting  bool      `json:"waiting"`
	JoinedAt time.Time `json:"joinedAt"`
}

// ConfRoomState is a room's live state, published by its owner.
type ConfRoomState struct {
	Room         string            `json:"room"` // the room number
	Node         string            `json:"node"`
	Locked       bool              `json:"locked"`
	Recording    bool              `json:"recording"`
	Participants []ConfParticipant `json:"participants"`
	UpdatedAt    time.Time         `json:"updatedAt"`
}

// ConfCommand is a control command for a room, queued for its owner node.
type ConfCommand struct {
	ID string `json:"id"`
	// Room is the room number.
	Room string `json:"room"`
	// Action is mute, unmute, kick, lock, unlock, mute_all or unmute_all.
	Action string `json:"action"`
	// Participant is the target of mute, unmute and kick.
	Participant string    `json:"participant,omitempty"`
	Actor       string    `json:"actor"`
	At          time.Time `json:"at"`
}

// ConfOwner returns the node that owns room, "" when nobody does.
func (s *Store) ConfOwner(ctx context.Context, number string) (string, error) {
	v, err := s.c.Do(ctx, s.c.B().Get().Key(ConfOwnerKey(number)).Build()).ToString()
	if valkey.IsValkeyNil(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("livestate: conference owner: %w", err)
	}
	return v, nil
}

// TakeConfOwner moves a room from a dead owner to node, atomically: it only
// succeeds while the lease still names from.
func (s *Store) TakeConfOwner(ctx context.Context, number, from, node string, ttl time.Duration) (bool, error) {
	n, err := takeLease.Exec(ctx, s.c, []string{ConfOwnerKey(number)}, []string{from, node, fmt.Sprint(ttl.Milliseconds())}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: take conference owner: %w", err)
	}
	return n == 1, nil
}

var takeLease = valkey.NewLuaScript(`
if redis.call('GET', KEYS[1]) == ARGV[1] then
  redis.call('SET', KEYS[1], ARGV[2], 'PX', ARGV[3])
  return 1
end
return 0`)

// PutConfState publishes a room's live state.
func (s *Store) PutConfState(ctx context.Context, st ConfRoomState) error {
	st.UpdatedAt = time.Now().UTC()
	v, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, s.c.B().Set().Key(confStatePrefix+st.Room).Value(string(v)).Px(ConfStateTTL).Build()).Error()
}

// DeleteConfState removes a room's published state (the room emptied).
func (s *Store) DeleteConfState(ctx context.Context, number string) error {
	return s.c.Do(ctx, s.c.B().Del().Key(confStatePrefix+number).Build()).Error()
}

// ConfStates returns the live state of the given rooms; a room with no state
// (empty, or its owner died) is absent from the result.
func (s *Store) ConfStates(ctx context.Context, numbers []string) (map[string]ConfRoomState, error) {
	out := map[string]ConfRoomState{}
	if len(numbers) == 0 {
		return out, nil
	}
	keys := make([]string, len(numbers))
	for i, n := range numbers {
		keys[i] = confStatePrefix + n
	}
	vals, err := s.c.Do(ctx, s.c.B().Mget().Key(keys...).Build()).ToArray()
	if err != nil {
		return nil, fmt.Errorf("livestate: conference states: %w", err)
	}
	for i, v := range vals {
		str, err := v.ToString()
		if err != nil {
			continue // a nil reply: no state for this room
		}
		var st ConfRoomState
		if json.Unmarshal([]byte(str), &st) == nil {
			out[numbers[i]] = st //nolint:gosec // MGET returns one reply per key
		}
	}
	return out, nil
}

// EnqueueConfCommand queues a command for the node that owns the room.
func (s *Store) EnqueueConfCommand(ctx context.Context, node string, c ConfCommand) error {
	v, err := json.Marshal(c)
	if err != nil {
		return err
	}
	key := confCmdPrefix + node
	for _, res := range s.c.DoMulti(ctx,
		s.c.B().Rpush().Key(key).Element(string(v)).Build(),
		s.c.B().Expire().Key(key).Seconds(int64(ConfCommandTTL/time.Second)).Build(),
	) {
		if err := res.Error(); err != nil {
			return fmt.Errorf("livestate: queue conference command: %w", err)
		}
	}
	return nil
}

// TakeConfCommands removes and returns up to max queued commands for node.
func (s *Store) TakeConfCommands(ctx context.Context, node string, max int) ([]ConfCommand, error) {
	var out []ConfCommand
	for len(out) < max {
		v, err := s.c.Do(ctx, s.c.B().Lpop().Key(confCmdPrefix+node).Build()).ToString()
		if valkey.IsValkeyNil(err) {
			break
		}
		if err != nil {
			return out, fmt.Errorf("livestate: take conference command: %w", err)
		}
		var c ConfCommand
		if json.Unmarshal([]byte(v), &c) == nil && time.Since(c.At) < ConfCommandTTL {
			out = append(out, c)
		}
	}
	return out, nil
}
