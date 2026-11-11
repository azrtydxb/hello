package crm

import (
	"context"
	"time"

	"github.com/valkey-io/valkey-go"
)

// ValkeyCache is the lookup Cache on Valkey. Client returns the connected
// client or nil while it is connecting; every operation is bounded to 200 ms
// and a failure is a miss, so a slow Valkey never delays a lookup.
type ValkeyCache struct {
	Client func() valkey.Client
}

const cacheOpTimeout = 200 * time.Millisecond

// Get implements Cache.
func (c ValkeyCache) Get(ctx context.Context, key string) ([]byte, bool) {
	vk := c.Client()
	if vk == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
	defer cancel()
	b, err := vk.Do(ctx, vk.B().Get().Key(key).Build()).AsBytes()
	if err != nil {
		return nil, false
	}
	return b, true
}

// Set implements Cache.
func (c ValkeyCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration) {
	vk := c.Client()
	if vk == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, cacheOpTimeout)
	defer cancel()
	_ = vk.Do(ctx, vk.B().Set().Key(key).Value(string(val)).Px(ttl).Build()).Error()
}
