package prov

import (
	"context"
	"crypto/rand"
	"log/slog"
	"net/netip"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Limits are the provisioning rate limits (spec S-15, HELLO_PROV_RATE_*).
// A limit of 0 or less is not enforced.
type Limits struct {
	IPPerMin       int // requests per source IP per minute
	DeniedPer10Min int // denied requests per source IP per 10 minutes, then a block
	PhonePerHour   int // config files per phone per hour
}

// Rate-limit windows and the block that follows too many denials.
const (
	ipWindow     = time.Minute
	deniedWindow = 10 * time.Minute
	phoneWindow  = time.Hour
	BlockFor     = 10 * time.Minute
	// valkeyTimeout bounds every limiter call, so a slow Valkey delays a
	// request by at most this much before the in-memory fallback decides.
	valkeyTimeout = 50 * time.Millisecond
)

// Valkey keys (spec Data).
const (
	keyIP     = "hello:prov:rl:ip:"
	keyDenied = "hello:prov:rl:denied:"
	keyPhone  = "hello:prov:rl:phone:"
	keyBlock  = "hello:prov:block:"
)

// Limiter enforces the limits cluster-wide in Valkey: sliding windows kept
// as sorted sets of request times in Valkey's own clock, so replicas with
// skewed clocks agree. When Valkey does not answer within 50 ms, the
// replica enforces the same limits in memory; it never fails open.
type Limiter struct {
	c       valkey.Client // nil: memory only
	lim     Limits
	log     *slog.Logger
	mem     *memLimiter
	prefix  string
	seq     atomic.Uint64
	degrade atomic.Bool
}

// NewLimiter returns a limiter on c (nil for memory only).
func NewLimiter(c valkey.Client, lim Limits, log *slog.Logger) *Limiter {
	return &Limiter{c: c, lim: lim, log: log, mem: newMemLimiter(), prefix: rand.Text()[:8]}
}

// slideLua is a sliding-window log: it drops entries older than the
// window, refuses when the window is full, otherwise records this request.
// A refused request is not recorded, so the set never outgrows the limit.
const slideLua = `
local function slide(key, limit, win, member)
  local t = redis.call('TIME')
  local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
  redis.call('ZREMRANGEBYSCORE', key, '-inf', now - win)
  if redis.call('ZCARD', key) >= limit then return 0 end
  redis.call('ZADD', key, now, now .. ':' .. member)
  redis.call('PEXPIRE', key, win)
  return 1
end
`

// requestScript: KEYS[1] block, KEYS[2] window; 0 when blocked or full.
var requestScript = valkey.NewLuaScript(slideLua + `
if redis.call('EXISTS', KEYS[1]) == 1 then return 0 end
return slide(KEYS[2], tonumber(ARGV[1]), tonumber(ARGV[2]), ARGV[3])`)

// deniedScript: KEYS[1] window, KEYS[2] block; a denial past the limit
// sets the block for ARGV[4] ms.
var deniedScript = valkey.NewLuaScript(slideLua + `
if slide(KEYS[1], tonumber(ARGV[1]), tonumber(ARGV[2]), ARGV[3]) == 1 then return 1 end
redis.call('SET', KEYS[2], '1', 'PX', ARGV[4])
return 0`)

func ms(d time.Duration) string { return strconv.FormatInt(d.Milliseconds(), 10) }

func (l *Limiter) member() string { return l.prefix + strconv.FormatUint(l.seq.Add(1), 36) }

// exec runs a script with the 50 ms bound; ok is false when Valkey failed.
func (l *Limiter) exec(ctx context.Context, s *valkey.Lua, keys, args []string) (allowed, ok bool) {
	if l.c == nil {
		return false, false
	}
	ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
	defer cancel()
	n, err := s.Exec(ctx, l.c, keys, args).AsInt64()
	if err != nil {
		if !l.degrade.Swap(true) {
			l.log.Warn("provisioning rate limits fall back to memory: valkey unavailable", "error", err)
		}
		return false, false
	}
	if l.degrade.Swap(false) {
		l.log.Info("provisioning rate limits back on valkey")
	}
	return n == 1, true
}

// Request counts a request from ip and reports whether it may proceed:
// false while ip is blocked or past its per-minute limit.
func (l *Limiter) Request(ctx context.Context, ip netip.Addr) bool {
	if l.lim.IPPerMin <= 0 {
		return !l.blocked(ctx, ip)
	}
	k := ip.String()
	if allowed, ok := l.exec(ctx, requestScript, []string{keyBlock + k, keyIP + k},
		[]string{strconv.Itoa(l.lim.IPPerMin), ms(ipWindow), l.member()}); ok {
		return allowed
	}
	return l.mem.request(k, l.lim.IPPerMin, time.Now())
}

func (l *Limiter) blocked(ctx context.Context, ip netip.Addr) bool {
	k := ip.String()
	if l.c != nil {
		ctx, cancel := context.WithTimeout(ctx, valkeyTimeout)
		defer cancel()
		n, err := l.c.Do(ctx, l.c.B().Exists().Key(keyBlock+k).Build()).AsInt64()
		if err == nil {
			return n == 1
		}
	}
	return l.mem.isBlocked(k, time.Now())
}

// Denied counts a denied request from ip; past the limit, ip is blocked
// for BlockFor.
func (l *Limiter) Denied(ctx context.Context, ip netip.Addr) {
	if l.lim.DeniedPer10Min <= 0 {
		return
	}
	k := ip.String()
	if _, ok := l.exec(ctx, deniedScript, []string{keyDenied + k, keyBlock + k},
		[]string{strconv.Itoa(l.lim.DeniedPer10Min), ms(deniedWindow), l.member(), ms(BlockFor)}); ok {
		return
	}
	l.mem.denied(k, l.lim.DeniedPer10Min, time.Now())
}

// Phone counts a config file for the phone and reports whether it may be
// served.
func (l *Limiter) Phone(ctx context.Context, phoneID int64) bool {
	if l.lim.PhonePerHour <= 0 {
		return true
	}
	k := strconv.FormatInt(phoneID, 10)
	if allowed, ok := l.exec(ctx, requestScript, []string{keyBlock + "phone:" + k, keyPhone + k},
		[]string{strconv.Itoa(l.lim.PhonePerHour), ms(phoneWindow), l.member()}); ok {
		return allowed
	}
	return l.mem.window("phone:"+k, l.lim.PhonePerHour, phoneWindow, time.Now())
}

// memLimiter is the per-replica fallback with the same windows.
type memLimiter struct {
	mu        sync.Mutex
	windows   map[string][]time.Time
	blocks    map[string]time.Time
	lastSweep time.Time
}

func newMemLimiter() *memLimiter {
	return &memLimiter{windows: map[string][]time.Time{}, blocks: map[string]time.Time{}}
}

func (m *memLimiter) request(ip string, limit int, now time.Time) bool {
	if m.isBlocked(ip, now) {
		return false
	}
	return m.window("ip:"+ip, limit, ipWindow, now)
}

func (m *memLimiter) isBlocked(ip string, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	until, ok := m.blocks[ip]
	return ok && now.Before(until)
}

func (m *memLimiter) denied(ip string, limit int, now time.Time) {
	if !m.window("denied:"+ip, limit, deniedWindow, now) {
		m.mu.Lock()
		m.blocks[ip] = now.Add(BlockFor)
		m.mu.Unlock()
	}
}

// window is the in-memory sliding-window log.
func (m *memLimiter) window(key string, limit int, win time.Duration, now time.Time) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweep(now)
	ts := m.windows[key]
	cut := 0
	for cut < len(ts) && !ts[cut].After(now.Add(-win)) {
		cut++
	}
	ts = ts[cut:]
	if len(ts) >= limit {
		m.windows[key] = ts
		return false
	}
	m.windows[key] = append(ts, now)
	return true
}

// sweep drops idle windows and ended blocks once a minute, so source
// addresses seen once do not accumulate.
func (m *memLimiter) sweep(now time.Time) {
	if now.Sub(m.lastSweep) < time.Minute {
		return
	}
	m.lastSweep = now
	for k, ts := range m.windows {
		if len(ts) == 0 || now.Sub(ts[len(ts)-1]) > phoneWindow {
			delete(m.windows, k)
		}
	}
	for k, until := range m.blocks {
		if !now.Before(until) {
			delete(m.blocks, k)
		}
	}
}
