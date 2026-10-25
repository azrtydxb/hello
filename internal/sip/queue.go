// ACD call queues (migration 00023). A queue answers the call through the
// media relay, holds the caller with music and periodic announcements and
// offers the call to the next free agent. The caller's dialog is the
// ordinary answered B2BUA dialog (like an IVR menu's), so the queue reuses
// the IVR machinery: the relay's caller leg plays the music, the agents'
// legs are forked with ivrRing and the winner is bridged by ivrConnect.
//
// What the nodes share lives in Valkey (internal/queue): the waiting callers
// and their order, the agents' login and pause state and the leases. The node
// holding a caller's dialog is the only one that offers that call; a lease
// per call and per agent keeps even a taking-over survivor and the node it
// replaces from ringing the same agent twice.
package sip

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// Limits and timings of a queue session.
const (
	// queueMaxDepth bounds queue-to-queue failover chains at run time.
	queueMaxDepth = 8
	// queueTick is how often a waiting call looks at its queue.
	queueTick = 500 * time.Millisecond
	// queueTouch is the entry heartbeat, well inside queue.EntryTTL.
	queueTouch = 2 * time.Second
	// queueEmptyCheck is how often a waiting call re-counts the staffed
	// agents for leave-when-empty.
	queueEmptyCheck = 3 * time.Second
	// haPhaseQueue is the replicated phase of a queued caller; its detail is
	// "queue:<name>:<entered ms>".
	haPhaseQueue        = "queue"
	haQueueDetailPrefix = "queue:"
)

// MOHProvider supplies music on hold by class name. The music-on-hold
// feature implements it; until it does the queue plays the built-in tone.
type MOHProvider interface {
	// PCM returns the class's audio as 16-bit mono 8 kHz PCM; false when the
	// class is unknown or empty (the built-in tone plays instead).
	PCM(ctx context.Context, class string) ([]byte, bool)
}

// queueStore is the queue state, nil when the node has none.
func (s *Server) queueStore() queue.Store { return s.deps.Queues }

// --- entry -------------------------------------------------------------------

// startQueue answers the call and runs the queue. tx is the INVITE's server
// transaction while the call has not begun; nil when the call already began
// (a ring group's failure destination).
func (c *call) startQueue(tx sip.ServerTransaction, snap *snapshot.Snapshot, q snapshot.Queue) {
	if !q.Enabled {
		c.ivrReject(tx, sip.StatusNotFound, "queue "+q.Name+" is disabled", ResultNotFound)
		return
	}
	if c.s.queueStore() == nil {
		c.ivrReject(tx, sip.StatusServiceUnavailable, "call queues need Valkey", ResultUnavailable)
		return
	}
	if tx != nil {
		c.considerAnchor(snap, EndpointInfo{})
	} else if !c.isAnchored() {
		c.startAnchor(snap, AnchorPolicy)
	}
	if !c.isAnchored() {
		c.ivrReject(tx, sip.StatusNotAcceptableHere, "a queue needs a G.711 SDP offer and a media port", ResultFailed)
		return
	}
	if tx != nil && !c.begin(c.inv, tx) {
		return
	}
	st := newIVRState()
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.ivr, c.ivrCancel = st, cancel
	c.setupDone = make(chan struct{})
	c.mu.Unlock()
	c.addTrace("Queue " + q.Name)
	if !c.soloAnswer(haPhaseQueue, haQueueDetailPrefix+q.Name+":0", "queue") {
		cancel()
		return
	}
	go func() {
		defer contain(c.s.log, "queue flow")
		c.queueSession(ctx, snap, q, 0)
	}()
}

