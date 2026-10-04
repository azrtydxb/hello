// Call flow (S-4, S-5, S-6, S-7): forwarding and DND evaluated from the
// snapshot before forking, ring/hunt group strategies, and the exhaustion
// hand-offs (busy forward, no-answer forward, voicemail) that move a call to
// its next stage. Everything here reads the snapshot only — the database is
// never on this path.
package sip

import (
	"fmt"
	"slices"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// How an exhausted ring stage resolves.
type exhaustion int

const (
	exhNone  exhaustion = iota // no forwarding or voicemail: the caller gets the failure
	exhRetry                   // forwarding moved the call to new legs
	exhDone                    // this function answered or ended the call
)

// ringTarget is dispatch's entry for a dialled extension: a ring group
// number resolves as a group, anything else goes through the extension's
// features (forwarding, DND, voicemail) and then forks to its devices.
func (c *call) ringTarget(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, ext string) {
	if g, members, ok := snap.RingGroup(ext); ok {
		c.ringGroup(req, tx, snap, g, members)
		return
	}
	c.ringExtension(req, tx, snap, ext, nil)
}

// forwardLoop rejects a forwarding chain that returns to a visited
// extension with 408 "forward loop" (spec S-4).
func (c *call) forwardLoop(tx sip.ServerTransaction, chain []string) {
	if tx == nil {
		// A stage advance has no INVITE transaction left: answer the dialog.
		c.respondA(sip.StatusRequestTimeout, "Forward Loop")
	} else {
		c.s.respond(tx, c.inv, sip.StatusRequestTimeout, "Forward Loop")
	}
	c.addTrace("Forward loop detected: " + fmt.Sprint(chain))
	c.s.m.Forwarded.WithLabelValues(ForwardLoop).Inc()
	c.record(sip.StatusRequestTimeout, cdr.SideSystem, "forward loop", ResultFailed)
}

// voicemailConfigured reports whether a voicemail hand-off is possible for
// e: the snapshot says the extension has a box, and this node can anchor
// media and reach the store.
func (c *call) voicemailConfigured(e snapshot.Extension) bool {
	s := c.s
	return e.VoicemailEnabled && e.VoicemailBoxID > 0 && s.deps.Voicemails != nil && s.deps.Objects != nil
}

// ringExtension evaluates an extension's features and forks to its
// registered devices. visited carries the extensions already rung, for the
// forwarding-loop check.
func (c *call) ringExtension(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, ext string, visited []string) {
	s := c.s
	if slices.Contains(visited, ext) {
		c.forwardLoop(tx, append(append([]string{}, visited...), ext))
		return
	}
	e, ok := snap.Extension(ext)
	if !ok {
		e = snapshot.Extension{Number: ext} // an extension without a snapshot row has no features
	}
	c.mu.Lock()
	c.stages = &callStages{visited: append(append([]string{}, visited...), ext), ext: ext, feats: e}
	c.mu.Unlock()
	if e.ForwardAlways != "" {
		c.addTrace(fmt.Sprintf("Extension %s forwards always to %s", ext, e.ForwardAlways))
		s.m.Forwarded.WithLabelValues(ForwardAlways).Inc()
		c.forward(req, tx, snap, e.ForwardAlways, c.stages.visited)
		return
	}
	if e.DND {
		if c.voicemailConfigured(e) {
			c.addTrace(fmt.Sprintf("Extension %s is DND: to voicemail", ext))
			s.m.Forwarded.WithLabelValues(ForwardDND).Inc()
			if !c.begin(req, tx) {
				return
			}
			c.startVoicemail(vmLeave, ForwardDND)
			return
		}
		c.addTrace(fmt.Sprintf("Extension %s is DND: declined", ext))
		s.respond(tx, req, sip.StatusGlobalDecline, statusText(sip.StatusGlobalDecline))
		c.record(sip.StatusGlobalDecline, cdr.SideCallee, "DND", ResultBusy)
		return
	}
	targets := c.bindingsForDevices(tx, req, snap, ext)
	if len(targets) == 0 {
		// Nothing registered: the no-answer target applies at once, else
		// voicemail with the unreachable greeting, else 480.
		if e.ForwardNoAnswer != "" {
			c.addTrace(fmt.Sprintf("Extension %s is unreachable: forward to %s", ext, e.ForwardNoAnswer))
			s.m.Forwarded.WithLabelValues(ForwardUnreach).Inc()
			c.forward(req, tx, snap, e.ForwardNoAnswer, c.stages.visited)
			return
		}
		if c.voicemailConfigured(e) {
			c.addTrace(fmt.Sprintf("Extension %s is unreachable: to voicemail", ext))
			if !c.begin(req, tx) {
				return
			}
			c.startVoicemail(vmLeave, "unreachable")
			return
		}
		s.respond(tx, req, sip.StatusTemporarilyUnavailable, statusText(sip.StatusTemporarilyUnavailable))
		c.record(sip.StatusTemporarilyUnavailable, cdr.SideSystem, "no registered device", ResultUnavailable)
		return
	}
	if !c.begin(req, tx) {
		return
	}
	c.forkBindings(targets)
	c.setup(len(targets))
}

// bindingsForDevices collects the live bindings of an extension's devices,
// except the calling device (Phase 1 ring-all rule). A live-state failure
// answers 503 and records; it returns nil then.
func (c *call) bindingsForDevices(tx sip.ServerTransaction, req *sip.Request, snap *snapshot.Snapshot, ext string) []livestate.Binding {
	s := c.s
	var targets []livestate.Binding
	for _, d := range snap.DevicesForExtension(ext) {
		if d.Username == c.callerDevice {
			continue
		}
		ctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
		cancel()
		if err != nil {
			if tx != nil {
				s.stateDown(tx, req, "bindings", err)
			}
			c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "live state unavailable", ResultFailed)
			return nil
		}
		targets = append(targets, bs...)
	}
	return targets
}

