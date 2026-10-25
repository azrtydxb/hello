package phonebook

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
)

// RenderKeyPrefix starts the Valkey key of a rendered directory:
// hello:pb:render:<vendor>:<hash> (spec P-11). The hash covers the vendor,
// the slot and the version of the phone's delivered set, so a change to any
// book of the set names a new key and the old one simply expires.
const RenderKeyPrefix = "hello:pb:render:"

// cacheTimeout bounds every cache call, so a slow Valkey costs a request at
// most this much before it is rendered from PostgreSQL instead.
const cacheTimeout = 50 * time.Millisecond

// Cache keeps rendered directories in Valkey, shared by every replica. It
// holds no truth: the version in the key comes from PostgreSQL, so a Valkey
// flush or outage loses nothing but the saved render.
type Cache struct {
	client  func() valkey.Client // returns nil while Valkey is connecting
	log     *slog.Logger
	timeout time.Duration
	down    atomic.Bool
}

// NewCache returns a cache on the client get returns at each call: nil
// while Valkey is still connecting, which makes every Get a miss.
func NewCache(get func() valkey.Client, log *slog.Logger) *Cache {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Cache{client: get, log: log, timeout: cacheTimeout}
}

// Get returns the directory cached under key.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, bool) {
	cl := c.client()
	if cl == nil {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	b, err := cl.Do(ctx, cl.B().Get().Key(key).Build()).AsBytes()
	if err != nil {
		if !valkey.IsValkeyNil(err) {
			c.degraded(err)
		} else {
			c.recovered()
		}
		return nil, false
	}
	c.recovered()
	return b, true
}

// Set caches body under key for ttl. A failure is logged once per outage
// and otherwise ignored.
func (c *Cache) Set(ctx context.Context, key string, body []byte, ttl time.Duration) {
	cl := c.client()
	if cl == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	if err := cl.Do(ctx, cl.B().Set().Key(key).Value(valkey.BinaryString(body)).Ex(ttl).Build()).Error(); err != nil {
		c.degraded(err)
		return
	}
	c.recovered()
}

func (c *Cache) degraded(err error) {
	if c.down.CompareAndSwap(false, true) {
		c.log.Warn("directory cache unavailable; rendering from the database", "error", err)
	}
}

func (c *Cache) recovered() {
	if c.down.CompareAndSwap(true, false) {
		c.log.Info("directory cache available again")
	}
}