// queueResume restarts a queued caller on a call a survivor took over: the
// taker's relay already carries the caller, the caller keeps its place in the
// queue (its entry, with the original time, is restored if it was dropped)
// and hears the music again.
func (c *call) queueResume(detail string) {
	parts := strings.SplitN(strings.TrimPrefix(detail, haQueueDetailPrefix), ":", 2)
	name, entered := parts[0], int64(0)
	if len(parts) == 2 {
		entered, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	relay := c.anchorRelay()
	snap := c.s.deps.Snapshots.Current()
	var q snapshot.Queue
	ok := false
	if snap != nil {
		q, ok = snap.Queue(name)
	}
	if !ok || relay == nil || c.s.queueStore() == nil {
		c.addTrace("Queue " + name + " is gone: ending the call")
		c.ivrHangup()
		return
	}
	st := newIVRState()
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.ivr, c.ivrCancel = st, cancel
	c.mu.Unlock()
	relay.OnDTMF(func(leg string, digit byte) { c.relayDTMF(leg, digit) })
	relay.OnPacket(c.recTap)
	c.mu.Lock()
	if c.events == nil {
		c.events = make(chan legEvent, 16) // a taken-over call has not rung anything yet
	}
	c.mu.Unlock()
	c.addTrace("Queue " + name + ": the caller keeps its place after the takeover")
	go func() {
		defer contain(c.s.log, "queue flow after takeover")
		c.queueRun(ctx, snap, q, entered)
	}()
}

// queueSession puts the (answered) caller in q and carries the call to its
// end: an agent, or a failover destination.
func (c *call) queueSession(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue, entered int64) {
	c.queueRun(ctx, snap, q, entered)
}

// --- the wait ----------------------------------------------------------------

// mohPlayer plays the queue's music to the caller until stopped; announcements
// stop it and start it again, so the two never overlap on the relay.
type mohPlayer struct {
	c    *call
	pcm  []byte
	mu   sync.Mutex
	stop context.CancelFunc
	done chan struct{}
}

func (p *mohPlayer) start(parent context.Context) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stop != nil {
		return
	}
	relay := p.c.anchorRelay()
	if relay == nil || len(p.pcm) == 0 {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	p.stop, p.done = cancel, make(chan struct{})
	done := p.done
	go func() {
		defer close(done)
		defer contain(p.c.s.log, "queue music")
		for ctx.Err() == nil {
			if err := relay.PlayTo(ctx, legCaller, p.pcm); err != nil && ctx.Err() == nil {
				select { // a relay that cannot play must not spin
				case <-time.After(time.Second):
				case <-ctx.Done():
				}
			}
		}
	}()
}

func (p *mohPlayer) halt() {
	p.mu.Lock()
	cancel, done := p.stop, p.done
	p.stop, p.done = nil, nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// queueMusic returns the audio a waiting caller hears: the queue's class from
// the music-on-hold provider, else the built-in tone.
func (c *call) queueMusic(ctx context.Context, q snapshot.Queue) []byte {
	if p := c.s.deps.MOH; p != nil {
		if pcm, ok := p.PCM(ctx, q.MOHClass); ok && len(pcm) > 0 {
			return pcm
		}
	}
	return media.HoldTonePCM()
}

// qLeg adds an event to the call's queue legs (its CDR).
func (c *call) qLeg(event, queueName, ext string, wait time.Duration, result string) {
	c.mu.Lock()
	c.qLegs = append(c.qLegs, cdr.QueueLeg{Event: event, Queue: queueName, Extension: ext, WaitMs: wait.Milliseconds(), Result: result, At: time.Now()})
	c.mu.Unlock()
}

func (c *call) queueLog(msg, name string, kv ...any) {
	args := append([]any{"component", "sip", "queue", name, "correlation_id", c.id, "call_id", c.callID, "caller", c.callerNum}, kv...)
	c.s.log.Info(msg, args...)
}

// queueRun is the life of one caller in q, from joining to leaving. entered is
// the caller's original join time (Unix ms) when it is restored after a
// takeover, else 0.
func (c *call) queueRun(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue, entered int64) {
	s := c.s
	st := s.queueStore()
	c.mu.Lock()
	c.qDepth++
	depth := c.qDepth
	c.mu.Unlock()
	if depth > queueMaxDepth {
		c.addTrace("Queue failover chain too long: ending the call")
		c.ivrHangup()
		return
	}
	if !q.Enabled || st == nil {
		c.addTrace("Queue " + q.Name + " is unavailable")
		c.ivrHangup()
		return
	}
	menu := snapshot.IVRMenu{Name: q.Name, Number: q.Number}
	staffed, free := c.queueAgents(ctx, snap, q)
	if staffed == 0 && !q.JoinEmpty {
		c.queueLog("queue no agents", q.Name)
		c.qLeg(cdr.QueueEnter, q.Name, "", 0, "no_agents")
		c.queueLeave(ctx, snap, q, menu, "no_agents", q.NoAgents, 0)
		return
	}
	entry, err := st.Join(ctx, queue.Entry{CallID: c.id, Queue: q.Name, Caller: c.callerNum, CallerName: c.callerName,
		Node: s.cfg.NodeID, EnteredMs: entered}, q.MaxCallers)
	switch {
	case err == queue.ErrFull:
		c.queueLog("queue full", q.Name)
		c.qLeg(cdr.QueueEnter, q.Name, "", 0, "full")
		c.queueLeave(ctx, snap, q, menu, "full", q.Full, 0)
		return
	case err != nil:
		if ctx.Err() == nil {
			s.log.Warn("queue state unavailable", "queue", q.Name, "correlation_id", c.id, "error", err)
			c.addTrace("Queue " + q.Name + ": queue state unavailable")
			c.ivrHangup()
		}
		return
	}
	enteredAt := entry.Entered()
	c.mu.Lock()
	c.qWaiting, c.qEnteredAt = true, enteredAt
	c.qName, c.qWrap = q.Name, time.Duration(q.WrapUp)*time.Second
	if c.haPhase == haPhaseQueue {
		c.haDetail = haQueueDetailPrefix + q.Name + ":" + strconv.FormatInt(entry.EnteredMs, 10)
	}
	c.mu.Unlock()
	c.queueLog("queue entered", q.Name, "staffed", staffed, "free", len(free))
	c.addTrace(fmt.Sprintf("Queue %s entered (%d agents staffed)", q.Name, staffed))
	c.qLeg(cdr.QueueEnter, q.Name, "", 0, "")
	s.m.QueueCalls.WithLabelValues(q.Name, "entered").Inc()
	s.m.QueueWaiting.WithLabelValues(q.Name).Inc()
	left := false
	waiting := true
	defer func() {
		if waiting {
			s.m.QueueWaiting.WithLabelValues(q.Name).Dec()
		}
		c.mu.Lock()
		yielded := c.haYielded
		c.mu.Unlock()
		if left || yielded { // a call handed to another node keeps its place
			return
		}
		lctx, cancel := s.stateCtx()
		defer cancel()
		_ = st.Leave(lctx, q.Name, c.id)
	}()
	// leave records the way out of the queue other than an agent.
	leave := func(why string, dest snapshot.QueueDest) {
		left = true
		c.queueNotWaiting()
		if waiting {
			waiting = false
			s.m.QueueWaiting.WithLabelValues(q.Name).Dec()
		}
		lctx, cancel := s.stateCtx()
		_ = st.Leave(lctx, q.Name, c.id)
		cancel()
		c.queueLeave(ctx, snap, q, menu, why, dest, time.Since(enteredAt))
	}

	moh := &mohPlayer{c: c, pcm: c.queueMusic(ctx, q)}
	defer moh.halt()
	moh.start(ctx)

	c.mu.Lock()
	digits := c.ivr.digits
	c.mu.Unlock()
	tick := time.NewTicker(queueTick)
	defer tick.Stop()
	start := time.Now()
	lastTouch, lastAnn, lastEmpty := start, start, start
	nextOffer := start
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			// The node is going down: the caller keeps its place for the
			// node that takes the call over.
			left = true
			return
		case d := <-digits:
			if c.queueBreakout(snap, q, d) {
				moh.halt()
				leave("breakout", snapshot.QueueDest{Kind: snapshot.IVRVoicemail, Target: q.BreakoutBox})
				return
			}
		case <-tick.C:
		}
		now := time.Now()
		wait := now.Sub(enteredAt)
		if q.MaxWait > 0 && wait >= time.Duration(q.MaxWait)*time.Second {
			moh.halt()
			leave("timeout", q.Timeout)
			return
		}
		if now.Sub(lastTouch) >= queueTouch {
			lastTouch = now
			lctx, cancel := s.stateCtx()
			ok, err := st.Touch(lctx, q.Name, c.id, s.cfg.NodeID, "")
			if err == nil && !ok {
				// The entry was dropped (a long stall): back in at the same place.
				_, err = st.Join(lctx, queue.Entry{CallID: c.id, Queue: q.Name, Caller: c.callerNum, CallerName: c.callerName,
					Node: s.cfg.NodeID, EnteredMs: entry.EnteredMs}, 0)
			}
			cancel()
			if err != nil {
				s.log.Warn("queue heartbeat failed", "queue", q.Name, "correlation_id", c.id, "error", err)
			}
		}
		if q.LeaveEmpty && now.Sub(lastEmpty) >= queueEmptyCheck {
			lastEmpty = now
			if n, _ := c.queueAgents(ctx, snap, q); n == 0 {
				moh.halt()
				leave("no_agents", q.NoAgents)
				return
			}
		}
		if q.AnnounceFrequency > 0 && now.Sub(lastAnn) >= time.Duration(q.AnnounceFrequency)*time.Second {
			lastAnn = time.Now()
			moh.halt()
			if broke := c.queueAnnounce(ctx, snap, q, entry); broke {
				leave("breakout", snapshot.QueueDest{Kind: snapshot.IVRVoicemail, Target: q.BreakoutBox})
				return
			}
			lastAnn = time.Now()
			moh.start(ctx)
		}
		if now.Before(nextOffer) {
			continue
		}
		w, ext, release := c.queueOffer(ctx, snap, q)
		if ctx.Err() != nil {
			if release != nil {
				release()
			}
			return
		}
		if w == nil {
			nextOffer = time.Now().Add(time.Duration(q.RetryDelay) * time.Second)
			continue
		}
		moh.halt()
		wait = time.Since(enteredAt)
		left, waiting = true, false
		c.queueNotWaiting()
		s.m.QueueWaiting.WithLabelValues(q.Name).Dec()
		lctx, cancel := s.stateCtx()
		_ = st.Leave(lctx, q.Name, c.id)
		_ = st.NoteAnswered(lctx, q.Name, ext)
		cancel()
		c.qLeg(cdr.QueueWait, q.Name, ext, wait, "answered")
		c.qLeg(cdr.QueueAnswered, q.Name, ext, wait, "")
		s.m.QueueWait.WithLabelValues(q.Name, "answered").Observe(wait.Seconds())
		s.m.QueueCalls.WithLabelValues(q.Name, "answered").Inc()
		c.mu.Lock()
		c.qAgent = ext
		c.mu.Unlock()
		c.queueLog("queue answered", q.Name, "agent", ext, "wait", wait.String())
		c.addTrace(fmt.Sprintf("Queue %s: answered by %s after %s", q.Name, ext, wait.Round(time.Second)))
		c.ivrConnect(w, ext)
		release() // the agent shows as busy now
		return
	}
}

