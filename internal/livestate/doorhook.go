package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// The doorbell webhook (spec phone-feature-parity S-48) is checked and
// rate-limited by hello-control, which queues the trigger here; any READY
// hello-sip node takes it and rings or pages.

const (
	doorHookQueue  = "hello:doorhook:queue"
	doorHookRate   = "hello:doorhook:rate:"
	doorHookRecent = "hello:doorhook:recent:"
	// DoorTriggerTTL bounds how long a trigger waits for a node: a doorbell
	// pressed a minute ago is not worth ringing for.
	DoorTriggerTTL = 60 * time.Second
)

// DoorTrigger asks a SIP node to ring the Door group or to chime a paging
// group for one webhook.
type DoorTrigger struct {
	ID      string `json:"id"`
	Webhook int64  `json:"webhook"`
	// Name is the webhook's name, the caller name the phones show.
	Name string `json:"name"`
	// Action is ring_door or chime_page.
	Action string `json:"action"`
	// RingGroup is the Door ring group to ring by name; empty rings every
	// Door group. PagingCode is the paging group to chime.
	RingGroup  string `json:"ringGroup,omitempty"`
	PagingCode string `json:"pagingCode,omitempty"`
	// Announcement is the chime's announcement name; empty plays the
	// built-in chime.
	Announcement string `json:"announcement,omitempty"`
	// Label is the optional label of the request, already trimmed.
	Label string    `json:"label,omitempty"`
	At    time.Time `json:"at"`
}

// EnqueueDoorTrigger queues a trigger for a SIP node.
func (s *Store) EnqueueDoorTrigger(ctx context.Context, t DoorTrigger) error {
	v, err := json.Marshal(t)
	if err != nil {
		return err
	}
	for _, res := range s.c.DoMulti(ctx,
		s.c.B().Rpush().Key(doorHookQueue).Element(string(v)).Build(),
		s.c.B().Expire().Key(doorHookQueue).Seconds(int64(DoorTriggerTTL/time.Second)).Build(),
	) {
		if err := res.Error(); err != nil {
			return fmt.Errorf("livestate: queue door trigger: %w", err)
		}
	}
	return nil
}

// TakeDoorTrigger removes and returns the oldest queued trigger; ok is false
// when there is none. A trigger older than DoorTriggerTTL is dropped.
func (s *Store) TakeDoorTrigger(ctx context.Context) (t DoorTrigger, ok bool, err error) {
	for {
		v, err := s.c.Do(ctx, s.c.B().Lpop().Key(doorHookQueue).Build()).ToString()
		if valkey.IsValkeyNil(err) {
			return t, false, nil
		}
		if err != nil {
			return t, false, fmt.Errorf("livestate: take door trigger: %w", err)
		}
		if err := json.Unmarshal([]byte(v), &t); err != nil {
			return DoorTrigger{}, false, fmt.Errorf("livestate: decode door trigger: %w", err)
		}
		if time.Since(t.At) < DoorTriggerTTL {
			return t, true, nil
		}
	}
}

// doorHookRateScript counts one request in a fixed window and returns the
// count and the window's remaining milliseconds. The expiry is set in the
// same step as the first count, and again when a key somehow lacks one, so a
// crash between the two cannot lock a webhook out for good.
var doorHookRateScript = valkey.NewLuaScript(`
local n = redis.call('INCR', KEYS[1])
local ttl = redis.call('PTTL', KEYS[1])
if n == 1 or ttl < 0 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
  ttl = tonumber(ARGV[1])
end
return {n, ttl}`)

// DoorHookRate counts one trigger of webhook id in the current window and
// returns the count so far and the time left in the window.
func (s *Store) DoorHookRate(ctx context.Context, id int64, window time.Duration) (count int64, left time.Duration, err error) {
	res, err := doorHookRateScript.Exec(ctx, s.c, []string{fmt.Sprintf("%s%d", doorHookRate, id)},
		[]string{fmt.Sprint(window.Milliseconds())}).ToArray()
	if err != nil || len(res) != 2 {
		return 0, 0, fmt.Errorf("livestate: count door trigger: %w", err)
	}
	n, err := res[0].AsInt64()
	if err != nil {
		return 0, 0, fmt.Errorf("livestate: count door trigger: %w", err)
	}
	ms, err := res[1].AsInt64()
	if err != nil {
		return 0, 0, fmt.Errorf("livestate: count door trigger: %w", err)
	}
	return n, time.Duration(ms) * time.Millisecond, nil
}

// DoorHookClaim reserves the right to ring for webhook id for window: it is
// true for the first caller and false for every duplicate within the window.
func (s *Store) DoorHookClaim(ctx context.Context, id int64, window time.Duration) (bool, error) {
	err := s.c.Do(ctx, s.c.B().Set().Key(fmt.Sprintf("%s%d", doorHookRecent, id)).Value("1").Nx().Px(window).Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("livestate: claim door trigger: %w", err)
	}
	return true, nil
}
