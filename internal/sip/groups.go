// Ring and hunt groups (S-6, S-7). The strategy orderings are pure
// functions over plain members, so they unit-test as tables; the fork
// integration (ringGroup) applies them to live bindings and carries the
// failure destination after the last member.
package sip

import (
	"fmt"
	"hash/fnv"
	"slices"
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
	// A nil tx means the call has begun already (a failover from another
	// group opens a new stage on it), as in the IVR, queue and time
	// condition entries.
	begin := func() bool { return tx == nil || c.begin(req, tx) }
	c.mu.Lock()
	c.gfChain = append(c.gfChain, g.Name)
	c.mu.Unlock()
	var ms []groupMember
	var busy map[string]bool // fetched once, and only when a member has call waiting off
	for _, m := range members {
		e, _ := snap.Extension(m.Extension)
		if e.NoCallWaiting {
			if busy == nil {
				busy = s.busyExtensions()
			}
			if busy[m.Extension] {
				// A busy member with call waiting off is unavailable (S-18).
				c.addTrace(fmt.Sprintf("Group %s: member %s is busy with call waiting off: skipped", g.Name, m.Extension))
				continue
			}
		}
		ms = append(ms, groupMember{ext: m.Extension, weight: m.Weight, delay: m.Delay, dnd: e.DND, lastEnd: s.lastEndOf(m.Extension)})
	}
	ordered := orderGroup(g.Strategy, ms, g.IgnoreDND, s.rotation(g.ID), groupSeed(g.Name, c.id))
	c.addTrace(fmt.Sprintf("Group %s (%s): members %v", g.Name, g.Strategy, extsOf(ordered)))
	if len(ordered) == 0 {
		s.m.Forwarded.WithLabelValues("group_dnd").Inc()
		if !begin() {
			return
		}
		c.groupFail(snap, g)
		return
	}
	c.mu.Lock()
	c.groupName, c.groupStrategy = g.Name, g.Strategy
	c.groupFn = func() { c.groupFail(snap, g) }
	c.mu.Unlock()
	c.noteMOHGroup(g)
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
		if !begin() {
			return
		}
		c.groupFail(snap, g)
		return
	}
	// The anchoring decision (contract 2) from the first member binding.
	c.considerAnchor(snap, bindingEndpoint(bindings[0], ordered[0].ext))
	if !begin() {
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
			// The message goes to the failure target's box, not to the box
			// named after the dialled group number.
			c.mu.Lock()
			c.vmExt = g.FailureTarget
			c.mu.Unlock()
			c.startVoicemail(vmLeave, "group-failure")
			return
		}
		c.hangupGroup(sip.StatusTemporarilyUnavailable, "failure destination unavailable")
	case "announcement":
		// The announcement destination answers, plays, then hangs the
		// caller up after the prompt (spec S-5). A missing or broken WAV
		// still answers and ends the call; the step is traced either way
		// (spec failure mode).
		c.addTrace(fmt.Sprintf("Group %s: announcement %q", g.Name, g.FailureTarget))
		c.announcementDestination(g.FailureTarget)
	case "ivr":
		if m, ok := snap.IVRMenu(g.FailureTarget); ok {
			c.addTrace(fmt.Sprintf("Group %s: IVR menu %q", g.Name, g.FailureTarget))
			c.startIVR(nil, snap, m)
			return
		}
		c.hangupGroup(sip.StatusTemporarilyUnavailable, "failure IVR menu unavailable")
	case "queue":
		if q, ok := snap.Queue(g.FailureTarget); ok {
			c.addTrace(fmt.Sprintf("Group %s: queue %q", g.Name, g.FailureTarget))
			c.startQueue(nil, snap, q)
			return
		}
		c.hangupGroup(sip.StatusTemporarilyUnavailable, "failure queue unavailable")
	case "time_condition":
		c.addTrace(fmt.Sprintf("Group %s: time condition %q", g.Name, g.FailureTarget))
		c.routeTimeCondition(nil, snap, g.FailureTarget)
	case "extension", "ring_group":
		c.groupFailover(snap, g)
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

// groupFailMaxDepth bounds a chain of ring group failovers at run time, like
// the queue failover chain; the visited names catch a loop sooner.
const groupFailMaxDepth = queueMaxDepth

