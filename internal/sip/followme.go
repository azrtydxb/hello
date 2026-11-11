// Follow-me (pbx-gaps S-5 and S-6, migration 00074). When a call reaches an
// extension's ring step, after forward-always and before the no-answer
// forward, the extension's profile adds up to five targets to its own
// devices: an external number (through outbound routing), another extension
// or a ring group. The profile is read from the snapshot only.
//
// The ring reuses what the other stages use: the legs, the setup events, the
// trunk slots and outbound decision of a forwarded external number
// (advanceStage), the members of a ring group, and the exhaustion hand-off
// (exhausted) that moves an unanswered ring to the no-answer forward or the
// voicemail box. An external target can be screened: it plays "press 1 to
// accept" and is dropped without the digit, before the caller is bridged, so
// a carrier's voicemail cannot take the call.
package sip

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
	"github.com/emiago/sipgo/sip"
)

// fmAcceptWindow is how long an external target has, after the prompt, to
// press 1 before it is dropped (S-6).
var fmAcceptWindow = 10 * time.Second

// Follow-me leg results (the CDR's and the metric's).
const (
	fmAnswered  = "answered"
	fmDeclined  = "declined"
	fmBusy      = "busy"
	fmNoAnswer  = "no_answer"
	fmFailed    = "failed"
	fmSkipped   = "skipped"
	fmCancelled = "cancelled"
)

// The kinds of a follow-me leg: the extension's own devices, or the profile
// target's kind.
const fmOwn = "own"

// fmStep is one place the follow-me rings: the extension's own devices or one
// target of the profile.
type fmStep struct {
	kind     string
	label    string
	delay    time.Duration // sequential: how long it rings before the next starts
	bindings []livestate.Binding
	dec      routing.Decision // an external target's outbound decision
}

// fmPlan is what a follow-me rings for one call.
type fmPlan struct {
	own        *fmStep
	targets    []fmStep
	sequential bool
	timeout    time.Duration
	confirm    bool
}

// fmRec is the CDR record and metric subject of one follow-me leg. Guarded by
// call.mu.
type fmRec struct {
	kind, ext, trunk, route, number string
	result                          string
}

// fmScreening is the "press 1" check running on an answered external leg.
type fmScreening struct {
	leg    *leg
	digits chan byte
}

// fmLegResult maps a final response of a follow-me leg to its result.
func fmLegResult(code int, reason string) string {
	switch {
	case reason == fmDeclined:
		return fmDeclined
	case isBusy(code):
		return fmBusy
	case code == sip.StatusRequestTimeout, code == sip.StatusTemporarilyUnavailable, code == sip.StatusRequestTerminated:
		return fmNoAnswer
	}
	return fmFailed
}

// fmEnd settles a leg's result once: the CDR row, the metric and a trace
// line. A result already set stands.
func (c *call) fmEnd(rec *fmRec, result string) {
	if rec == nil {
		return
	}
	c.mu.Lock()
	if rec.result != "" {
		c.mu.Unlock()
		return
	}
	rec.result = result
	kind, who := rec.kind, rec.ext
	if rec.trunk != "" {
		who = rec.trunk
	}
	c.mu.Unlock()
	c.s.m.FollowMeLegs.WithLabelValues(kind, result).Inc()
	c.addTrace(fmt.Sprintf("Follow-me: %s %s -> %s", kind, who, result))
}

// fmCDRLegs is the call's follow-me legs for the CDR, in the order they rang.
func (c *call) fmCDRLegs() []cdr.FollowMeLeg {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]cdr.FollowMeLeg, 0, len(c.fmRecs))
	for _, r := range c.fmRecs {
		out = append(out, cdr.FollowMeLeg{Kind: r.kind, Extension: r.ext, Trunk: r.trunk, Result: r.result})
	}
	return out
}

