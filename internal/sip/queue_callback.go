// Queue callback (pbx-gaps S-9, S-10; migration 00054, docs/queues.md).
//
// A caller who would wait long is offered a call back instead. Accepting
// writes a durable queue_callbacks row (the caller's number, and its original
// enqueue time as position_key) and ends the call. A sweep, on every SIP
// node, claims the head request when an agent could take it and rings the
// caller first; the agent is rung only after the caller has answered, so no
// agent waits on an absent customer.
//
// The answered customer is an ordinary queued caller from then on: the
// callback call is built as a call whose only dialog is the customer's (the
// shape a survivor rebuilds after a takeover), joins the queue with
// EnteredMs = position_key (so it sorts where it always did), hears music and
// is offered to an agent by queueOffer, and is bridged by ivrConnect. That
// reuse is also what carries a callback over a node failure: the queued
// caller is replicated with its callback id (the "queue:" phase detail), a
// survivor re-homes it and keeps serving the request. A node lost before the
// customer answers leaves the row dialling; its 60 s lease lapses and
// another node claims it again.
package sip

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
	"github.com/emiago/sipgo/sip"
)

// Timings of the callback work.
const (
	// callbackPurgeEvery is how often finished requests are purged.
	callbackPurgeEvery = 10 * time.Minute
	// callbackDBWait bounds one PostgreSQL call of the callback work, which
	// runs off the SIP transaction path.
	callbackDBWait = 3 * time.Second
	// haQueueCallbackMark separates the callback id in the replicated queue
	// detail: "queue:<name>:<entered ms>:cb<id>".
	haQueueCallbackMark = "cb"
)

// Vars so tests shrink them: how often a node sweeps the requests (Config's
// CallbackInterval overrides it per node), how often a waiting caller's
// estimated wait is compared with the offer threshold, and how long the
// caller is rung.
var (
	callbackEvery       = 3 * time.Second
	callbackOfferCheck  = 3 * time.Second
	callbackRingTimeout = 30 * time.Second
	// callbackWatchEvery is how often a ringing attempt, or a caller on the
	// line, checks that the request was not cancelled (and renews its lease).
	callbackWatchEvery = 2 * time.Second
)

// callbackNumberRe is a number a callback can dial: digits with an optional
// leading plus. A caller that is anonymous or has no number does not match,
// and is never offered a callback.
var callbackNumberRe = regexp.MustCompile(`^\+?[0-9]{2,20}$`)

// callbackNumber is the number to call back for the caller as the call
// presented it.
func callbackNumber(caller string) (string, bool) {
	n := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(caller)
	return n, callbackNumberRe.MatchString(n)
}

// queueDetail is the replicated phase detail of a queued caller: the queue,
// its place (the original enqueue time) and, for a callback, the request.
func queueDetail(name string, enteredMs, cbID int64) string {
	d := haQueueDetailPrefix + name + ":" + strconv.FormatInt(enteredMs, 10)
	if cbID != 0 {
		d += ":" + haQueueCallbackMark + strconv.FormatInt(cbID, 10)
	}
	return d
}

// parseQueueDetail reads queueDetail back.
func parseQueueDetail(detail string) (name string, enteredMs, cbID int64) {
	parts := strings.SplitN(strings.TrimPrefix(detail, haQueueDetailPrefix), ":", 3)
	if len(parts) > 1 {
		enteredMs, _ = strconv.ParseInt(parts[1], 10, 64)
	}
	if len(parts) > 2 {
		if id, ok := strings.CutPrefix(parts[2], haQueueCallbackMark); ok {
			cbID, _ = strconv.ParseInt(id, 10, 64)
		}
	}
	return parts[0], enteredMs, cbID
}

// queueLang is the language the queue speaks to its callers: its own, else
// the deployment's, else English (S-20).
func (s *Server) queueLang(q snapshot.Queue) voiceprompt.Lang {
	return voiceprompt.Resolve(q.PromptLanguage, s.cfg.PromptLanguage)
}

// --- speaking ----------------------------------------------------------------

