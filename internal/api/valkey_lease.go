package api

import (
	"context"
	"strconv"
	"time"

	"github.com/valkey-io/valkey-go"
)

// renewScript extends a lease only while holder still owns it.
var renewScript = valkey.NewLuaScript(`if redis.call("GET", KEYS[1]) == ARGV[1] then return redis.call("PEXPIRE", KEYS[1], ARGV[2]) end return 0`)

// AcquireLease takes key for holder for ttl unless someone else holds it,
// or extends it when holder already does. It reports whether holder holds
// the lease afterwards. Background jobs that one replica runs (the fetch
// audit pruner, the redirect worker) take one.
func (l *LazyValkey) AcquireLease(ctx context.Context, key, holder string, ttl time.Duration) (bool, error) {
	c, err := l.get()
	if err != nil {
		return false, err
	}
	n, err := renewScript.Exec(ctx, c.c, []string{key}, []string{holder, valkeyMillis(ttl)}).AsInt64()
	if err != nil {
		return false, err
	}
	if n == 1 {
		return true, nil
	}
	err = c.c.Do(ctx, c.c.B().Set().Key(key).Value(holder).Nx().Px(ttl).Build()).Error()
	if valkey.IsValkeyNil(err) {
		return false, nil // held by someone else
	}
	return err == nil, err
}

// ReleaseLease gives key up if holder holds it.
func (l *LazyValkey) ReleaseLease(ctx context.Context, key, holder string) error {
	c, err := l.get()
	if err != nil {
		return err
	}
	return unlockScript.Exec(ctx, c.c, []string{key}, []string{holder}).Error()
}

func valkeyMillis(d time.Duration) string {
	return strconv.FormatInt(d.Milliseconds(), 10)
}