// fmSettleAll gives every leg without a result its end: cancelled when the
// call was answered (by another leg), else not answered.
func (c *call) fmSettleAll(answered bool) {
	c.mu.Lock()
	recs := slices.Clone(c.fmRecs)
	c.mu.Unlock()
	res := fmNoAnswer
	if answered {
		res = fmCancelled
	}
	for _, r := range recs {
		c.fmEnd(r, res)
	}
}

// fmApplies reports whether the follow-me profile of ext rings for this call,
// with the reason it does not when it is configured but skipped.
func (c *call) fmApplies(e snapshot.Extension, fm snapshot.FollowMe) (bool, string) {
	switch {
	case !fm.Active():
		return false, ""
	case c.secure:
		return false, "sips: follow-me has no TLS path"
	case e.DND && fm.DNDBlocks:
		return false, "do not disturb stops follow-me"
	}
	return true, ""
}

// fmBuildPlan resolves the profile into what rings. It reads the snapshot and
// the live bindings; an error is the live-state failure.
func (c *call) fmBuildPlan(snap *snapshot.Snapshot, domain string, hdr func(string) string, ext string, e snapshot.Extension, fm snapshot.FollowMe, visited []string) (fmPlan, error) {
	s := c.s
	plan := fmPlan{
		sequential: fm.Strategy == snapshot.FollowMeSequential,
		timeout:    time.Duration(fm.TimeoutSeconds) * time.Second,
		confirm:    fm.ConfirmExternal,
	}
	seen := map[string]bool{}
	fresh := func(bs []livestate.Binding) []livestate.Binding {
		var out []livestate.Binding
		for _, b := range bs {
			k := b.AOR + "|" + b.ContactURI
			if !seen[k] {
				seen[k] = true
				out = append(out, b)
			}
		}
		return out
	}
	skip := func(kind, why string) {
		c.addTrace("Follow-me: " + why)
		s.m.FollowMeLegs.WithLabelValues(kind, fmSkipped).Inc()
	}
	if fm.RingOwnDevices {
		switch {
		case e.DND:
			c.addTrace(fmt.Sprintf("Follow-me: extension %s is DND: its own devices do not ring", ext))
		case e.NoCallWaiting && s.busyExtensions()[ext]:
			c.addTrace(fmt.Sprintf("Follow-me: extension %s is busy with call waiting off: its own devices do not ring", ext))
		default:
			bs, err := c.bindingsFor(snap, ext)
			if err != nil {
				return plan, err
			}
			if bs = fresh(bs); len(bs) > 0 {
				plan.own = &fmStep{kind: fmOwn, label: ext, bindings: bs}
			} else {
				c.addTrace(fmt.Sprintf("Follow-me: no device of extension %s is registered: the targets ring alone", ext))
			}
		}
	}
	extBindings := func(target string, te snapshot.Extension) ([]livestate.Binding, string, error) {
		switch {
		case target == ext || slices.Contains(visited, target):
			return nil, "loop back to extension " + target, nil
		case te.DND:
			return nil, "extension " + target + " is DND", nil
		}
		bs, err := c.bindingsFor(snap, target)
		if err != nil {
			return nil, "", err
		}
		if bs = fresh(bs); len(bs) == 0 {
			return nil, "extension " + target + " has no registered device", nil
		}
		return bs, "", nil
	}
	for _, t := range fm.Targets {
		step := fmStep{kind: t.Kind, label: t.Target, delay: time.Duration(t.Delay) * time.Second}
		switch t.Kind {
		case snapshot.FollowMeExtension:
			te, _ := snap.Extension(t.Target)
			bs, why, err := extBindings(t.Target, te)
			if err != nil {
				return plan, err
			}
			if why != "" {
				skip(t.Kind, why)
				continue
			}
			step.bindings = bs
		case snapshot.FollowMeRingGroup:
			g, members, ok := snap.RingGroup(t.Target)
			if !ok {
				skip(t.Kind, "ring group "+t.Target+" does not exist")
				continue
			}
			for _, m := range members {
				me, _ := snap.Extension(m.Extension)
				if me.DND && !g.IgnoreDND {
					continue
				}
				bs, _, err := extBindings(m.Extension, snapshot.Extension{})
				if err != nil {
					return plan, err
				}
				step.bindings = append(step.bindings, bs...)
			}
			if len(step.bindings) == 0 {
				skip(t.Kind, "ring group "+t.Target+" has no member to ring")
				continue
			}
		case snapshot.FollowMeExternal:
			rs := snap.Routing()
			if rs == nil || rs.Router == nil {
				skip(t.Kind, "no outbound routing for "+t.Target)
				continue
			}
			dec := s.decide(rs, routing.Call{
				FromExtension: ext, Number: t.Target, CallerID: c.callerNum,
				SIPDomain: domain, Header: hdr, At: time.Now(),
			})
			c.trace = append(c.trace, dec.Trace...)
			switch {
			case dec.Kind == routing.KindInternal && dec.Extension != "":
				// The number is one of ours: ring it as an extension.
				te, _ := snap.Extension(dec.Extension)
				bs, why, err := extBindings(dec.Extension, te)
				if err != nil {
					return plan, err
				}
				if why != "" {
					skip(t.Kind, why)
					continue
				}
				step.kind, step.label, step.bindings = snapshot.FollowMeExtension, dec.Extension, bs
			case dec.Kind == routing.KindOutbound && len(dec.Candidates) > 0:
				step.dec = dec
			default:
				skip(t.Kind, "no route for "+t.Target)
				continue
			}
		}
		plan.targets = append(plan.targets, step)
	}
	return plan, nil
}

