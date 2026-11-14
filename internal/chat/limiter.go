package chat

import (
	"context"
	"crypto/rand"
	"log/slog"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Limiter is the sliding-window throttle every abuse limit of the widget
// uses (spec M-29a.2): Valkey, so the replicas share one count, and the same
// windows per replica in memory when Valkey does not answer within 50 ms. It
// never fails open.
type Limiter interface {
	// Allow records one hit on key and reports whether it is within limit
	// hits per window.
	Allow(ctx context.Context, key string, limit int, window time.Duration) bool
}

const (
	keyPrefix     = "hello:chat:rl:"
	valkeyTimeout = 50 * time.Millisecond
	memMaxKeys    = 20000
)

var slideScript = valkey.NewLuaScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local win = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - win)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[1]) then return 0 end
redis.call('ZADD', KEYS[1], now, now .. ':' .. ARGV[3])
redis.call('PEXPIRE', KEYS[1], win)
return 1`)

type valkeyLimiter struct {
	client  func() valkey.Client
	log     *slog.Logger
	prefix  string
	seq     atomic.Uint64
	degrade atomic.Bool

	// run executes the script; tests replace it.
	run func(ctx context.Context, c valkey.Client, key string, limit int, window time.Duration, member string) (int64, error)

	mu  sync.Mutex
	mem map[string][]time.Time
}

// NewLimiter returns the limiter on the client get returns at each call (nil
// get, or a nil client, limits in memory).
func NewLimiter(get func() valkey.Client, log *slog.Logger) Limiter {
	if get == nil {
		get = func() valkey.Client { return nil }
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &valkeyLimiter{client: get, log: log, prefix: rand.Text()[:8], mem: map[string][]time.Time{}, run: runSlide}
}

func runSlide(ctx context.Context, c valkey.Client, key string, limit int, window time.Duration, member string) (int64, error) {
	return slideScript.Exec(ctx, c, []string{keyPrefix + key},
		[]string{strconv.Itoa(limit), strconv.FormatInt(window.Milliseconds(), 10), member}).AsInt64()
}

// Allow implements Limiter.
func (l *valkeyLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) bool {
	if c := l.client(); c != nil {
		vctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
		n, err := l.run(vctx, c, key, limit, window, l.prefix+strconv.FormatUint(l.seq.Add(1), 36))
		cancel()
		if err == nil {
			if l.degrade.Swap(false) {
				l.log.Info("chat limits back on valkey")
			}
			if n != 1 {
				return false
			}
			// Mirror the hit in memory, so a later call that falls back
			// there already counts it: memory only ever holds a subset of
			// the hits, so it can only make a limit stricter.
			return l.memAllow(key, limit, window, time.Now())
		}
		if !l.degrade.Swap(true) {
			l.log.Warn("chat limits fall back to memory: valkey unavailable", "error", err)
		}
	}
	return l.memAllow(key, limit, window, time.Now())
}

func (l *valkeyLimiter) memAllow(key string, limit int, window time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.mem[key][:0]
	for _, t := range l.mem[key] {
		if now.Sub(t) < window {
			hits = append(hits, t)
		}
	}
	if len(hits) >= limit {
		l.mem[key] = hits
		return false
	}
	l.mem[key] = append(hits, now)
	if len(l.mem) > memMaxKeys {
		l.sweep(now)
	}
	return true
}

// sweep drops keys whose newest hit is older than a day, the longest window.
func (l *valkeyLimiter) sweep(now time.Time) {
	for k, hits := range l.mem {
		if len(hits) == 0 || now.Sub(hits[len(hits)-1]) > 25*time.Hour {
			delete(l.mem, k)
		}
	}
}