// queueSpeak speaks text to the caller in the queue's voice: synthesized (or
// fetched from the cache) and played on the relay's caller leg, a key press
// stopping it when barge is set. ok is false when speech is unavailable, in
// which case nothing was played.
func (c *call) queueSpeak(ctx context.Context, text string, barge bool) (digit byte, hit, alive, ok bool) {
	sp := c.s.deps.Speech
	if sp == nil || text == "" {
		return 0, false, ctx.Err() == nil, false
	}
	sctx, cancel := context.WithTimeout(ctx, vmSpeakWait)
	wav, err := sp.Speak(sctx, text)
	cancel()
	if err != nil {
		c.s.log.Debug("queue prompt unavailable", "error", err)
		return 0, false, ctx.Err() == nil, false
	}
	pcm, rate, err := media.DecodeWAV(wav)
	if err != nil {
		return 0, false, ctx.Err() == nil, false
	}
	digit, hit, alive = c.ivrPlayAudio(ctx, ivrAudio{pcm: pcm, rate: rate}, "queue callback", barge)
	return digit, hit, alive, true
}

// queueSpeakKey speaks the prompt in the queue's language, in English when
// the voice cannot produce that language's clip.
func (c *call) queueSpeakKey(ctx context.Context, q snapshot.Queue, k voiceprompt.Key, barge bool) (digit byte, hit, alive bool) {
	lang := c.s.queueLang(q)
	d, h, a, ok := c.queueSpeak(ctx, voiceprompt.Text(lang, k, nil), barge)
	if !ok && lang != voiceprompt.Default && a {
		d, h, a, _ = c.queueSpeak(ctx, voiceprompt.Text(voiceprompt.Default, k, nil), barge)
	}
	return d, h, a
}

// --- the offer and the request ------------------------------------------------

// queueKey is what a key pressed in the queue asks for.
type queueKey int

const (
	keyNone queueKey = iota
	keyBreakout
	keyCallback
)

// callbackOffered reports whether the queue may offer this call a callback:
// the queue has it on, the store is there, the call is not itself a callback,
// and the caller has a number to dial.
func (c *call) callbackOffered(q snapshot.Queue) bool {
	c.mu.Lock()
	cb := c.cbID
	c.mu.Unlock()
	if cb != 0 || !q.CallbackEnabled || q.CallbackDigit == "" || c.s.deps.Callbacks == nil {
		return false
	}
	_, ok := callbackNumber(c.callerNum)
	return ok
}

// keyFor classifies a key pressed in the queue.
func (c *call) keyFor(snap *snapshot.Snapshot, q snapshot.Queue, d byte) queueKey {
	switch {
	case c.callbackOffered(q) && q.CallbackDigit[0] == d:
		return keyCallback
	case c.queueBreakout(snap, q, d):
		return keyBreakout
	}
	return keyNone
}

// callbackDue reports whether the caller's estimated wait exceeds the
// queue's threshold. With nobody staffed no wait can be estimated, so the
// time already waited stands in for it. A threshold of 0 offers at once.
func (c *call) callbackDue(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue, since time.Time) bool {
	thr := time.Duration(q.CallbackOfferAfter) * time.Second
	if thr == 0 {
		return true
	}
	st := c.s.queueStore()
	lctx, cancel := c.s.stateCtx()
	waiting, _ := st.Waiting(lctx, q.Name)
	avg, _ := st.AvgHandle(lctx, q.Name)
	cancel()
	staffed, _ := c.queueAgents(ctx, snap, q)
	if staffed == 0 {
		return time.Since(since) >= thr
	}
	return queue.EstimatedWait(queue.Position(waiting, c.id), avg, staffed) > thr
}

// queueCallbackOffer speaks the offer once. A press of the callback key
// accepts; a press of the breakout key is reported so the caller leaves.
func (c *call) queueCallbackOffer(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue) queueKey {
	lang := c.s.queueLang(q)
	text := voiceprompt.CallbackOffer(lang, q.CallbackDigit)
	d, hit, alive, ok := c.queueSpeak(ctx, text, true)
	if !ok && lang != voiceprompt.Default && alive {
		d, hit, alive, ok = c.queueSpeak(ctx, voiceprompt.CallbackOffer(voiceprompt.Default, q.CallbackDigit), true)
	}
	c.addTrace(fmt.Sprintf("Queue %s: callback offered (spoken: %t)", q.Name, ok))
	if hit && alive {
		return c.keyFor(snap, q, d)
	}
	return keyNone
}