// groupFailover is the failure destination "extension" or "ring_group": the
// group's ring stage has ended, so the call gets a new one (stage) that rings
// the extension, or the other group with its own strategy and failure
// destination. It returns once the new stage's setup has finished. A chain
// that comes back to a group or extension it already rang, or that is longer
// than groupFailMaxDepth, is cut with 408 "forward loop" (spec S-4).
func (c *call) groupFailover(snap *snapshot.Snapshot, g snapshot.RingGroup) {
	kind, target := g.FailureKind, g.FailureTarget
	c.mu.Lock()
	c.gfDepth++
	depth := c.gfDepth
	chain := append([]string(nil), c.gfChain...)
	var visited []string
	if c.stages != nil {
		visited = append(visited, c.stages.visited...)
	}
	c.mu.Unlock()
	loop := depth > groupFailMaxDepth
	switch kind {
	case "ring_group":
		loop = loop || slices.Contains(chain, target)
	case "extension":
		loop = loop || slices.Contains(visited, target)
	}
	if loop {
		c.groupLoop(g, append(chain, target))
		return
	}
	switch kind {
	case "ring_group":
		g2, members, ok := snap.RingGroup(target)
		if !ok {
			c.hangupGroup(sip.StatusTemporarilyUnavailable, "failure ring group unavailable")
			return
		}
		c.addTrace(fmt.Sprintf("Group %s: fails over to group %s", g.Name, target))
		c.s.m.Forwarded.WithLabelValues(ForwardGroupFailover).Inc()
		if !c.openFailoverStage(nil) {
			return
		}
		c.ringGroup(c.inv, nil, snap, g2, members)
	default:
		if !snap.HasExtension(target) {
			c.hangupGroup(sip.StatusTemporarilyUnavailable, "failure extension unavailable")
			return
		}
		e, ok := snap.Extension(target)
		if !ok {
			e = snapshot.Extension{Number: target} // no row: no features
		}
		c.failoverExtension(snap, g, e, visited)
	}
}

// failoverExtension rings the extension a group failed over to. Its own
// features apply from there: do not disturb and no registered device send
// the call to its voicemail box when it has one, and a ring that is not
// answered follows its no-answer forward or voicemail like a forward does.
func (c *call) failoverExtension(snap *snapshot.Snapshot, g snapshot.RingGroup, e snapshot.Extension, visited []string) {
	s := c.s
	ext := e.Number
	c.addTrace(fmt.Sprintf("Group %s: fails over to extension %s", g.Name, ext))
	s.m.Forwarded.WithLabelValues(ForwardGroupFailover).Inc()
	toVoicemail := func(reason string) bool {
		if !c.voicemailConfigured(e) {
			return false
		}
		c.mu.Lock()
		c.vmExt = ext
		c.mu.Unlock()
		c.addTrace(fmt.Sprintf("Extension %s: to voicemail (%s)", ext, reason))
		c.startVoicemail(vmLeave, reason)
		return true
	}
	if e.DND {
		if !toVoicemail(ForwardDND) {
			c.hangupGroup(sip.StatusGlobalDecline, "failure extension is do not disturb")
		}
		return
	}
	targets, err := c.bindingsFor(snap, ext)
	if err != nil {
		c.respondA(sip.StatusServiceUnavailable, "Service Unavailable")
		c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "live state unavailable", ResultFailed)
		return
	}
	if len(targets) == 0 {
		if !toVoicemail("unreachable") {
			c.hangupGroup(sip.StatusTemporarilyUnavailable, "failure extension has no registered device")
		}
		return
	}
	if !c.openFailoverStage(&callStages{
		visited: append(visited, ext), ext: ext, feats: e, source: ForwardGroupFailover,
	}) {
		return
	}
	c.considerAnchor(snap, bindingEndpoint(targets[0], ext))
	c.forkBindings(targets)
	c.setup(len(targets))
}

// openFailoverStage ends the ring stage that gave up and opens the next:
// its legs are abandoned and cancelled, late answers from them are refused,
// and the call's setup gets a fresh event channel (as an IVR menu does).
// stages is the new stage's extension state, nil for a group. It reports
// false when a leg answered in the meantime or the caller is gone.
func (c *call) openFailoverStage(stages *callStages) bool {
	c.mu.Lock()
	if c.winner != nil || c.ended {
		c.mu.Unlock()
		return false
	}
	if c.isCancelled {
		// The setup loop that would have ended the call is the one that
		// handed over here, so the call ends here (the transaction layer
		// has answered 487).
		c.mu.Unlock()
		c.cancelForks(nil)
		c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
		return false
	}
	for _, l := range c.legs {
		l.abandoned = true
	}
	c.stageSeq++
	c.stages = stages
	c.groupFn = nil // the new stage sets its own
	c.setupClosed = false
	c.setupDone = make(chan struct{})
	c.mu.Unlock()
	c.cancelForks(nil)
	return true
}

// groupLoop cuts a failover chain that came back on itself.
func (c *call) groupLoop(g snapshot.RingGroup, chain []string) {
	c.respondA(sip.StatusRequestTimeout, "Forward Loop")
	c.addTrace(fmt.Sprintf("Group %s: failover loop detected: %v", g.Name, chain))
	c.s.m.Forwarded.WithLabelValues(ForwardLoop).Inc()
	c.end(sip.StatusRequestTimeout, cdr.SideSystem, "forward loop", ResultFailed)
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
