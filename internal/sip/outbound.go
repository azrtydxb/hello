package sip

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/emiago/sipgo/sip"
)

// statusText is the reason phrase Hello uses for a status code.
func statusText(code int) string {
	switch code {
	case 200:
		return "OK"
	case 401:
		return "Unauthorized"
	case 403:
		return "Forbidden"
	case 404:
		return "Not Found"
	case 407:
		return "Proxy Authentication Required"
	case 408:
		return "Request Timeout"
	case 480:
		return "Temporarily Unavailable"
	case 486:
		return "Busy Here"
	case 487:
		return "Request Terminated"
	case 500:
		return "Server Internal Error"
	case 502:
		return "Bad Gateway"
	case 503:
		return "Service Unavailable"
	case 504:
		return "Server Time-out"
	case 603:
		return "Decline"
	}
	return "Status " + fmt.Sprint(code)
}

// attemptOutcome is how one trunk attempt ended.
type attemptOutcome int

const (
	attemptAnswered  attemptOutcome = iota
	attemptFailed                   // a final non-2xx response
	attemptTimeout                  // nothing beyond 100 Trying within TrunkAttemptTimeout
	attemptCancelled                // the caller cancelled
)

// setupOutbound tries the decision's candidates in order (S-8): for each
// trunk it takes a call slot (a full trunk is skipped), then sends the
// INVITE to each destination not known down. A failover code or no answer
// moves on to the next destination, then the next trunk; any other final
// response ends the call with that response; the caller's CANCEL stops
// everything. The caller gets the last trunk's answer, or 503 when no
// trunk answered at all. Every attempt is traced.
func (c *call) setupOutbound(dec routing.Decision) {
	defer close(c.setupDone)
	s := c.s
	lastCode, lastReason := 0, ""
	for _, cand := range dec.Candidates {
		t := cand.Trunk
		if t == nil {
			continue
		}
		c.mu.Lock()
		c.trunkName = t.Name
		c.mu.Unlock()
		if ok, why := c.acquireSlot(t, dec.Emergency); !ok {
			c.addTrace(fmt.Sprintf("Trunk %s skipped: %s", t.Name, why))
			if why == "full" {
				s.m.TrunkCalls.WithLabelValues(t.Name, TrunkFull).Inc()
			}
			continue
		}
		for _, d := range cand.Destinations {
			if s.trunks.destinationDown(t.ID, d) && !dec.Emergency {
				c.addTrace(fmt.Sprintf("Trunk %s destination %s:%d skipped: down", t.Name, d.Host, d.Port))
				continue
			}
			addr := s.trunks.destAddr(d)
			c.mu.Lock()
			l := c.addLeg(&leg{trunk: t, dest: d, addr: addr, number: dec.Number, callerID: dec.CallerID})
			c.mu.Unlock()
			go l.run()
			outcome, code, reason := c.awaitAttempt(l)
			switch outcome {
			case attemptAnswered:
				c.addTrace(fmt.Sprintf("%s -> 200 OK", t.Name))
				s.m.TrunkCalls.WithLabelValues(t.Name, TrunkAnswered).Inc()
				c.answer(l)
				return
			case attemptCancelled:
				c.addTrace(fmt.Sprintf("%s (%s) -> cancelled by the caller; no further trunks tried", t.Name, addr))
				s.m.TrunkCalls.WithLabelValues(t.Name, TrunkCancelled).Inc()
				c.cancelForks(nil)
				c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
				return
			case attemptTimeout:
				c.addTrace(fmt.Sprintf("%s (%s) -> no answer", t.Name, addr))
				c.addTrace("Failover permitted for no answer")
				s.m.TrunkCalls.WithLabelValues(t.Name, TrunkFailover).Inc()
				lastCode, lastReason = 0, ""
				continue
			}
			if reason == "" {
				reason = statusText(code)
			}
			c.addTrace(fmt.Sprintf("%s (%s) -> %d %s", t.Name, addr, code, reason))
			if !slices.Contains(failoverCodes(dec), code) {
				c.addTrace(fmt.Sprintf("No failover for %d", code))
				s.m.TrunkCalls.WithLabelValues(t.Name, TrunkRejected).Inc()
				c.finishOutbound(code, reason)
				return
			}
			c.addTrace(fmt.Sprintf("Failover permitted for %d", code))
			s.m.TrunkCalls.WithLabelValues(t.Name, TrunkFailover).Inc()
			lastCode, lastReason = code, reason
		}
		c.releaseSlot() // this trunk did not take the call
	}
	if lastCode == 0 {
		lastCode, lastReason = sip.StatusServiceUnavailable, statusText(sip.StatusServiceUnavailable)
	}
	c.finishOutbound(lastCode, lastReason)
}

