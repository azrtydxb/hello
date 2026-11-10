package webhook

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/safehttp"
)

// Delays are the waits before each retry: the first attempt is immediate,
// then a failed delivery is tried again after 10 s, 30 s, 2 min, 10 min,
// 1 h, 4 h and 12 h (each jittered by plus or minus 20 percent), so a
// delivery gets eight attempts over about 17 hours before it is dead.
var Delays = []time.Duration{
	10 * time.Second, 30 * time.Second, 2 * time.Minute, 10 * time.Minute,
	time.Hour, 4 * time.Hour, 12 * time.Hour,
}

// MaxAttempts is the attempts a delivery gets in one series.
var MaxAttempts = len(Delays) + 1

// MaxRetryAfter caps how long a receiver's Retry-After may postpone a retry.
const MaxRetryAfter = time.Hour

// MaxConsecutiveFailures is the failed attempts in a row after which an
// endpoint is disabled.
const MaxConsecutiveFailures = 50

// RetryDelay is the wait before the next attempt after n attempts of the
// series have failed (n starts at 1); ok is false when the series is spent.
// rnd returns a uniform value in [0, 1) and spreads the delay by +-20%.
func RetryDelay(n int, rnd func() float64) (time.Duration, bool) {
	if n < 1 || n > len(Delays) {
		return 0, false
	}
	d := Delays[n-1]
	if rnd != nil {
		d = time.Duration(float64(d) * (0.8 + 0.4*rnd()))
	}
	return d, true
}

// Outcome classifies one attempt.
type Outcome int

// The outcomes of an attempt.
const (
	// OutcomeSuccess is a 2xx.
	OutcomeSuccess Outcome = iota
	// OutcomeRetry is a network error, 5xx, 408 or 429: try again later.
	OutcomeRetry
	// OutcomeDead is a failure retrying cannot fix: any other status (a 4xx,
	// or a redirect, which is never followed) or a refused address.
	OutcomeDead
	// OutcomeGone is a 410: the receiver says the endpoint is gone. The
	// delivery is dead and the endpoint is disabled.
	OutcomeGone
)

// Classify maps an HTTP status (0 when no response came) and transport error
// to an Outcome.
func Classify(status int, err error) Outcome {
	if err != nil {
		if errors.Is(err, safehttp.ErrBlockedAddress) {
			return OutcomeDead
		}
		return OutcomeRetry
	}
	switch {
	case status >= 200 && status < 300:
		return OutcomeSuccess
	case status == http.StatusGone:
		return OutcomeGone
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests, status >= 500:
		return OutcomeRetry
	}
	return OutcomeDead
}

// ErrorText is the short, secret-free description stored for a failed
// attempt: never the raw transport error, which can carry the URL.
func ErrorText(status int, err error) string {
	if err != nil {
		switch {
		case errors.Is(err, safehttp.ErrBlockedAddress):
			return "blocked_address"
		case isTimeout(err):
			return "timeout"
		}
		return "connection_error"
	}
	if status >= 300 && status < 400 {
		return "redirect_not_followed (http " + strconv.Itoa(status) + ")"
	}
	return "http " + strconv.Itoa(status)
}

func isTimeout(err error) bool {
	var t interface{ Timeout() bool }
	return errors.As(err, &t) && t.Timeout()
}

// ParseRetryAfter reads a Retry-After header (seconds or an HTTP date); zero
// when absent or invalid. The result is capped at MaxRetryAfter.
func ParseRetryAfter(v string, now time.Time) time.Duration {
	if v == "" {
		return 0
	}
	var d time.Duration
	if n, err := strconv.Atoi(v); err == nil {
		d = time.Duration(n) * time.Second
	} else if t, err := http.ParseTime(v); err == nil {
		d = t.Sub(now)
	}
	return min(max(d, 0), MaxRetryAfter)
}