// forkBindings adds a leg per binding and starts it; c.begin must have run.
func (c *call) forkBindings(targets []livestate.Binding) {
	c.mu.Lock()
	stage := c.stageSeq
	for _, b := range targets {
		c.addLeg(&leg{binding: b, stage: stage})
	}
	legs := c.legs
	c.mu.Unlock()
	for _, l := range legs {
		if l.stage == stage {
			go l.run()
		}
	}
}

// forward follows a forwarding target that is not the extension itself: a
// group, another extension, or an external number through outbound routing.
// It runs before the caller has been answered (forward-always, unreachable),
// so it can still answer the INVITE transaction in any of the ways dispatch
// does.
func (c *call) forward(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, target string, visited []string) {
	if _, _, ok := snap.RingGroup(target); ok {
		if g, members, _ := snap.RingGroup(target); ok {
			c.ringGroup(req, tx, snap, g, members)
			return
		}
	}
	if snap.HasExtension(target) {
		c.ringExtension(req, tx, snap, target, visited)
		return
	}
	// External: routed as the forwarding extension's own outbound call.
	rs := snap.Routing()
	fromExt := target
	if len(visited) > 0 {
		fromExt = visited[len(visited)-1]
	}
	dec := c.s.decide(rs, routing.Call{
		FromExtension: fromExt, Number: target, CallerID: c.callerNum,
		SIPDomain: req.Recipient.Host, Header: headerOf(req), At: time.Now(),
	})
	c.trace = append(c.trace, dec.Trace...)
	c.route, c.rewritten = dec.Route, dec.Number
	switch {
	case dec.Kind == routing.KindOutbound:
		c.direction = cdr.DirectionOutbound
		if c.begin(req, tx) {
			c.setupOutbound(dec)
		}
	case dec.Kind == routing.KindInternal && dec.Extension != "":
		c.ringExtension(req, tx, snap, dec.Extension, visited)
	case dec.Kind == routing.KindInbound && dec.SIPURI != "":
		c.ringURI(req, tx, dec.SIPURI)
	default:
		code, reason := dec.RejectCode, dec.Reason
		if code < 300 {
			code = sip.StatusNotFound
		}
		if reason == "" {
			reason = "forward target has no route"
		}
		c.s.respond(tx, req, code, statusText(code))
		c.record(code, cdr.SideSystem, reason, ResultNotFound)
	}
}

// exhausted is the setup loop's hand-off when every fork failed (busyAll)
// or the ring timer fired. It moves the call to its next stage — forward on
// busy or no answer, else voicemail — or leaves the original failure in
// place.
func (c *call) exhausted(busyAll bool) exhaustion {
	s := c.s
	c.mu.Lock()
	st, groupFn := c.stages, c.groupFn
	c.mu.Unlock()
	if st == nil {
		// A group call has no forwarding stages: its failure destination
		// applies when the ring timeout or the last member gives up.
		if groupFn != nil {
			if !c.closeSetup() {
				return exhDone
			}
			c.cancelForks(nil)
			groupFn()
			return exhDone
		}
		return exhNone
	}
	e := st.feats
	kind, target := "", ""
	switch {
	case busyAll && e.ForwardBusy != "":
		kind, target = ForwardBusy, e.ForwardBusy
	case !busyAll && e.ForwardNoAnswer != "":
		kind, target = ForwardNoAnswer, e.ForwardNoAnswer
	}
	if target != "" {
		return c.advanceStage(kind, target)
	}
	if c.voicemailConfigured(e) {
		if !c.closeSetup() {
			return exhDone // a fork won just now; answer() sees it
		}
		c.cancelForks(nil)
		reason := "no-answer"
		if busyAll {
			reason = "busy"
		}
		c.addTrace(fmt.Sprintf("Extension %s: to voicemail after %s", st.ext, reason))
		s.m.Forwarded.WithLabelValues(reason).Inc()
		c.startVoicemail(vmLeave, reason)
		return exhDone
	}
	c.mu.Lock()
	fn := c.groupFn
	c.mu.Unlock()
	if fn != nil {
		if !c.closeSetup() {
			return exhDone
		}
		c.cancelForks(nil)
		fn()
		return exhDone
	}
	return exhNone
}