// queueCallbackAccept records the caller's request and confirms it. It
// reports false when the request could not be taken (the queue holds its
// maximum, or PostgreSQL is unavailable): the caller hears so and keeps its
// place in line.
func (c *call) queueCallbackAccept(ctx context.Context, q snapshot.Queue, entry queue.Entry) bool {
	s := c.s
	num, ok := callbackNumber(c.callerNum)
	if !ok || s.deps.Callbacks == nil {
		return false
	}
	dctx, cancel := context.WithTimeout(ctx, callbackDBWait)
	cb, created, err := s.deps.Callbacks.Request(dctx, q.ID, num, c.callerName, entry.EnteredMs)
	cancel()
	switch {
	case errors.Is(err, queue.ErrCallbackFull):
		s.m.CallbackTotal.WithLabelValues("full").Inc()
		c.addTrace(fmt.Sprintf("Queue %s: callback refused, %d requests wait", q.Name, queue.CallbackMaxWaiting))
		c.queueSpeakKey(ctx, q, voiceprompt.QueueCallbackFull, false)
		return false
	case err != nil:
		if ctx.Err() == nil {
			s.log.Warn("queue callback not recorded", "queue", q.Name, "correlation_id", c.id, "error", err)
		}
		s.m.CallbackTotal.WithLabelValues("failed").Inc()
		c.queueSpeakKey(ctx, q, voiceprompt.QueueCallbackFull, false)
		return false
	}
	result := "accepted"
	if created {
		s.m.CallbackTotal.WithLabelValues("requested").Inc()
	} else {
		result = "coalesced"
		s.m.CallbackTotal.WithLabelValues("coalesced").Inc()
	}
	c.qLeg(cdr.QueueCallbackRequested, q.Name, "", time.Since(entry.Entered()), result)
	c.queueLog("queue callback requested", q.Name, "request", cb.ID, "result", result)
	c.addTrace(fmt.Sprintf("Queue %s: callback %s as request %d, keeping the place from %s",
		q.Name, result, cb.ID, entry.Entered().Format("15:04:05")))
	c.queueSpeakKey(ctx, q, voiceprompt.QueueCallbackConfirm, false)
	return true
}

// --- the call's own outcome ----------------------------------------------------

// cbFinish records the outcome of the request this call serves, once: the
// row (connected, or an attempt failed and either retried or ended), the CDR
// leg and the counter. result is connected, no_answer, busy, failed,
// abandoned or no_agent; agent names the connected agent.
func (c *call) cbFinish(result, agent string) {
	c.mu.Lock()
	id, qn := c.cbID, c.cbQueue
	if id == 0 || c.cbDone {
		c.mu.Unlock()
		return
	}
	c.cbDone = true
	c.mu.Unlock()
	s := c.s
	ctx, cancel := context.WithTimeout(context.Background(), callbackDBWait)
	defer cancel()
	var (
		final bool
		err   error
	)
	switch result {
	case "connected":
		err = s.deps.Callbacks.Connect(ctx, id, s.cfg.NodeID, agent)
		c.qLeg(cdr.QueueCallbackConnected, qn, agent, 0, "")
	case "cancelled":
		// An operator ended the request: the row is already cancelled.
		c.qLeg(cdr.QueueCallbackFailed, qn, "", 0, result)
	default:
		final, err = s.deps.Callbacks.Fail(ctx, id, s.cfg.NodeID, result)
		c.qLeg(cdr.QueueCallbackFailed, qn, "", 0, result)
	}
	s.m.CallbackTotal.WithLabelValues(result).Inc()
	switch {
	case err != nil && !errors.Is(err, queue.ErrCallbackNotFound):
		s.log.Warn("queue callback outcome not saved", "queue", qn, "request", id, "result", result, "error", err)
	case final:
		c.addTrace(fmt.Sprintf("Callback %d failed for good after %d attempts (%s)", id, queue.CallbackMaxAttempts, result))
	case result != "connected":
		c.addTrace(fmt.Sprintf("Callback %d attempt failed (%s); the next is in %s", id, result, queue.CallbackRetryAfter))
	}
	c.queueLog("queue callback "+result, qn, "request", id, "final", final)
}

// cbRenew renews the request's lease from this node; a takeover adopts the
// request the same way.
//
// It reports false when the request is no longer dialling: an operator
// cancelled it (or it expired), and the call must end. A store that cannot be
// reached reports true: the call carries on and tries again.
func (c *call) cbRenew() bool {
	c.mu.Lock()
	id, done := c.cbID, c.cbDone
	c.mu.Unlock()
	if id == 0 || done {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), callbackDBWait)
	defer cancel()
	err := c.s.deps.Callbacks.Renew(ctx, id, c.s.cfg.NodeID)
	switch {
	case errors.Is(err, queue.ErrCallbackNotFound):
		return false
	case err != nil:
		c.s.log.Warn("queue callback lease not renewed", "request", id, "error", err)
	}
	return true
}

