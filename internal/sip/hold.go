// Hold (S-1): a re-INVITE whose SDP stops sending media holds the call; the
// B2BUA relays the offer body unchanged, so both phones see the hold, and
// hello_hold_active counts the calls currently held on this node.
package sip

import "strings"

// sdpHeld reports whether an SDP body puts the media on hold: a sendonly or
// inactive direction attribute, or a zero connection address.
func sdpHeld(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		switch {
		case strings.HasPrefix(line, "a=sendonly"), strings.HasPrefix(line, "a=inactive"):
			return true
		case strings.HasPrefix(line, "c=IN IP4 0.0.0.0"), strings.HasPrefix(line, "c=IN IP6 ::"):
			return true
		}
	}
	return false
}

// setHeld moves the call in or out of hold exactly once per transition, so
// the gauge counts calls, not re-INVITEs.
func (c *call) setHeld(held bool) {
	c.mu.Lock()
	was := c.held
	if was == held || c.ended {
		c.mu.Unlock()
		return
	}
	c.held = held
	c.mu.Unlock()
	// A recording pauses while the call is held and resumes after (spec
	// edge case "Re-INVITE hold during recording": hold silence is not
	// captured).
	c.mu.Lock()
	rec := c.rec
	c.mu.Unlock()
	if rec != nil {
		rec.setPaused(held)
	}
	if held {
		c.s.m.HoldActive.Inc()
		c.addTrace("Call held")
		return
	}
	c.s.m.HoldActive.Dec()
	c.addTrace("Call resumed")
}