// queueBreakout reports whether the key is the queue's voicemail breakout and
// a box is there to take the message.
func (c *call) queueBreakout(snap *snapshot.Snapshot, q snapshot.Queue, d byte) bool {
	if q.BreakoutKey == "" || q.BreakoutBox == "" || q.BreakoutKey[0] != d {
		return false
	}
	e, ok := snap.Extension(q.BreakoutBox)
	return ok && c.voicemailConfigured(e)
}

// queueLeave takes the caller out of the queue to a failover destination: the
// call has left the queue, the destination decides what happens next.
func (c *call) queueLeave(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue, menu snapshot.IVRMenu, why string, dest snapshot.QueueDest, wait time.Duration) {
	s := c.s
	c.qLeg(cdr.QueueWait, q.Name, "", wait, why)
	c.qLeg(cdr.QueueExit, q.Name, "", wait, why)
	s.m.QueueWait.WithLabelValues(q.Name, why).Observe(wait.Seconds())
	s.m.QueueCalls.WithLabelValues(q.Name, "exit_"+why).Inc()
	c.queueLog("queue exit", q.Name, "reason", why, "kind", dest.Kind, "target", dest.Target, "wait", wait.String())
	next, done := c.ivrLeave(ctx, snap, menu, ivrExit{dest: dest, why: "queue " + q.Name + " " + why, auto: true})
	switch {
	case done || ctx.Err() != nil:
	case next != "":
		nm, ok := snap.IVRMenu(next)
		if !ok || !nm.Enabled {
			c.addTrace(fmt.Sprintf("IVR menu %s is unavailable", next))
			c.ivrHangup()
			return
		}
		c.ivrRun(ctx, nm)
	default:
		c.ivrHangup() // the destination could not take the call
	}
}