// fmAnchorOK drops the external targets a screening cannot cover: the prompt
// and the digit need the media anchor, which a call without an SDP offer does
// not have.
func (c *call) fmAnchorOK(plan *fmPlan) {
	if !plan.confirm || c.isAnchored() {
		return
	}
	kept := plan.targets[:0]
	for _, t := range plan.targets {
		if t.kind == snapshot.FollowMeExternal {
			c.addTrace("Follow-me: " + t.label + " skipped: the call has no media anchor to ask it to press 1")
			c.s.m.FollowMeLegs.WithLabelValues(t.kind, fmSkipped).Inc()
			continue
		}
		kept = append(kept, t)
	}
	plan.targets = kept
}

// ringFollowMe rings the extension's profile for a call that reached its ring
// step. It reports false when nothing would ring (the normal ring step then
// runs); otherwise the call is handled here: answered, forwarded on or ended.
func (c *call) ringFollowMe(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, ext string, e snapshot.Extension, fm snapshot.FollowMe) bool {
	s := c.s
	c.mu.Lock()
	visited := c.stages.visited
	c.mu.Unlock()
	plan, err := c.fmBuildPlan(snap, req.Recipient.Host, headerOf(req), ext, e, fm, visited)
	if err != nil {
		if tx != nil {
			s.stateDown(tx, req, "bindings", err)
		}
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "live state unavailable", ResultFailed)
		return true
	}
	if plan.own == nil && len(plan.targets) == 0 {
		return false
	}
	var first livestate.Binding
	switch {
	case plan.own != nil:
		first = plan.own.bindings[0]
	default:
		for _, t := range plan.targets {
			if len(t.bindings) > 0 {
				first = t.bindings[0]
				break
			}
		}
	}
	ep := EndpointInfo{}
	if first.ContactURI != "" {
		ep = bindingEndpoint(first, first.Extension)
	}
	c.considerAnchor(snap, ep)
	c.fmAnchorOK(&plan)
	if plan.own == nil && len(plan.targets) == 0 {
		return false
	}
	if tx != nil && !c.begin(req, tx) {
		return true
	}
	s.m.Forwarded.WithLabelValues(ForwardFollowMe).Inc()
	c.addTrace(fmt.Sprintf("Extension %s follows me (%s): %s", ext, fm.Strategy, fmDescribe(plan)))
	c.followMeRun(plan)
	return true
}