// callbackReserve is how long the sweep holds an agent for a ringing caller:
// the ring time and a margin.
func callbackReserve() time.Duration { return callbackRingTimeout + 30*time.Second }

// --- the sweep ----------------------------------------------------------------

// callbackLoop sweeps the queues' callback requests until ctx ends. Every
// node runs it; the database decides who claims what.
func (s *Server) callbackLoop(ctx context.Context) {
	defer contain(s.log, "callback loop")
	if s.deps.Callbacks == nil || s.queueStore() == nil {
		return
	}
	every := callbackEvery
	if s.cfg.CallbackInterval > 0 {
		every = s.cfg.CallbackInterval
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	lastPurge := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-tick.C:
		}
		s.callbackSweepAll(ctx)
		if time.Since(lastPurge) >= callbackPurgeEvery {
			lastPurge = time.Now()
			dctx, cancel := context.WithTimeout(ctx, callbackDBWait)
			if err := s.deps.Callbacks.Purge(dctx); err != nil {
				s.log.Warn("queue callbacks not purged", "error", err)
			}
			cancel()
		}
	}
}

// callbackSweepAll refreshes the hold map and sweeps each queue that has a
// request due.
func (s *Server) callbackSweepAll(ctx context.Context) {
	dctx, cancel := context.WithTimeout(ctx, callbackDBWait)
	due, err := s.deps.Callbacks.Due(dctx)
	cancel()
	if err != nil {
		// PostgreSQL is unavailable: callbacks wait, and no call is held
		// back for one.
		s.cbHold.Store(map[int64]int64(nil))
		return
	}
	s.cbHold.Store(due)
	snap := s.deps.Snapshots.Current()
	if snap == nil || !s.serving.Load() {
		return
	}
	for _, q := range snap.Queues() {
		if _, ok := due[q.ID]; ok && q.Enabled {
			s.callbackSweep(ctx, snap, q)
		}
	}
}

// callbackHeld reports whether a waiting call that entered at enteredMs must
// let a callback go first: one is due, or being served, with an earlier
// place.
func (s *Server) callbackHeld(queueID, enteredMs int64) bool {
	m, _ := s.cbHold.Load().(map[int64]int64)
	key, ok := m[queueID]
	return ok && key < enteredMs
}

// callbackSweep claims the queue's head request when a staffed agent would
// take it under the queue's strategy, and starts the attempt. The queue's
// callback lease serialises the check and the claim across the nodes.
func (s *Server) callbackSweep(ctx context.Context, snap *snapshot.Snapshot, q snapshot.Queue) {
	st, cbs := s.queueStore(), s.deps.Callbacks
	lease := "callback:" + q.Name
	lctx, cancel := s.stateCtx()
	got, err := st.Acquire(lctx, lease, s.cfg.NodeID, 5*time.Second)
	cancel()
	if err != nil || !got {
		return
	}
	defer func() {
		lctx, cancel := s.stateCtx()
		_ = st.Release(lctx, lease, s.cfg.NodeID)
		cancel()
	}()
	dctx, cancel := context.WithTimeout(ctx, callbackDBWait)
	defer cancel()
	head, ok, err := cbs.Head(dctx, q.ID)
	if err != nil || !ok {
		return
	}
	lctx, cancel2 := s.stateCtx()
	waiting, err := st.Waiting(lctx, q.Name)
	cancel2()
	if err != nil || queue.AheadOfCallback(waiting, head.PositionKey) {
		return // callers who waited longer are served first
	}
	scratch := &call{s: s}
	_, free := scratch.queueAgents(ctx, snap, q)
	dialling, err := cbs.Dialling(dctx, q.ID)
	if err != nil || len(free) == 0 || len(free) <= dialling {
		return // no agent would take it now
	}
	// Reserve the agent the strategy would ring first (the first of the plan
	// that is not being rung for another caller) while the caller's phone
	// rings, so a caller who waits less cannot take it meanwhile.
	token := s.cfg.NodeID + "/callback"
	var agent string
	for _, round := range queue.Plan(q.Strategy, free, 0, queue.Seed(q.Name, strconv.FormatInt(head.ID, 10))) {
		for _, ext := range round {
			lctx, cancel := s.stateCtx()
			got, err := st.Acquire(lctx, queue.AgentLease(ext), token, callbackReserve())
			cancel()
			if err == nil && got {
				agent = ext
				break
			}
		}
		if agent != "" {
			break
		}
	}
	if agent == "" {
		return // every free agent is being rung for another caller
	}
	release := func() {
		lctx, cancel := s.stateCtx()
		_ = st.Release(lctx, queue.AgentLease(agent), token)
		cancel()
	}
	cb, ok, err := cbs.Claim(dctx, q.ID, s.cfg.NodeID)
	if err != nil || !ok {
		release()
		return
	}
	s.bg.Go(func() {
		defer contain(s.log, "queue callback")
		s.callbackAttempt(snap, q, cb, agent, release)
	})
}

