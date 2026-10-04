// Ring and hunt groups (S-6, S-7). The strategy orderings are pure
// functions over plain members, so they unit-test as tables; the fork
// integration (ringGroup) applies them to live bindings and carries the
// failure destination after the last member.
package sip

import (
	"fmt"
	"hash/fnv"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// groupMember is the strategy input for one group member.
type groupMember struct {
	ext     string
	weight  int
	delay   int
	dnd     bool
	lastEnd time.Time // zero: never called (oldest possible for longest-idle)
}

// eligible drops DND members a group must not wake.
func eligible(ms []groupMember, ignoreDND bool) []groupMember {
	if ignoreDND {
		return ms
	}
	out := make([]groupMember, 0, len(ms))
	for _, m := range ms {
		if !m.dnd {
			out = append(out, m)
		}
	}
	return out
}

// orderGroup returns ms in ring order for the strategy. rot is the
// round-robin rotation counter (advanced per call, per group); seed makes
// the weighted order deterministic per call. DND members not ignored are
// always skipped.
func orderGroup(strategy string, ms []groupMember, ignoreDND bool, rot *atomic.Uint64, seed uint64) []groupMember {
	ms = eligible(ms, ignoreDND)
	switch strategy {
	case "round-robin":
		return orderRoundRobin(ms, rot)
	case "longest-idle":
		return orderLongestIdle(ms)
	case "weighted":
		return orderWeighted(ms, seed)
	default: // ring-all and sequential both keep position order
		return ms
	}
}

// orderRoundRobin starts at the rotation counter and wraps; each call moves
// the start for the next one. With no members nothing rotates.
func orderRoundRobin(ms []groupMember, rot *atomic.Uint64) []groupMember {
	if len(ms) == 0 {
		return ms
	}
	n := uint64(len(ms))
	start := rot.Load() % n
	rot.Add(1)
	out := make([]groupMember, 0, len(ms))
	for i := uint64(0); i < n; i++ {
		out = append(out, ms[(start+i)%n])
	}
	return out
}

// orderLongestIdle puts the member whose last call ended longest ago (or
// never) first.
func orderLongestIdle(ms []groupMember) []groupMember {
	out := append([]groupMember(nil), ms...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].lastEnd.Before(out[j-1].lastEnd); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// orderWeighted picks members one after another, an entry's chance of coming
// next being its weight over the remaining total, drawn from seed: the same
// call always orders the same way, different calls spread by weight (the
// Phase 2 seeded approach).
func orderWeighted(ms []groupMember, seed uint64) []groupMember {
	out := append([]groupMember(nil), ms...)
	rng := seed
	total := 0
	for _, m := range out {
		total += m.weight
	}
	if total == 0 {
		return out
	}
	for k := 0; k < len(out)-1; k++ {
		rest := 0
		for _, m := range out[k:] {
			rest += m.weight
		}
		if rest == 0 {
			break
		}
		r := int(splitmix64(&rng) % uint64(rest)) //nolint:gosec // weights are bounded by the schema
		pick := k
		for acc := out[k].weight; acc <= r; acc += out[pick].weight {
			pick++
		}
		out[k], out[pick] = out[pick], out[k]
	}
	return out
}

// splitmix64 is the mixing function the routing engine's weighted order uses.
func splitmix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// ringGroup dials a group number: the strategy orders the members, DND
// members are skipped unless the group ignores it, and the failure
// destination applies after the last member or the ring timeout. Hunt
// groups ring one member at a time; ring groups fork (staggered by delay).
func (c *call) ringGroup(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, g snapshot.RingGroup, members []snapshot.RingGroupMember) {
	s := c.s
	var ms []groupMember
	for _, m := range members {
		e, _ := snap.Extension(m.Extension)
		ms = append(ms, groupMember{ext: m.Extension, weight: m.Weight, delay: m.Delay, dnd: e.DND, lastEnd: s.lastEndOf(m.Extension)})
	}
	ordered := orderGroup(g.Strategy, ms, g.IgnoreDND, s.rotation(g.ID), groupSeed(g.Name, c.id))
	c.addTrace(fmt.Sprintf("Group %s (%s): members %v", g.Name, g.Strategy, extsOf(ordered)))
	if len(ordered) == 0 {
		s.m.Forwarded.WithLabelValues("group_dnd").Inc()
		if !c.begin(req, tx) {
			return
		}
		c.groupFail(snap, g)
		return
	}
	c.mu.Lock()
	c.groupName, c.groupStrategy = g.Name, g.Strategy
	c.groupFn = func() { c.groupFail(snap, g) }
	c.mu.Unlock()
	// Bindings per member, fetched here on the request goroutine (bounded).
	var bindings []livestate.Binding
	var order []groupMember
	for _, m := range ordered {
		bs := c.bindingsForDevices(tx, req, snap, m.ext)
		if len(bs) == 0 {
			continue
		}
		bindings = append(bindings, bs[0]) // one binding per member: a hunt never rings two at once
		order = append(order, m)
	}
	if len(bindings) == 0 {
		if !c.begin(req, tx) {
			return
		}
		c.groupFail(snap, g)
		return
	}
	if !c.begin(req, tx) {
		return
	}
	if g.Hunt {
		c.setupHunt(bindings, order, g)
		return
	}
	// Fork with staggered starts: sequential uses the group's member delay
	// as the spacing, the other strategies each member's own delay.
	c.mu.Lock()
	stage := c.stageSeq
	n := 0
	for i, m := range order {
		delay := time.Duration(m.delay) * time.Second
		if g.Strategy == "sequential" {
			delay = time.Duration(i*g.MemberDelay) * time.Second
		}
		if delay == 0 {
			c.addLeg(&leg{binding: bindings[i], stage: stage})
			n++
			continue
		}
		idx, stg := i, stage
		time.AfterFunc(delay, func() {
			c.mu.Lock()
			closed := c.setupClosed || c.winner != nil
			var l *leg
			if !closed {
				l = c.addLeg(&leg{binding: bindings[idx], stage: stg})
			}
			c.mu.Unlock()
			if !closed {
				c.startLeg(l)
			}
		})
	}
	legs := c.legs
	c.mu.Unlock()
	for _, l := range legs {
		if l.stage == stage {
			go l.run()
		}
	}
	c.setup(n)
}

// startLeg launches one fork's INVITE.
func (c *call) startLeg(l *leg) { go l.run() }

// setupHunt rings one member at a time: each gets its delay (or the group's
// member delay), and a no-answer or busy moves to the next. The group's
// ring timeout bounds the whole hunt; then the failure destination applies.
func (c *call) setupHunt(bindings []livestate.Binding, order []groupMember, g snapshot.RingGroup) {
	defer close(c.setupDone)
	deadline := time.NewTimer(time.Duration(g.RingTimeout) * time.Second)
	defer deadline.Stop()
	for i, m := range order {
		c.mu.Lock()
		stage := c.stageSeq
		l := c.addLeg(&leg{binding: bindings[i], stage: stage})
		c.mu.Unlock()
		go l.run()
		per := time.Duration(m.delay) * time.Second
		if per == 0 {
			per = time.Duration(g.MemberDelay) * time.Second
		}
		if per <= 0 {
			per = time.Duration(g.RingTimeout) * time.Second
		}
		member := time.NewTimer(per)
	wait:
		for {
			select {
			case ev := <-c.events:
				if ev.leg != l && ev.kind != evAnswered {
					continue // a member already passed, still winding down
				}
				switch ev.kind {
				case evAnswered:
					member.Stop()
					c.answer(ev.leg)
					return
				case evFailed:
					break wait // busy or no answer: the next member
				}
			case <-member.C:
				break wait // this member had its turn
			case <-deadline.C:
				c.mu.Lock()
				c.cancelForks(nil)
				c.mu.Unlock()
				c.groupFail(c.snapOrFail(), g)
				return
			}
		}
		member.Stop()
		c.abandon(l) // it may still answer late; its answer is hung up
	}
	c.groupFail(c.snapOrFail(), g)
}

// groupFail applies the failure destination after the hunt or ring gave up:
// the named extension's voicemail box, an external number, or a hangup.
func (c *call) groupFail(snap *snapshot.Snapshot, g snapshot.RingGroup) {
	switch g.FailureKind {
	case "voicemail":
		e, ok := snap.Extension(g.FailureTarget)
		if ok && c.voicemailConfigured(e) {
			c.addTrace(fmt.Sprintf("Group %s: to voicemail of %s", g.Name, g.FailureTarget))
			c.startVoicemail(vmLeave, "group-failure")
			return
		}
		c.hangupGroup(sip.StatusTemporarilyUnavailable, "failure destination unavailable")
	case "external":
		if req := c.inv; req != nil {
			c.forward(req, nil, snap, g.FailureTarget, nil)
			return
		}
		c.hangupGroup(sip.StatusTemporarilyUnavailable, "failure destination unreachable")
	default:
		c.hangupGroup(sip.StatusRequestTimeout, "group timeout")
	}
}

// hangupGroup answers the caller with the final failure and records it.
func (c *call) hangupGroup(code int, why string) {
	c.respondA(code, statusText(code))
	c.end(code, cdr.SideSystem, why, ResultNoAnswer)
}

// rotation is the per-group round-robin counter, in this node's memory: a
// restart restarts the rotation, which only spreads the first ring.
func (s *Server) rotation(groupID int64) *atomic.Uint64 {
	s.rrMu.Lock()
	defer s.rrMu.Unlock()
	if r, ok := s.rrPos[groupID]; ok {
		return r
	}
	r := new(atomic.Uint64)
	s.rrPos[groupID] = r
	return r
}

// lastEndOf is the extension's last call end this node saw, for
// longest-idle. It is node-local: after a failover the idle order restarts.
func (s *Server) lastEndOf(ext string) time.Time {
	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	return s.lastEnd[ext]
}

// noteCallEnd remembers the call's participants' last call end.
func (s *Server) noteCallEnd(caller, callee string) {
	s.lastMu.Lock()
	defer s.lastMu.Unlock()
	now := time.Now()
	s.lastEnd[caller] = now
	if callee != "" {
		s.lastEnd[callee] = now
	}
}

// groupSeed makes a weighted group's order deterministic per call.
func groupSeed(group, callID string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(group))
	_, _ = h.Write([]byte(callID))
	return h.Sum64()
}

func extsOf(ms []groupMember) []string {
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, m.ext)
	}
	return out
}

// snapOrFail is the current snapshot during a group call's teardown.
func (c *call) snapOrFail() *snapshot.Snapshot {
	return c.s.deps.Snapshots.Current()
}