// --- agents ------------------------------------------------------------------

// queueAgents counts the agents that staff the queue (logged in, not paused,
// not in DND, a phone registered: busy ones count, they will be free) and
// lists the ones that can take a call now.
func (c *call) queueAgents(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue) (staffed int, free []queue.Candidate) {
	s := c.s
	st := s.queueStore()
	lctx, cancel := s.stateCtx()
	defer cancel()
	states, err := st.Agents(lctx)
	if err != nil {
		s.log.Warn("queue agents unavailable", "queue", q.Name, "error", err)
		return 0, nil
	}
	stats, err := st.Stats(lctx, q.Name)
	if err != nil {
		s.log.Warn("queue statistics unavailable", "queue", q.Name, "error", err)
	}
	busy := s.busyExtensions()
	now := time.Now()
	for _, m := range q.Members {
		as, known := states[m.Extension]
		if !(queue.Member{Ext: m.Extension, Dynamic: m.Dynamic}).Staffed(q.Name, as, known) {
			continue
		}
		if e, _ := snap.Extension(m.Extension); e.DND {
			continue
		}
		if bs, err := c.extensionBindings(snap, m.Extension); err != nil || len(bs) == 0 {
			continue
		}
		staffed++
		ss := stats[m.Extension]
		if busy[m.Extension] || ss.Wrapping(now) {
			continue
		}
		free = append(free, queue.Candidate{Ext: m.Extension, Penalty: m.Penalty, Position: m.Position, Calls: ss.Calls, LastEnd: ss.LastEnd()})
	}
	return staffed, free
}