// --- one attempt ----------------------------------------------------------------

// newCallbackCall builds the call of one attempt: a call with no inbound
// INVITE, whose caller is the person asking to be called back.
func (s *Server) newCallbackCall(q snapshot.Queue, cb queue.Callback) *call {
	c := &call{
		s: s, id: newID(), callerNum: cb.Number, callerName: cb.CallerName, dialled: orDefault(q.Number, q.Name),
		start: time.Now(), direction: cdr.DirectionInternal, kind: cdr.KindCallback, mediaMode: "anchored",
		canceled: make(chan struct{}), stopHB: make(chan struct{}), setupDone: make(chan struct{}),
		aborted: make(chan struct{}), haStop: make(chan struct{}), pickupCh: make(chan *pickupReq, 1),
		events: make(chan legEvent, 16),
	}
	c.callerDialogUp = true // no caller INVITE to answer: respondA stays quiet
	c.cbID, c.cbQueue = cb.ID, q.Name
	return c
}

// callbackPlan is how the caller's number is reached: an extension's phones
// (all rung together), or the outbound route's trunk candidates, tried in
// route order like any outbound call.
type callbackPlan struct {
	phones   func() []*leg // an extension's phones, nil for a trunk plan
	dec      routing.Decision
	host     string // where the caller's media is expected, for the offer
	why      string // set when nothing can be rung
	external bool
}

// callbackPlan decides how to reach number: an extension's own phones, or
// outbound routing as a call from agent (the one the queue would ring first).
func (c *call) callbackPlan(snap *snapshot.Snapshot, q snapshot.Queue, number, agent string) callbackPlan {
	s := c.s
	ident := &legIdent{fromNum: orDefault(q.Number, q.Name), fromName: q.Name, toNum: number}
	var host string
	phonesOf := func(ext string) func() []*leg {
		var bindings []livestate.Binding
		for _, d := range snap.DevicesForExtension(ext) {
			ctx, cancel := s.stateCtx()
			bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
			cancel()
			if err == nil {
				bindings = append(bindings, bs...)
			}
		}
		if len(bindings) == 0 {
			return nil
		}
		var u sip.Uri
		if sip.ParseUri(trimAngle(bindings[0].ContactURI), &u) == nil {
			host = u.Host
		}
		return func() []*leg {
			out := make([]*leg, 0, len(bindings))
			for _, b := range bindings {
				out = append(out, c.addLeg(&leg{binding: b, ident: ident}))
			}
			return out
		}
	}
	if snap.HasExtension(number) {
		mk := phonesOf(number)
		if mk == nil {
			return callbackPlan{why: "extension " + number + " has no phone registered"}
		}
		return callbackPlan{phones: mk, host: host}
	}
	dec := s.decide(snap.Routing(), routing.Call{FromExtension: agent, Number: number, CallerID: orDefault(q.Number, q.Name), At: time.Now()})
	c.mu.Lock()
	c.trace = append(c.trace, dec.Trace...)
	c.route, c.rewritten = dec.Route, dec.Number
	c.mu.Unlock()
	switch {
	case dec.Kind == routing.KindOutbound:
		c.mu.Lock()
		c.direction = cdr.DirectionOutbound
		c.mu.Unlock()
		for _, cand := range dec.Candidates {
			if cand.Trunk != nil && len(cand.Destinations) > 0 {
				return callbackPlan{dec: dec, host: cand.Destinations[0].Host, external: true}
			}
		}
		return callbackPlan{why: "no trunk could take the call"}
	case dec.Kind == routing.KindInternal && dec.Extension != "":
		if mk := phonesOf(dec.Extension); mk != nil {
			return callbackPlan{phones: mk, host: host}
		}
		return callbackPlan{why: "extension " + dec.Extension + " has no phone registered"}
	}
	return callbackPlan{why: "routing: " + orDefault(dec.Reason, "no route for "+number)}
}

