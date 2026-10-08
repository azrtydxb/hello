package ai

import (
	"context"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/config"
)

// interactiveWait is how long an interactive call waits for a slot before
// ai_busy (spec S-16).
const interactiveWait = 5 * time.Second

// limits bounds one replica's model calls (spec S-16): at most
// MaxConcurrency in flight and RequestsPerMinute starts (a token bucket
// refilled continuously).
type limits struct {
	slots chan struct{}
	now   func() time.Time

	mu     sync.Mutex
	tokens float64
	max    float64
	perSec float64
	last   time.Time
}

func newLimits(cfg config.AIAgent, now func() time.Time) *limits {
	n, rpm := max(cfg.MaxConcurrency, 1), max(cfg.RequestsPerMinute, 1)
	return &limits{slots: make(chan struct{}, n), now: now, tokens: float64(rpm), max: float64(rpm), perSec: float64(rpm) / 60, last: now()}
}

// inUse is the calls in flight.
func (l *limits) inUse() int { return len(l.slots) }

// takeToken takes a start from the bucket or reports how long until one
// is there.
func (l *limits) takeToken() (wait time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.tokens = min(l.max, l.tokens+now.Sub(l.last).Seconds()*l.perSec)
	l.last = now
	if l.tokens >= 1 {
		l.tokens--
		return 0
	}
	return time.Duration((1 - l.tokens) / l.perSec * float64(time.Second))
}

// acquire takes a slot and a start. Background calls never wait; an
// interactive call waits at most interactiveWait. It fails with ai_busy,
// and reason names the bound that refused (concurrency or rate).
func (l *limits) acquire(ctx context.Context, background bool) (release func(), reason string, err error) {
	wait := interactiveWait
	if background {
		wait = 0
	}
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	busy := func(reason string) (func(), string, error) {
		return nil, reason, &Error{Code: CodeBusy, Message: "the AI service is busy (" + reason + "); try again shortly"}
	}

	select {
	case l.slots <- struct{}{}:
	default:
		if background {
			return busy("concurrency")
		}
		select {
		case l.slots <- struct{}{}:
		case <-deadline.C:
			return busy("concurrency")
		case <-ctx.Done():
			return nil, "", ctx.Err()
		}
	}
	release = func() { <-l.slots }
	for {
		d := l.takeToken()
		if d == 0 {
			return release, "", nil
		}
		if background {
			release()
			return busy("rate")
		}
		t := time.NewTimer(d)
		select {
		case <-t.C:
		case <-deadline.C:
			t.Stop()
			release()
			return busy("rate")
		case <-ctx.Done():
			t.Stop()
			release()
			return nil, "", ctx.Err()
		}
	}
}