// queueOffer offers the caller to the free agents when it is next in line.
// It returns the leg that answered and the agent, nil when nobody did.
// release frees the winner's lease once the call is bridged.
func (c *call) queueOffer(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue) (w *leg, agent string, release func()) {
	s := c.s
	st := s.queueStore()
	lctx, cancel := s.stateCtx()
	waiting, err := st.Waiting(lctx, q.Name)
	cancel()
	if err != nil || !queue.NextInLine(waiting, c.id) {
		return nil, "", nil
	}
	_, free := c.queueAgents(ctx, snap, q)
	if len(free) == 0 {
		return nil, "", nil
	}
	token := s.cfg.NodeID + "/" + c.id
	rr := uint64(0)
	if q.Strategy == queue.RoundRobin {
		lctx, cancel := s.stateCtx()
		rr, _ = st.NextRotation(lctx, q.Name)
		cancel()
	}
	rounds := queue.Plan(q.Strategy, free, rr, queue.Seed(q.Name, c.id))
	timeout := time.Duration(q.AgentTimeout) * time.Second
	// The call's lease: only this node offers the call, for as long as the
	// rounds can last.
	lctx, cancel = s.stateCtx()
	ok, err := st.Acquire(lctx, queue.CallLease(c.id), token, time.Duration(len(rounds))*timeout+10*time.Second)
	cancel()
	if err != nil || !ok {
		return nil, "", nil
	}
	defer func() {
		lctx, cancel := s.stateCtx()
		_ = st.Release(lctx, queue.CallLease(c.id), token)
		cancel()
	}()
	for _, round := range rounds {
		if ctx.Err() != nil {
			return nil, "", nil
		}
		held := c.queueLeaseAgents(round, token, timeout+5*time.Second)
		if len(held) == 0 {
			continue
		}
		won, ext := c.queueRing(ctx, snap, q, held, timeout)
		for _, h := range held {
			lctx, cancel := s.stateCtx()
			if won == nil || h != ext {
				_ = st.Release(lctx, queue.AgentLease(h), token)
			}
			cancel()
		}
		if won != nil {
			return won, ext, func() {
				lctx, cancel := s.stateCtx()
				_ = st.Release(lctx, queue.AgentLease(ext), token)
				cancel()
			}
		}
		lctx, cancel := s.stateCtx()
		_, _ = st.Touch(lctx, q.Name, c.id, s.cfg.NodeID, "")
		cancel()
	}
	return nil, "", nil
}

// queueLeaseAgents takes the lease of each agent of the round; the ones it
// could not (rung for another caller right now) are left out.
func (c *call) queueLeaseAgents(round []string, token string, ttl time.Duration) []string {
	s := c.s
	var held []string
	for _, ext := range round {
		lctx, cancel := s.stateCtx()
		ok, err := s.queueStore().Acquire(lctx, queue.AgentLease(ext), token, ttl)
		cancel()
		if err == nil && ok {
			held = append(held, ext)
		}
	}
	return held
}