// failoverCodes are the decision's, or the spec defaults.
func failoverCodes(dec routing.Decision) []int {
	if len(dec.FailoverCodes) > 0 {
		return dec.FailoverCodes
	}
	return []int{408, 480, 500, 502, 503, 504}
}

// finishOutbound answers the caller with a final failure and ends the call;
// a cancellation that raced it wins (the transaction layer sent 487).
func (c *call) finishOutbound(code int, reason string) {
	if !c.closeSetup() {
		return
	}
	c.mu.Lock()
	cancelled := c.isCancelled
	c.mu.Unlock()
	if cancelled {
		c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
		return
	}
	result := ResultFailed
	switch {
	case isBusy(code):
		result = ResultBusy
	case code == sip.StatusServiceUnavailable:
		result = ResultUnavailable
	}
	c.respondA(code, reason)
	c.end(code, cdr.SideCallee, "no trunk connected the call", result)
}

// awaitAttempt waits for one trunk leg's outcome. Ringing is relayed to
// the caller; a challenge is traced (the leg answers it itself). A leg that
// stays silent beyond TrunkAttemptTimeout is abandoned: if it answers later
// it is ACKed and hung up.
func (c *call) awaitAttempt(l *leg) (attemptOutcome, int, string) {
	timer := time.NewTimer(c.s.cfg.TrunkAttemptTimeout)
	defer timer.Stop()
	silent := timer.C
	for {
		select {
		case ev := <-c.events:
			if ev.leg != l {
				continue // a leg already failed over
			}
			switch ev.kind {
			case evRinging:
				silent = nil
				c.ringing()
			case evChallenged:
				silent = nil
				c.addTrace(fmt.Sprintf("%s (%s) -> %d %s; answered with the trunk credentials (credentials: configured)",
					l.trunk.Name, l.addr, ev.code, ev.reason))
			case evAnswered:
				return attemptAnswered, sip.StatusOK, ""
			case evFailed:
				return attemptFailed, ev.code, ev.reason
			}
		case <-c.canceled:
			if !c.closeSetup() {
				continue // the leg already won; answer() sees the cancel
			}
			l.cancel()
			return attemptCancelled, 0, ""
		case <-silent:
			if c.abandon(l) {
				return attemptTimeout, 0, ""
			}
			silent = nil // it won just now: its answer event follows
		}
	}
}

// acquireSlot counts this call against trunk t (S-9). An emergency call
// ignores the limit and is never blocked by the trunk state being
// unreachable.
func (c *call) acquireSlot(t *routing.Trunk, emergency bool) (bool, string) {
	st := c.s.deps.Trunks
	if st == nil {
		if emergency {
			return true, ""
		}
		return false, "state unavailable"
	}
	limit := t.MaxCalls
	if emergency {
		limit = 0
	}
	ctx, cancel := c.s.stateCtx()
	ok, err := st.AcquireTrunkCall(ctx, t.ID, c.id, limit, c.s.cfg.CallTTL)
	cancel()
	switch {
	case err != nil && emergency:
		return true, ""
	case err != nil:
		c.s.log.Warn("trunk state unavailable", "op", "acquire_slot", "error", err)
		return false, "state unavailable"
	case !ok:
		return false, "full"
	}
	c.mu.Lock()
	c.slotTrunk = t.ID
	c.mu.Unlock()
	return true, ""
}

func (c *call) releaseSlot() {
	c.mu.Lock()
	id := c.slotTrunk
	c.slotTrunk = 0
	c.mu.Unlock()
	if id == 0 || c.s.deps.Trunks == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := c.s.deps.Trunks.ReleaseTrunkCall(ctx, id, c.id); err != nil {
		c.s.log.Warn("could not release trunk slot", "correlation_id", c.id, "error", err)
	}
}

func (c *call) refreshSlot() {
	c.mu.Lock()
	id := c.slotTrunk
	c.mu.Unlock()
	if id == 0 || c.s.deps.Trunks == nil {
		return
	}
	ctx, cancel := c.s.stateCtx()
	defer cancel()
	if err := c.s.deps.Trunks.RefreshTrunkCall(ctx, id, c.id, c.s.cfg.CallTTL); err != nil {
		c.s.log.Warn("could not refresh trunk slot", "correlation_id", c.id, "error", err)
	}
}