// callbackDial rings the caller. Phones ring together. A trunk plan follows
// setupOutbound: each candidate in route order, taking a call slot (a full
// trunk is skipped), each destination not known down in turn; a failover code
// or a silent attempt moves on to the next, any other final answer ends the
// attempt with that response.
func (c *call) callbackDial(plan callbackPlan) (w *leg, status int, result, reason string) {
	s := c.s
	if plan.phones != nil {
		legs := plan.phones()
		for _, l := range legs {
			go l.run()
		}
		w, status, result, reason, _ = c.callbackAwait(len(legs), 0, nil, 0)
		return w, status, result, reason
	}
	dec := plan.dec
	lastStatus, lastResult, lastReason := sip.StatusServiceUnavailable, "failed", "no trunk connected the call"
	for _, cand := range dec.Candidates {
		t := cand.Trunk
		if t == nil {
			continue
		}
		if ok, why := c.acquireSlot(t, dec.Emergency, c.id); !ok {
			c.addTrace(fmt.Sprintf("Trunk %s skipped: %s", t.Name, why))
			continue
		}
		c.mu.Lock()
		c.trunkName = t.Name
		c.mu.Unlock()
		for _, d := range cand.Destinations {
			if s.trunks.destinationDown(t.ID, d) && !dec.Emergency {
				c.addTrace(fmt.Sprintf("Trunk %s destination %s:%d skipped: down", t.Name, d.Host, d.Port))
				continue
			}
			addr := s.trunks.destAddr(d)
			c.mu.Lock()
			c.stageSeq++
			stage := c.stageSeq
			c.mu.Unlock()
			l := c.addLeg(&leg{trunk: t, dest: d, addr: addr, number: dec.Number, callerID: dec.CallerID, stage: stage})
			go l.run()
			w, status, result, reason, code := c.callbackAwait(1, stage, l, s.cfg.TrunkAttemptTimeout)
			if w != nil {
				c.addTrace(t.Name + " -> 200 OK")
				return w, 0, "", ""
			}
			switch result {
			case "cancelled", "draining":
				return nil, status, result, reason
			case "silent":
				c.addTrace(fmt.Sprintf("%s (%s) -> no answer", t.Name, addr))
				c.addTrace("Failover permitted for no answer")
				lastStatus, lastResult, lastReason = sip.StatusRequestTimeout, "no_answer", "no answer from the trunk"
				continue
			case "no_answer":
				return nil, status, result, reason // it rang and nobody answered: no failover
			}
			c.addTrace(fmt.Sprintf("%s (%s) -> %d %s", t.Name, addr, code, statusText(code)))
			if !slices.Contains(failoverCodes(dec), code) {
				c.addTrace(fmt.Sprintf("No failover for %d", code))
				return nil, status, result, reason
			}
			c.addTrace(fmt.Sprintf("Failover permitted for %d", code))
			lastStatus, lastResult, lastReason = status, result, reason
		}
		c.releaseSlot(t.ID, c.id) // this trunk did not take the call
	}
	return nil, lastStatus, lastResult, lastReason
}

// callbackFail ends an attempt that never reached the caller.
func (c *call) callbackFail(result string, status int, reason string) {
	c.cbFinish(result, "")
	c.addTrace("Callback attempt failed: " + reason)
	res := ResultFailed
	switch result {
	case "busy":
		res = ResultBusy
	case "no_answer":
		res = ResultNoAnswer
	case "cancelled":
		res = ResultCancelled
	}
	c.end(status, cdr.SideCallee, reason, res)
}

// callbackOffer is the offer the caller is rung with: G.711 and DTMF events
// on the relay's caller leg.
func callbackOffer(host string, port int) ([]byte, media.AudioSDP) {
	var codecs []media.Codec
	for _, pt := range []uint8{0, 8} {
		if cd, ok := media.StaticCodec(pt); ok {
			codecs = append(codecs, cd)
		}
	}
	events := []media.Codec{{PT: 101, Name: "telephone-event", ClockRate: 8000, Fmtp: "0-16"}}
	body := media.BuildSDP(host, port, codecs, events, "sendrecv")
	off, _ := media.ParseAudioSDP(body)
	return body, off
}