// fmDescribe lists what a plan rings, for the trace.
func fmDescribe(p fmPlan) string {
	var parts []string
	if p.own != nil {
		parts = append(parts, "own devices")
	}
	for _, t := range p.targets {
		parts = append(parts, t.kind+" "+t.label)
	}
	return strings.Join(parts, ", ")
}

// fmStartStep adds and starts the legs of one step; it returns them with
// their records. External legs take a trunk slot of their own.
func (c *call) fmStartStep(st fmStep, confirm bool, stage int, live map[*leg]*fmRec) []*leg {
	var legs []*leg
	add := func(l *leg, rec *fmRec) {
		l.fmRec = rec
		c.mu.Lock()
		c.addLeg(l)
		c.fmRecs = append(c.fmRecs, rec)
		c.mu.Unlock()
		live[l] = rec
		legs = append(legs, l)
	}
	switch st.kind {
	case snapshot.FollowMeExternal:
		dec := st.dec
		for i, cand := range dec.Candidates {
			t := cand.Trunk
			if t == nil {
				continue
			}
			member := fmt.Sprintf("%s:fm%d", c.id, i)
			if ok, why := c.acquireSlot(t, dec.Emergency, member); !ok {
				c.addTrace(fmt.Sprintf("Follow-me: trunk %s skipped: %s", t.Name, why))
				continue
			}
			n := 0
			for _, d := range cand.Destinations {
				if c.s.trunks.destinationDown(t.ID, d) && !dec.Emergency {
					c.addTrace(fmt.Sprintf("Follow-me: trunk %s destination %s:%d skipped: down", t.Name, d.Host, d.Port))
					continue
				}
				l := &leg{trunk: t, dest: d, addr: c.s.trunks.destAddr(d), number: dec.Number, callerID: dec.CallerID, stage: stage, fmSlot: member}
				if confirm {
					l.accept = c.fmScreen
				}
				add(l, &fmRec{kind: st.kind, ext: st.label, trunk: t.Name, route: dec.Route, number: dec.Number})
				n++
			}
			if n == 0 {
				c.releaseSlot(t.ID, member)
				continue
			}
			c.addTrace(fmt.Sprintf("Follow-me: ringing %s through %s", st.label, t.Name))
			break // the first usable trunk carries the leg, as a forward does
		}
	default:
		for _, b := range st.bindings {
			add(&leg{binding: b, stage: stage}, &fmRec{kind: st.kind, ext: b.Extension})
		}
	}
	for _, l := range legs {
		go l.run()
	}
	return legs
}