// advanceStage closes the current ring, resolves the forwarding target and
// opens a new ring stage with fresh legs. A target already in the visited
// chain is a loop: 408 to the caller (spec S-4).
func (c *call) advanceStage(kind, target string) exhaustion {
	s := c.s
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return exhNone
	}
	c.mu.Lock()
	st := c.stages
	c.mu.Unlock()
	if st == nil {
		return exhNone
	}
	if snap.HasExtension(target) && slices.Contains(st.visited, target) {
		if !c.closeSetup() {
			return exhDone
		}
		c.cancelForks(nil)
		c.forwardLoop(nil, st.visited)
		return exhDone
	}
	// Close setup, stop the old stage's legs, then reopen for the new ones.
	c.mu.Lock()
	c.setupClosed = true
	c.stageSeq++
	stage := c.stageSeq
	c.mu.Unlock()
	c.cancelForks(nil)
	c.addTrace(fmt.Sprintf("Forwarding on %s to %s", kind, target))
	s.m.Forwarded.WithLabelValues(kind).Inc()
	var targets []livestate.Binding
	if snap.HasExtension(target) {
		e, _ := snap.Extension(target)
		st.visited = append(st.visited, target)
		st.ext = target
		st.feats = e
		st.source = kind
		targets = c.bindingsForDevices(nil, nil, snap, target)
		if targets == nil {
			// Live state down or nothing registered: end the call here.
			c.respondA(sip.StatusServiceUnavailable, "Service Unavailable")
			c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "forward target unreachable", ResultUnavailable)
			return exhDone
		}
	} else {
		// External target: fork trunk legs for the first usable candidate.
		rs := snap.Routing()
		dec := s.decide(rs, routing.Call{FromExtension: st.ext, Number: target, CallerID: c.callerNum, At: time.Now()})
		c.trace = append(c.trace, dec.Trace...)
		if dec.Kind != routing.KindOutbound || len(dec.Candidates) == 0 {
			c.respondA(sip.StatusNotFound, "Not Found")
			c.end(sip.StatusNotFound, cdr.SideSystem, "forward target has no route", ResultNotFound)
			return exhDone
		}
		c.route, c.rewritten, c.direction = dec.Route, dec.Number, cdr.DirectionOutbound
		for _, cand := range dec.Candidates {
			t := cand.Trunk
			if t == nil {
				continue
			}
			if ok, _ := c.acquireSlot(t, dec.Emergency, c.id); !ok {
				continue
			}
			for _, d := range cand.Destinations {
				c.mu.Lock()
				c.addLeg(&leg{trunk: t, dest: d, addr: s.trunks.destAddr(d), number: dec.Number, callerID: dec.CallerID, stage: stage})
				c.mu.Unlock()
			}
			break // the first usable candidate carries the forwarded call
		}
	}
	c.mu.Lock()
	if c.winner != nil { // a racing answer from the old stage wins after all
		c.mu.Unlock()
		return exhDone
	}
	// The new stage's legs: extension bindings, or the trunk legs already
	// added above.
	for _, b := range targets {
		c.addLeg(&leg{binding: b, stage: stage})
	}
	n := 0
	for _, l := range c.legs {
		if l.stage == stage {
			n++
			go l.run()
		}
	}
	c.setupClosed = false
	c.mu.Unlock()
	if n == 0 {
		if c.closeSetup() {
			c.respondA(sip.StatusServiceUnavailable, "Service Unavailable")
			c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "forward target had no usable trunk", ResultUnavailable)
		}
		return exhDone
	}
	return exhRetry
}

// curStage is the forwarding stage the setup loop is waiting on.
func (c *call) curStage() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stageSeq
}

// pendingLegs is the number of legs of the current stage that may still
// answer (the setup loop's new pending count after a stage advance).
func (c *call) pendingLegs() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, l := range c.legs {
		if l.stage == c.stageSeq && !l.abandoned {
			n++
		}
	}
	return n
}