// callbackAttempt rings the caller of one claimed request and, when the
// caller answers, queues the call at the caller's original place. release
// frees the agent the sweep reserved for the attempt.
func (s *Server) callbackAttempt(snap *snapshot.Snapshot, q snapshot.Queue, cb queue.Callback, agent string, release func()) {
	released := false
	free := func() {
		if !released {
			released = true
			release()
		}
	}
	defer free()
	c := s.newCallbackCall(q, cb)
	s.m.ActiveCalls.Inc()
	c.addTrace(fmt.Sprintf("Callback request %d for queue %s, attempt %d: ringing %s", cb.ID, q.Name, cb.Attempts, cb.Number))
	c.qLeg(cdr.QueueCallbackDialled, q.Name, "", 0, "")
	s.m.CallbackTotal.WithLabelValues("dialled").Inc()
	c.queueLog("queue callback dialled", q.Name, "request", cb.ID, "attempt", cb.Attempts)

	plan := c.callbackPlan(snap, q, cb.Number, agent)
	if plan.why != "" {
		c.callbackFail("failed", sip.StatusNotFound, plan.why)
		return
	}
	relay, err := media.NewRelay(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		c.callbackFail("failed", sip.StatusServiceUnavailable, "media ports: "+err.Error())
		return
	}
	for _, leg := range []string{legCaller, legCallee} {
		if _, err := relay.AddLeg(leg); err != nil {
			relay.Close()
			c.callbackFail("failed", sip.StatusServiceUnavailable, "media leg: "+err.Error())
			return
		}
	}
	host := s.anchorHostOr(plan.host)
	body, off := callbackOffer(host, relay.LegPort(legCaller))
	relay.SetCodecs(legCaller, off.Codecs, off.DTMFPayloadType)
	relay.SetCodecs(legCallee, off.Codecs, off.DTMFPayloadType)
	if m := s.deps.Media; m != nil {
		m.NoteStart()
	}
	c.mu.Lock()
	c.relay, c.anchorHost, c.cbOffer = relay, host, body
	c.callID = newID() // the dialog's own Call-ID is the first leg's, set once it is dialled
	c.mu.Unlock()
	c.publish()
	go c.heartbeat()

	w, status, result, reason := c.callbackDial(plan)
	free() // the agent is the queue's to offer from here on
	if w == nil {
		c.callbackFail(result, status, reason)
		return
	}
	if !c.callbackHome(snap, q, cb, w, off) {
		w.bye()
		c.callbackFail("failed", sip.StatusServiceUnavailable, "the answer could not be taken in")
		return
	}
}

// callbackAwait waits for the first leg to answer, every leg to fail, or the
// ring time. silent, when set, is how long an attempt may go without a
// ringing response before it counts as silent (result "silent", for trunk
// failover). On failure it classifies the outcome for the row and the CDR;
// code is the last final response. While it waits, it watches the request:
// an operator who cancelled it stops the ringing.
func (c *call) callbackAwait(pending, stage int, only *leg, silent time.Duration) (w *leg, status int, result, reason string, code int) {
	s := c.s
	timer := time.NewTimer(callbackRingTimeout)
	defer timer.Stop()
	var silentC <-chan time.Time
	if silent > 0 {
		st := time.NewTimer(silent)
		defer st.Stop()
		silentC = st.C
	}
	watch := time.NewTicker(callbackWatchEvery)
	defer watch.Stop()
	total, busy := pending, 0
	for {
		select {
		case ev := <-c.events:
			if ev.leg != nil && ev.leg.stage != stage {
				continue // a leg an earlier attempt gave up on
			}
			switch ev.kind {
			case evRinging:
				silentC = nil
				c.mu.Lock()
				if c.ringTime.IsZero() {
					c.ringTime = time.Now()
				}
				c.mu.Unlock()
			case evAnswered:
				return ev.leg, 0, "", "", 0
			case evFailed:
				pending--
				code = ev.code
				if isBusy(ev.code) {
					busy++
				}
				if pending == 0 {
					if busy == total {
						return nil, sip.StatusBusyHere, "busy", "caller busy", code
					}
					return nil, ev.code, "failed", "the caller's phone could not be reached", code
				}
			}
		case <-silentC:
			silentC = nil
			if only != nil && c.abandon(only) {
				return nil, sip.StatusRequestTimeout, "silent", "no response", 0
			}
		case <-timer.C:
			if c.closeSetup() {
				c.cancelForks(nil)
				return nil, sip.StatusRequestTimeout, "no_answer", "ring timeout", 0
			}
			// a leg answered just now: its event is on the way
		case <-watch.C:
			if !c.cbRenew() && c.closeSetup() {
				c.cancelForks(nil)
				return nil, sip.StatusRequestTerminated, "cancelled", "cancelled by an operator", 0
			}
		case <-c.aborted:
			c.closeSetup()
			c.cancelForks(nil)
			return nil, sip.StatusServiceUnavailable, "draining", "the node is draining", 0
		case <-s.done:
			c.closeSetup()
			c.cancelForks(nil)
			return nil, sip.StatusServiceUnavailable, "draining", "the node is shutting down", 0
		}
	}
}