// followMeRun is the ring loop of a follow-me: it starts the legs of the plan
// (all at once, or target by target), waits for the first to answer, and on
// the timeout or when every leg has failed hands the call to the extension's
// exhaustion path (no-answer forward, voicemail) like any other ring.
func (c *call) followMeRun(plan fmPlan) {
	delegated := false
	defer func() {
		if !delegated {
			close(c.setupDone)
		}
	}()
	c.mu.Lock()
	stage := c.stageSeq
	c.mu.Unlock()
	settled := false
	settle := func() {
		if settled {
			return
		}
		settled = true
		c.mu.Lock()
		answered := c.winner != nil
		legs := slices.Clone(c.legs)
		c.mu.Unlock()
		c.fmSettleAll(answered)
		if !answered {
			for _, l := range legs {
				c.fmFreeSlot(l) // no leg won: the trunk slots go back at once
			}
		}
	}
	defer settle()

	timer := time.NewTimer(plan.timeout)
	defer timer.Stop()
	live := map[*leg]*fmRec{}
	var (
		total, busy int
		next        int // sequential: the next target to start
		cur         []*leg
		stepT       *time.Timer
		stepC       <-chan time.Time
		waitingWin  bool // a leg won the race for the call; its answer is on the way
	)
	defer func() {
		if stepT != nil {
			stepT.Stop()
		}
	}()
	gone := func(l *leg, result string) {
		rec, ok := live[l]
		if !ok {
			return
		}
		delete(live, l)
		total++
		if result == fmBusy {
			busy++
		}
		c.fmEnd(rec, result)
		c.fmFreeSlot(l)
	}
	startTarget := func() {
		if stepT != nil {
			stepT.Stop()
			stepT, stepC = nil, nil
		}
		for next < len(plan.targets) {
			st := plan.targets[next]
			next++
			cur = c.fmStartStep(st, plan.confirm, stage, live)
			if len(cur) == 0 {
				continue // nothing to ring for this target: the next one
			}
			if st.delay > 0 && next < len(plan.targets) {
				stepT = time.NewTimer(st.delay)
				stepC = stepT.C
			}
			return
		}
	}
	if plan.own != nil {
		c.fmStartStep(*plan.own, plan.confirm, stage, live)
	}
	if plan.sequential {
		startTarget()
	} else {
		for _, st := range plan.targets {
			c.fmStartStep(st, plan.confirm, stage, live)
		}
		next = len(plan.targets)
	}
	exhaust := func() (stop bool) {
		switch c.exhausted(total > 0 && busy == total) {
		case exhRetry: // forwarding moved the call to a new stage: the ordinary loop serves it
			settle() // this ring is over: its legs did not answer
			delegated = true
			c.setup(c.pendingLegs())
			return true
		case exhDone:
			return true
		}
		if c.closeSetup() {
			code, reason, result := sip.StatusTemporarilyUnavailable, "Temporarily Unavailable", ResultUnavailable
			if total > 0 && busy == total {
				code, reason, result = sip.StatusBusyHere, "Busy Here", ResultBusy
			}
			c.respondA(code, reason)
			c.end(code, cdr.SideCallee, reason, result)
			return true
		}
		if c.setupAbort() {
			return true
		}
		waitingWin = true
		return false
	}
	for {
		if !waitingWin && len(live) == 0 && (!plan.sequential || next >= len(plan.targets)) {
			if exhaust() {
				return
			}
		}
		select {
		case ev := <-c.events:
			l := ev.leg
			rec, mine := live[l]
			if !mine {
				continue // an abandoned step, a late event
			}
			switch ev.kind {
			case evRinging:
				c.ringing()
				if l.binding.Extension != "" {
					c.noteRinging(l.binding.Extension)
				}
			case evAnswered:
				c.fmEnd(rec, fmAnswered)
				delete(live, l)
				c.fmWon(l, rec)
				settle() // before the answer: the CDR may be written as soon as the call ends
				c.answer(l)
				return
			case evFailed:
				c.mu.Lock()
				result := l.fmResult
				c.mu.Unlock()
				if result == "" {
					result = fmLegResult(ev.code, ev.reason)
				}
				gone(l, result)
				if plan.sequential && len(cur) > 0 && !slices.ContainsFunc(cur, func(x *leg) bool { _, ok := live[x]; return ok }) {
					startTarget()
				}
			}
		case <-stepC:
			// The target's turn is over: it stops ringing and the next starts.
			stepC = nil
			for _, l := range cur {
				if _, ok := live[l]; ok && c.abandon(l) {
					gone(l, fmNoAnswer)
				}
			}
			startTarget()
		case req := <-c.pickupCh:
			c.answerPickup(req)
			return
		case <-c.canceled:
			if c.closeSetup() {
				c.cancelForks(nil)
				c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
				return
			}
			if c.setupAbort() {
				return
			}
		case <-c.aborted:
			if c.setupAbort() {
				return
			}
		case <-timer.C:
			// The ring is over: settle the legs now, before the no-answer
			// forward can be answered and make them read as cancelled.
			settle()
			switch c.exhausted(false) {
			case exhRetry:
				delegated = true
				c.setup(c.pendingLegs())
				return
			case exhDone:
				return
			}
			if c.closeSetup() {
				c.cancelForks(nil)
				c.respondA(sip.StatusRequestTimeout, "Request Timeout")
				c.end(sip.StatusRequestTimeout, cdr.SideSystem, "ring timeout", ResultNoAnswer)
				return
			}
			if c.setupAbort() {
				return
			}
		}
	}
}