// queueRing rings the agents together and waits for the first to answer.
func (c *call) queueRing(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue, agents []string, timeout time.Duration) (*leg, string) {
	s := c.s
	var forks []ivrFork
	for _, ext := range agents {
		bs, err := c.extensionBindings(snap, ext)
		if err != nil {
			continue
		}
		for _, b := range bs {
			forks = append(forks, ivrFork{mk: func(stage int) *leg { return c.addLeg(&leg{binding: b, stage: stage}) }})
		}
	}
	if len(forks) == 0 {
		return nil, ""
	}
	lctx, cancel := s.stateCtx()
	_, _ = s.queueStore().Touch(lctx, q.Name, c.id, s.cfg.NodeID, strings.Join(agents, ","))
	cancel()
	c.addTrace(fmt.Sprintf("Queue %s: ringing %s", q.Name, strings.Join(agents, ", ")))
	w, busyAll := c.ivrRing(ctx, forks, false, timeout)
	lctx, cancel = s.stateCtx()
	_, _ = s.queueStore().Touch(lctx, q.Name, c.id, s.cfg.NodeID, "")
	cancel()
	if w == nil {
		result := "no_answer"
		if busyAll {
			result = "busy"
		}
		for _, ext := range agents {
			c.qLeg(cdr.QueueAgent, q.Name, ext, 0, result)
		}
		return nil, ""
	}
	ext := w.binding.Extension
	for _, a := range agents {
		if a != ext {
			c.qLeg(cdr.QueueAgent, q.Name, a, 0, "no_answer")
		}
	}
	c.qLeg(cdr.QueueAgent, q.Name, ext, 0, "answered")
	return w, ext
}

// queueEnded records the end of an agent's call: the agent is idle from now
// and wraps up for the queue's wrap-up time.
func (c *call) queueEnded() {
	c.mu.Lock()
	name, agent, wrap, yielded := c.qName, c.qAgent, c.qWrap, c.haYielded
	answered := c.answerTime
	c.mu.Unlock()
	if name == "" || agent == "" || yielded || c.s.queueStore() == nil {
		return
	}
	talk := time.Duration(0)
	if !answered.IsZero() {
		talk = time.Since(answered)
	}
	ctx, cancel := c.s.stateCtx()
	defer cancel()
	if err := c.s.queueStore().NoteEnded(ctx, name, agent, talk, wrap); err != nil {
		c.s.log.Warn("queue agent statistics not saved", "queue", name, "agent", agent, "error", err)
	}
}

// --- announcements -----------------------------------------------------------

// queueAnnounce plays the periodic prompt, the caller's position and the
// estimated wait. A press of the breakout key interrupts it and reports true.
func (c *call) queueAnnounce(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue, entry queue.Entry) bool {
	s := c.s
	st := s.queueStore()
	lctx, cancel := s.stateCtx()
	waiting, _ := st.Waiting(lctx, q.Name)
	avg, _ := st.AvgHandle(lctx, q.Name)
	cancel()
	pos := queue.Position(waiting, c.id)
	staffed, _ := c.queueAgents(ctx, snap, q)
	est := queue.EstimatedWait(pos, avg, staffed)
	var seq []string
	seq = append(seq, q.PeriodicPrompt)
	if q.AnnouncePosition && pos > 0 {
		seq = append(seq, q.PositionPrompt)
		seq = append(seq, numberPrompts(q.DigitsPrefix, pos)...)
	}
	if q.AnnounceWait && pos > 0 && staffed > 0 {
		seq = append(seq, q.WaitPrompt)
		seq = append(seq, numberPrompts(q.DigitsPrefix, queue.Minutes(est))...)
		seq = append(seq, q.MinutesPrompt)
	}
	if q.BreakoutKey != "" {
		seq = append(seq, q.BreakoutPrompt)
	}
	c.addTrace(fmt.Sprintf("Queue %s: announcing position %d, about %d min", q.Name, pos, queue.Minutes(est)))
	for _, name := range ivrPrompts(seq...) {
		d, hit, alive := c.ivrPlay(ctx, snap, name, true)
		if !alive {
			return false
		}
		if hit && c.queueBreakout(snap, q, d) {
			return true
		}
	}
	return false
}

// numberPrompts names the announcements that speak n digit by digit:
// prefix0 to prefix9.
func numberPrompts(prefix string, n int) []string {
	var out []string
	for _, ch := range strconv.Itoa(max(n, 0)) {
		out = append(out, prefix+string(ch))
	}
	return out
}

// --- feature codes -----------------------------------------------------------

