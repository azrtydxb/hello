package sip

import (
	"sync/atomic"
	"time"
)

// tunable is a package-level duration that tests shorten. It is atomic
// because a call or takeover goroutine of an earlier test can still be
// reading it while the next test sets it.
type tunable struct{ ns atomic.Int64 }

func newTunable(d time.Duration) *tunable {
	t := &tunable{}
	t.ns.Store(int64(d))
	return t
}

// Get is the current value.
func (t *tunable) Get() time.Duration { return time.Duration(t.ns.Load()) }

// Set replaces the value (tests).
func (t *tunable) Set(d time.Duration) { t.ns.Store(int64(d)) }