// fmWon prepares the call for the answering leg: the CANCELs of the others
// say the call was completed elsewhere, the winner's trunk is the call's
// trunk and the other external legs give their trunk slots back.
func (c *call) fmWon(w *leg, rec *fmRec) {
	c.mu.Lock()
	legs := slices.Clone(c.legs)
	c.mu.Unlock()
	for _, l := range legs {
		if l == w {
			continue
		}
		// A leg already abandoned (its turn ended) is being cancelled for
		// no answer, not for the call being completed elsewhere; abandon sets
		// the flag under c.mu before it cancels, so holding c.mu here keeps
		// the write to the dialog's UA ahead of that cancel's read of it.
		c.mu.Lock()
		if !l.abandoned {
			l.mu.Lock()
			l.elsewhere = true
			if l.dcs != nil {
				l.dcs.UA = c.s.uacElsewhere
			}
			l.mu.Unlock()
		}
		c.mu.Unlock()
		c.fmFreeSlot(l)
	}
	if w.trunk != nil {
		c.mu.Lock()
		c.trunkName, c.route, c.rewritten = w.trunk.Name, rec.route, rec.number
		c.direction = cdr.DirectionOutbound
		c.mu.Unlock()
		c.addTrace(fmt.Sprintf("Follow-me: %s answered through %s", rec.ext, w.trunk.Name))
	} else {
		c.addTrace(fmt.Sprintf("Follow-me: %s answered", rec.ext))
	}
}

// fmFreeSlot gives back the trunk slot an external leg took, once.
func (c *call) fmFreeSlot(l *leg) {
	if l.trunk == nil || l.fmSlot == "" {
		return
	}
	c.mu.Lock()
	member := l.fmSlot
	l.fmSlot = ""
	c.mu.Unlock()
	if member != "" && !c.fmSlotShared(l, member) {
		c.releaseSlot(l.trunk.ID, member)
	}
}

// fmSlotShared reports whether another leg of the same attempt still uses the
// slot (a trunk with several destinations rings them all on one slot).
func (c *call) fmSlotShared(self *leg, member string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.legs {
		if l != self && l.fmSlot == member {
			return true
		}
	}
	return false
}

// --- screening -----------------------------------------------------------------

// fmDigit feeds a key pressed on the callee side to the screening in
// progress. It reports whether the digit was taken (it never reaches the
// feature-code collection then). l is the leg an INFO came from, nil for an
// RFC 2833 digit on the relay's callee leg.
func (c *call) fmDigit(l *leg, d byte) bool {
	c.mu.Lock()
	sc := c.fmScr
	c.mu.Unlock()
	if sc == nil || (l != nil && l != sc.leg) {
		return false
	}
	if d == '1' {
		select {
		case sc.digits <- d:
		default:
		}
	}
	return true
}

// fmDecided reports whether the call no longer needs leg l's answer.
func (c *call) fmDecided(l *leg) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.winner != nil || c.setupClosed || l.abandoned || c.ended
}

// fmPrompt is the "press 1 to accept" audio: the voice's clip in the
// deployment language, else English, else a beep.
func (c *call) fmPrompt(ctx context.Context) ([]byte, int) {
	if sp := c.s.deps.Speech; sp != nil {
		lang := voiceprompt.Resolve("", c.s.cfg.PromptLanguage)
		langs := []voiceprompt.Lang{lang}
		if lang != voiceprompt.Default {
			langs = append(langs, voiceprompt.Default)
		}
		for _, lg := range langs {
			sctx, cancel := context.WithTimeout(ctx, vmSpeakWait)
			wav, err := sp.Speak(sctx, voiceprompt.Text(lg, voiceprompt.FollowMeAccept, nil))
			cancel()
			if err != nil {
				c.s.log.Debug("follow-me prompt unavailable", "language", string(lg), "error", err)
				continue
			}
			if pcm, rate, err := media.DecodeWAV(wav); err == nil {
				return pcm, rate
			}
		}
	}
	return media.BeepPCM(), 8000
}