// handleQueueCode applies an agent feature code dialled as a call: login,
// logout, pause or unpause for the calling extension. The argument of a
// login is an optional queue number (log in to that queue only).
func (c *call) handleQueueCode(tx sip.ServerTransaction, fc snapshot.FeatureCode, arg string) {
	s := c.s
	st := s.queueStore()
	snap := s.deps.Snapshots.Current()
	fail := func(code int, why, result string) {
		s.respond(tx, c.inv, code, statusText(code))
		c.record(code, cdr.SideSystem, why, result)
	}
	if st == nil || snap == nil {
		fail(sip.StatusServiceUnavailable, "queue state unavailable", ResultUnavailable)
		return
	}
	ext := c.callerNum
	mine := queuesOf(snap, ext)
	if len(mine) == 0 {
		fail(sip.StatusForbidden, "extension "+ext+" is not an agent of any queue", ResultFailed)
		return
	}
	ctx, cancel := s.stateCtx()
	defer cancel()
	cur, _, err := st.Agent(ctx, ext)
	if err != nil {
		fail(sip.StatusServiceUnavailable, "queue state unavailable", ResultUnavailable)
		return
	}
	next, err := applyAgentCode(cur, ext, fc, arg, mine, snap, time.Now())
	if err != nil {
		fail(sip.StatusNotFound, err.Error(), ResultNotFound)
		return
	}
	if err := st.SetAgent(ctx, next); err != nil {
		fail(sip.StatusServiceUnavailable, "queue state unavailable", ResultUnavailable)
		return
	}
	if !c.begin(c.inv, tx) {
		return
	}
	c.addTrace("Feature code " + fc.Code + " (" + fc.Action + ") applied")
	c.queueLog("queue agent "+strings.TrimPrefix(fc.Action, "queue_"), strings.Join(next.Queues, ","), "agent", ext)
	c.answerSelf(c.inv.Body())
}

// queuesOf lists the names of the queues ext is an agent of.
func queuesOf(snap *snapshot.Snapshot, ext string) []string {
	var out []string
	for _, q := range snap.Queues() {
		for _, m := range q.Members {
			if m.Extension == ext {
				out = append(out, q.Name)
				break
			}
		}
	}
	slices.Sort(out)
	return out
}

// applyAgentCode is the agent state after a feature code: the pure part of
// handleQueueCode.
func applyAgentCode(cur queue.AgentState, ext string, fc snapshot.FeatureCode, arg string, mine []string, snap *snapshot.Snapshot, now time.Time) (queue.AgentState, error) {
	cur.Ext = ext
	switch fc.Action {
	case ActionQueueLogin:
		cur.LoggedIn, cur.Queues = true, nil
		if arg != "" {
			q, ok := snap.QueueByNumber(arg)
			if !ok || !slices.Contains(mine, q.Name) {
				return cur, fmt.Errorf("extension %s is not an agent of the queue %s", ext, arg)
			}
			cur.Queues = []string{q.Name}
		}
	case ActionQueueLogout:
		cur.LoggedIn, cur.Queues, cur.Paused, cur.Reason = false, nil, false, ""
	case ActionQueuePause:
		cur.Paused, cur.Reason = true, orDefault(fc.Argument, "pause")
	case ActionQueueUnpause:
		cur.Paused, cur.Reason = false, ""
	}
	cur.SinceMs = now.UnixMilli()
	return cur, nil
}

// queueNotWaiting marks the caller as no longer waiting in the queue.
func (c *call) queueNotWaiting() {
	c.mu.Lock()
	c.qWaiting = false
	c.mu.Unlock()
}

// queueAbandoned records, when the call ends while its caller still waits in
// a queue, that the caller gave up: the CDR's wait and abandon legs, the
// counters and the wait histogram. It runs from end(), before the CDR is
// built, so the legs are in it.
func (c *call) queueAbandoned() {
	c.mu.Lock()
	if !c.qWaiting {
		c.mu.Unlock()
		return
	}
	c.qWaiting = false
	name, since := c.qName, c.qEnteredAt
	c.mu.Unlock()
	wait := time.Since(since)
	c.qLeg(cdr.QueueWait, name, "", wait, "abandoned")
	c.qLeg(cdr.QueueAbandon, name, "", wait, "")
	c.s.m.QueueAbandoned.WithLabelValues(name).Inc()
	c.s.m.QueueWait.WithLabelValues(name, "abandoned").Observe(wait.Seconds())
	c.s.m.QueueCalls.WithLabelValues(name, "abandoned").Inc()
	c.queueLog("queue abandoned", name, "wait", wait.String())
}