// callbackHome turns the answered leg into the call's one dialog (the
// caller's), aims the relay at it and starts the queued session at the
// request's original place. It reports false when the dialog cannot be
// described.
func (c *call) callbackHome(snap *snapshot.Snapshot, q snapshot.Queue, cb queue.Callback, w *leg, off media.AudioSDP) bool {
	s := c.s
	c.cancelForks(w)
	c.mu.Lock()
	c.callID = w.callID
	c.mu.Unlock()
	dcs := w.session()
	relay := c.anchorRelay()
	if dcs == nil || dcs.InviteRequest == nil || dcs.InviteResponse == nil || relay == nil {
		return false
	}
	req, res := dcs.InviteRequest, dcs.InviteResponse
	ans, err := media.ParseAudioSDP(res.Body())
	if err != nil {
		return false
	}
	w.ack(nil, "")
	neg := media.AnswerFor(off, ans)
	relay.SetCodecs(legCaller, neg.Codecs, neg.DTMFPayloadType)
	relay.SetCodecs(legCallee, neg.Codecs, neg.DTMFPayloadType)
	relay.SetTarget(legCaller, udpAddr(ans.Address, ans.Port))
	allowSignalSource(relay, legCaller, res.Source())
	c.mu.Lock()
	host := c.anchorHost
	c.mu.Unlock()
	a := haLegFrom(livestate.DialogLeg{
		CallID: w.callID, LocalTag: tagParam(req.From()), RemoteTag: tagParam(res.To()),
		LocalCSeq: s.nextCSeq(w.callID, req.CSeq().SeqNo, dcs.CSEQ()), RemoteCSeq: req.CSeq().SeqNo,
		// A UAC's route set is the 2xx's Record-Route reversed (RFC 3261 12.1.2).
		RouteSet:      reversed(headerValues(res, "Record-Route")),
		LocalIdentity: uriString(*req.From().Address.Clone()), RemoteIdentity: uriString(*req.To().Address.Clone()),
		RemoteTarget: uriString(w.target()), Endpoint: w.binding.AOR, Source: res.Source(),
		SDP: relaySDPDir(host, relay, legCaller, neg, "sendrecv"), RemoteSDP: string(res.Body()),
	}, false)
	if a == nil {
		return false
	}
	now := time.Now()
	c.mu.Lock()
	c.winner, c.setupClosed = nil, false
	c.legs = filterLegs(c.legs, w)
	c.homedCall = &homedCall{takenAt: now, legs: [2]*haLeg{a, nil}}
	c.cbOffer = nil
	c.anchored, c.relay, c.negotiated = true, relay, neg
	c.rec = &recording{codecs: neg.Codecs}
	c.connected, c.answerTime, c.ivrAnswered = true, now, now
	c.haSolo, c.haPhase, c.haDetail = true, haPhaseQueue, queueDetail(q.Name, cb.PositionKey, cb.ID)
	c.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, c.expire)
	c.mu.Unlock()
	s.bind(w.callID, dialogRef{c: c}) // the dialog is the caller's now
	c.observeRelay(relay)
	relay.Start()
	c.logTerminatedCodec("callback", "queue", []byte(a.sdp))
	c.addTrace(fmt.Sprintf("Callback: %s answered; queued in %s at its original place", cb.Number, q.Name))
	c.publish()
	if s.deps.HAState != nil {
		go c.haLoop() // replicated like any queued caller: a survivor re-homes it
	}
	c.queueResume(queueDetail(q.Name, cb.PositionKey, cb.ID))
	return true
}

// filterLegs removes w from legs.
func filterLegs(legs []*leg, w *leg) []*leg {
	out := make([]*leg, 0, len(legs))
	for _, l := range legs {
		if l != w {
			out = append(out, l)
		}
	}
	return out
}

// isCallback reports whether the call serves a callback request.
func (c *call) isCallback() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cbID != 0
}