// fmScreen asks an answered external leg to press 1. It runs on the leg's
// own goroutine before the leg may claim the call, one leg at a time. The
// result is "" when the digit came; "declined" when it did not within the
// window; "cancelled" when the call no longer needs the leg; "failed" when
// there is no media to ask on.
func (c *call) fmScreen(l *leg) string {
	c.fmScreenMu.Lock()
	defer c.fmScreenMu.Unlock()
	ctx := l.ctx
	if ctx.Err() != nil || c.fmDecided(l) {
		return fmCancelled
	}
	relay := c.anchorRelay()
	dcs := l.session()
	if relay == nil || dcs == nil || dcs.InviteResponse == nil || c.inv == nil {
		return fmFailed
	}
	res := dcs.InviteResponse
	ans, err := media.ParseAudioSDP(res.Body())
	if err != nil {
		return fmFailed
	}
	off, _ := media.ParseAudioSDP(c.inv.Body())
	l.ack(nil, "")
	neg := media.AnswerFor(off, ans)
	relay.SetCodecs(legCallee, neg.Codecs, neg.DTMFPayloadType)
	relay.SetTarget(legCallee, udpAddr(ans.Address, ans.Port))
	allowSignalSource(relay, legCallee, res.Source())
	// Neither side hears the other while the leg is being asked.
	relay.Mute(legCallee, true)
	relay.Mute(legCaller, true)
	defer relay.Mute(legCallee, false)
	defer relay.Mute(legCaller, false)

	sc := &fmScreening{leg: l, digits: make(chan byte, 4)}
	c.mu.Lock()
	c.fmScr = sc
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.fmScr = nil
		c.mu.Unlock()
	}()

	pcm, rate := c.fmPrompt(ctx)
	pctx, stop := context.WithCancel(ctx)
	defer stop()
	played := make(chan struct{})
	go func() {
		defer close(played)
		_ = relay.PlayPCMTo(pctx, legCallee, pcm, rate)
	}()
	done := played
	var window <-chan time.Time
	for {
		select {
		case <-sc.digits:
			stop()
			<-played
			if c.fmDecided(l) || ctx.Err() != nil {
				return fmCancelled
			}
			return ""
		case <-done:
			done = nil
			t := time.NewTimer(fmAcceptWindow)
			defer t.Stop()
			window = t.C
		case <-window:
			c.addTrace("Follow-me: " + l.number + " did not press 1")
			return fmDeclined
		case <-ctx.Done():
			stop()
			<-played
			return fmCancelled
		}
	}
}

// --- follow-me in an IVR dial-out ------------------------------------------------

// ivrFollowMeForks resolves the profile of ext for an IVR dial-out into
// forks and the time they ring; no forks means nothing would ring and the
// ordinary ring applies. visited is the forward chain before ext.
func (c *call) ivrFollowMeForks(snap *snapshot.Snapshot, ext string, e snapshot.Extension, fm snapshot.FollowMe, visited []string) ([]ivrFork, time.Duration) {
	hdr := func(string) string { return "" }
	if c.inv != nil {
		hdr = headerOf(c.inv)
	}
	plan, err := c.fmBuildPlan(snap, c.s.cfg.Domain, hdr, ext, e, fm, append(slices.Clone(visited), ext))
	if err != nil {
		return nil, 0
	}
	c.fmAnchorOK(&plan)
	if plan.own == nil && len(plan.targets) == 0 {
		return nil, 0
	}
	c.s.m.Forwarded.WithLabelValues(ForwardFollowMe).Inc()
	c.addTrace(fmt.Sprintf("Extension %s follows me (%s): %s", ext, fm.Strategy, fmDescribe(plan)))
	return c.fmIvrForks(plan, plan.confirm), plan.timeout
}

