// Presence: per-device dialog state published by the owning hello-sip node
// and read by hello-control (spec contract 3).
package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/valkey-io/valkey-go"
)

// DeviceState is one device's presence.
type DeviceState struct {
	Device    string    `json:"device"`
	Extension string    `json:"extension"`
	State     string    `json:"state"` // idle | ringing | on-call | dnd
	UpdatedAt time.Time `json:"updatedAt"`
}

const presencePrefix = "hello:presence:"

// DeviceStateKeyspace is the SCAN pattern for presence keys.
const DeviceStateKeyspace = presencePrefix + "*"

// SetDeviceState publishes s with the given TTL.
func (s *Store) SetDeviceState(ctx context.Context, sds DeviceState, ttl time.Duration) error {
	b, err := json.Marshal(sds)
	if err != nil {
		return err
	}
	return s.c.Do(ctx, s.c.B().Set().Key(presencePrefix+sds.Device).Value(string(b)).Px(ttl).Build()).Error()
}

// DeviceStates lists every live presence record.
func (s *Store) DeviceStates(ctx context.Context) ([]DeviceState, error) {
	var keys []string
	var cursor uint64
	for {
		e, err := s.c.Do(ctx, s.c.B().Scan().Cursor(cursor).Match(DeviceStateKeyspace).Count(200).Build()).AsScanEntry()
		if err != nil {
			return nil, fmt.Errorf("livestate: presence scan: %w", err)
		}
		keys = append(keys, e.Elements...)
		if e.Cursor == 0 {
			break
		}
		cursor = e.Cursor
	}
	out := make([]DeviceState, 0, len(keys))
	for _, k := range keys {
		v, err := s.c.Do(ctx, s.c.B().Get().Key(k).Build()).ToString()
		if valkey.IsValkeyNil(err) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("livestate: presence get: %w", err)
		}
		var sds DeviceState
		if err := json.Unmarshal([]byte(v), &sds); err != nil {
			return nil, fmt.Errorf("livestate: presence decode: %w", err)
		}
		out = append(out, sds)
	}
	return out, nil
}
