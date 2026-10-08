package oauth

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

// Throttle limits (spec S-9, S-10).
const (
	clientFailLimit  = 10
	clientFailWindow = time.Minute
	registerLimit    = 10
	registerWindow   = time.Hour
	// keyPrefix prefixes every throttle key in Valkey.
	keyPrefix = "hello:oauth:rl:"
	// valkeyTimeout bounds a throttle call before the memory fallback
	// decides.
	valkeyTimeout = 50 * time.Millisecond
)

// slideScript is a sliding-window log in Valkey's own clock, so replicas
// with skewed clocks agree: it drops entries older than the window and
// refuses when the window is full, otherwise records the hit.
var slideScript = valkey.NewLuaScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local win = tonumber(ARGV[2])
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', now - win)
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[1]) then return 0 end
redis.call('ZADD', KEYS[1], now, now .. ':' .. ARGV[3])
redis.call('PEXPIRE', KEYS[1], win)
return 1`)

// valkeyLimiter is the Limiter of the authorization server: Valkey when it
// answers within 50 ms, the same windows per replica in memory otherwise.
// It never fails open.
type valkeyLimiter struct {
	client  func() valkey.Client
	log     *slog.Logger
	prefix  string
	seq     atomic.Uint64
	degrade atomic.Bool

	mu  sync.Mutex
	mem map[string][]time.Time
}

// NewLimiter returns the throttle on the client get returns at each call
// (nil get, or a nil client, limits in memory).
func NewLimiter(get func() valkey.Client, log *slog.Logger) Limiter {
	if get == nil {
		get = func() valkey.Client { return nil }
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &valkeyLimiter{client: get, log: log, prefix: rand.Text()[:8], mem: map[string][]time.Time{}}
}

// Allow implements Limiter.
func (l *valkeyLimiter) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	if c := l.client(); c != nil {
		vctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
		n, err := slideScript.Exec(vctx, c, []string{keyPrefix + key},
			[]string{strconv.Itoa(limit), strconv.FormatInt(window.Milliseconds(), 10),
				l.prefix + strconv.FormatUint(l.seq.Add(1), 36)}).AsInt64()
		cancel()
		if err == nil {
			if l.degrade.Swap(false) {
				l.log.Info("oauth throttle back on valkey")
			}
			return n == 1, nil
		}
		if !l.degrade.Swap(true) {
			l.log.Warn("oauth throttle falls back to memory: valkey unavailable", "error", err)
		}
	}
	return l.memAllow(key, limit, window, time.Now()), nil
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
	if len(l.mem) > 10000 {
		l.sweep(now)
	}
	return true
}

// sweep drops keys whose newest hit is older than the longest window.
func (l *valkeyLimiter) sweep(now time.Time) {
	for k, hits := range l.mem {
		if len(hits) == 0 || now.Sub(hits[len(hits)-1]) > registerWindow {
			delete(l.mem, k)
		}
	}
}