// fmIvrForks turns a plan into the forks of an IVR ring. The IVR ring starts
// each fork on its own timer and never stops a fork before the end, so a
// sequential plan starts each target when the targets before it have had
// their turn, and those keep ringing (the ring stops at the plan's timeout or
// the first answer).
func (c *call) fmIvrForks(plan fmPlan, confirm bool) []ivrFork {
	var forks []ivrFork
	fork := func(delay time.Duration, rec *fmRec, mk func(stage int) *leg) {
		forks = append(forks, ivrFork{delay: delay, mk: func(stage int) *leg {
			l := mk(stage)
			l.fmRec = rec
			c.fmRecs = append(c.fmRecs, rec) // c.mu is held by the ring
			return l
		}})
	}
	steps := make([]fmStep, 0, len(plan.targets)+1)
	if plan.own != nil {
		steps = append(steps, *plan.own)
	}
	steps = append(steps, plan.targets...)
	var start time.Duration
	n := 0
	for _, st := range steps {
		delay := time.Duration(0)
		if st.kind != fmOwn && plan.sequential {
			delay = start
			start += st.delay
		}
		n++
		switch st.kind {
		case snapshot.FollowMeExternal:
			dec := st.dec
			for i, cand := range dec.Candidates {
				t := cand.Trunk
				if t == nil {
					continue
				}
				member := fmt.Sprintf("%s:fm%d-%d", c.id, n, i)
				if ok, why := c.acquireSlot(t, dec.Emergency, member); !ok {
					c.addTrace(fmt.Sprintf("Follow-me: trunk %s skipped: %s", t.Name, why))
					continue
				}
				used := 0
				for _, d := range cand.Destinations {
					if c.s.trunks.destinationDown(t.ID, d) && !dec.Emergency {
						continue
					}
					used++
					rec := &fmRec{kind: st.kind, ext: st.label, trunk: t.Name, route: dec.Route, number: dec.Number}
					d := d
					fork(delay, rec, func(stage int) *leg {
						l := &leg{trunk: t, dest: d, addr: c.s.trunks.destAddr(d), number: dec.Number, callerID: dec.CallerID, stage: stage, fmSlot: member}
						if confirm {
							l.accept = c.fmScreen
						}
						return c.addLeg(l)
					})
				}
				if used == 0 {
					c.releaseSlot(t.ID, member)
					continue
				}
				break
			}
		default:
			for _, b := range st.bindings {
				b := b
				fork(delay, &fmRec{kind: st.kind, ext: b.Extension}, func(stage int) *leg {
					return c.addLeg(&leg{binding: b, stage: stage})
				})
			}
		}
	}
	return forks
}

// fmIvrDone settles the legs of an IVR follow-me ring: the winner (nil when
// nobody answered) is the call's, every other leg gets the result it ended
// with or is marked cancelled or unanswered, and the trunk slots of the
// legs that did not win go back.
func (c *call) fmIvrDone(w *leg) {
	c.mu.Lock()
	legs := slices.Clone(c.legs)
	c.mu.Unlock()
	if w != nil && w.fmRec != nil {
		c.fmEnd(w.fmRec, fmAnswered)
		c.fmWon(w, w.fmRec)
	}
	for _, l := range legs {
		if l.fmRec == nil || l == w {
			continue
		}
		c.mu.Lock()
		res := l.fmResult
		c.mu.Unlock()
		if res == "" {
			res = fmNoAnswer
			if w != nil {
				res = fmCancelled
			}
		}
		c.fmEnd(l.fmRec, res)
		if w == nil {
			c.fmFreeSlot(l)
		}
	}
}
