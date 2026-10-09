package sip

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// byeTimeout bounds a BYE or in-dialog transaction (Timer F is 32s).
const byeTimeout = 35 * time.Second

// call is one B2BUA call: the caller's leg (A, a UAS dialog) and one fork
// per binding of the dialled extension (B legs, UAC dialogs). It stays on
// the node that set it up.
type call struct {
	s      *Server
	id     string // correlation ID
	callID string // caller's Call-ID
	// The caller as presented: an extension (with its device) or a trunk's
	// caller ID; callerDevice is never rung for this call.
	callerNum    string
	callerName   string
	callerDevice string
	dialled      string
	start        time.Time
	inv          *sip.Request
	dss          *sipgo.DialogServerSession

	events    chan legEvent
	setupDone chan struct{} // closed when setup stops reading events
	canceled  chan struct{}
	// aborted is closed when the node ends a call still being set up
	// (drain timeout); the setup goroutine answers the caller. Taking its
	// ownership also closes setup under c.mu, so a fork failure, the ring
	// timer or fork exhaustion cannot win closeSetup afterwards and record
	// a normal callee or timeout result instead of the abort's.
	aborted     chan struct{}
	abortOnce   sync.Once
	abortReason string
	abortOwned  bool        // guarded by mu: the abort owns the setup's end
	maxTimer    *time.Timer // ends a connected call at MaxCallDuration
	cancelMu    sync.Once
	stopHB      chan struct{}
	// pubMu orders live-call writes: publish and the final delete never
	// overlap, and nothing is published once the call has ended, so an
	// in-flight heartbeat cannot resurrect an ended call.
	pubMu   sync.Mutex
	endOnce sync.Once

	mu          sync.Mutex
	legs        []*leg
	winner      *leg
	setupClosed bool // no fork may win any more
	isCancelled bool
	connected   bool
	ended       bool
	hungUp      bool
	stageSeq    int // the current forwarding stage; legs carry the stage they belong to
	// invTx is the caller INVITE's server transaction, kept so a
	// callee-less answer (voicemail) can respond without sipgo's dialog
	// ACK-wait; vmTag is the To tag that answer carries.
	invTx sip.ServerTransaction
	vmTag string
	// inDialog counts re-INVITE/UPDATE relays in progress. Add happens
	// under mu while the call is not being ended, so never after Wait.
	inDialog   sync.WaitGroup
	lateAck    bool // the winner's ACK waits for the caller's (late offer)
	ringTime   time.Time
	answerTime time.Time
	// Routing detail for the CDR; trace is the decision's trace extended
	// with this node's attempts.
	direction string
	route     string
	rewritten string
	trunkName string
	trace     routing.Trace
	slots     []heldSlot // trunk call slots held, released when the attempt ends

	// Phase 4 call-flow state. stages carries the forwarding/DND context of
	// the extension being rung (nil for a plain call); mediaMode is the CDR's
	// media column ("direct", or "anchored" for voicemail); vmSession is the
	// voicemail anchor; held is the hold state behind hello_hold_active.
	// groupName/Strategy/Fn carry the group a call dialled and the failure
	// destination that applies when the group gives up.
	stages    *callStages
	mediaMode string
	// quality is the anchored relay's latest stats snapshot (spec S-5.1),
	// guarded by qmu (not mu: the relay delivers it from Close).
	qmu       sync.Mutex
	quality   *media.RelayStats
	vmSession media.Session
	held      bool

	// Phase 5 anchoring state: the reason the media anchors ("" direct),
	// the session's relay, and the recording state. Guarded by mu.
	anchored      bool
	anchorReason  AnchorReason
	policyTrigger AnchorReason // what the pre-Phase-7 decision would have been
	// haDirs is each dialog's current SDP direction ("sendrecv" both, or a
	// hold's sendonly/recvonly pair), replicated so a taker's re-INVITEs
	// keep the hold. haLegs is the dialog data snapshot taken at the same
	// quiescent points — the replication heartbeat never reads sipgo's
	// dialog objects, which are not thread-safe. Both guarded by mu.
	haDirs [2]string
	haLegs [2]haLegSnap
	relay  *media.Relay
	rec    *recording
	// anchorHost is the advertised IPv4 for relay SDP answers.
	anchorHost string

	groupName     string
	groupStrategy string
	groupFn       func()
	transferKind  string // blind or attended, for hello_transfers_total

	// In-call HA state (Phase 7). haPhase/haDetail are the replicated call
	// phase beyond hold; haYielded stops replication and record deletion
	// once another node took the call; haStop closes the replication
	// heartbeat; homedCall carries a taken-over call's rebuilt dialogs.
	// All guarded by mu.
	haPhase   string
	haDetail  string
	haYielded bool
	haStop    chan struct{}
	homedCall *homedCall
	// haSolo marks a call Hello answered itself (voicemail, an
	// announcement destination): one replicated leg, the caller's.
	haSolo bool
	// haHandoff marks a call this draining node is handing to a survivor:
	// its record says so once (haHandoffWritten) and is never written
	// again, so the taker's record is not overwritten.
	haHandoff, haHandoffWritten bool
	// haClaim is the Call-ID whose takeover claim this taker still holds:
	// it is released only once a replication write names this node, so no
	// survivor finds the dialog unclaimed and still owned by the dead node
	// (and takes it a second time, counting a zombie when that fails).
	haClaim string
	// callerDialogUp marks a call that reuses an already confirmed caller
	// dialog (a transferred call): it sends no responses on it.
	callerDialogUp bool
	// haShared marks a transferred call whose Call-ID keys the original
	// call's replicated record: until it replicates itself (haStart) the
	// record is the original's, and its end must not delete it.
	haShared bool
	// haWriteMu orders replication writes against the record's deletion:
	// a heartbeat write in flight when the call ends must not land after
	// haDelete and resurrect the record of an ended call (a survivor would
	// take that ghost over: re-INVITEs on dead dialogs, a zombie counted).
	// haDeleted (guarded by haWriteMu) stops every later write.
	haWriteMu sync.Mutex
	haDeleted bool
	// maxFrom starts the maximum-duration clock when it is not the
	// answer on this node: a taken-over call's original answer.
	maxFrom time.Time
	// transferNotify reports a transfer's outcome to the transferee's
	// dialog (set by transferBlindVia for the rethreaded call).
	transferNotify func(fragment string, final bool)
}

// callStages is the forwarding/DND context of the extension a call is
// currently ringing, carried across the exhaustion hand-offs (forward on
// busy or no answer, voicemail). The setup goroutine owns it.
type callStages struct {
	visited []string           // extensions already rung; a revisit is a loop
	ext     string             // the extension this stage rings
	feats   snapshot.Extension // its features
	source  string             // the forwarding kind that led here, "" at first
}

type legEventKind int

const (
	evRinging legEventKind = iota
	evAnswered
	evFailed
	evChallenged // a trunk challenged the INVITE (code 401 or 407)
)

type legEvent struct {
	leg    *leg
	kind   legEventKind
	code   int
	reason string
}

// leg is one fork towards a callee binding.
type leg struct {
	c       *call
	binding livestate.Binding
	callID  string
	ctx     context.Context
	cancel  context.CancelFunc

	mu    sync.Mutex
	dcs   *sipgo.DialogClientSession
	acked bool

	// A trunk attempt (trunk set) or a SIP URI target (uri set); neither
	// means a phone binding.
	trunk     *routing.Trunk
	dest      routing.Destination
	addr      string
	number    string
	callerID  string
	uri       *sip.Uri
	abandoned bool // guarded by c.mu: failed over, may no longer win
	stage     int  // the forwarding stage this fork belongs to; setup ignores older stages' events
}

func (l *leg) session() *sipgo.DialogClientSession {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dcs
}

func isInDialog(req *sip.Request) bool {
	_, ok := req.To().Params.Get("tag")
	return ok
}

// handleInvite sets up a call, or relays a re-INVITE within an existing one.
// It stays blocked until the caller has a final response (sipgo terminates
// the server transaction when the handler returns).
//
// An INVITE without credentials from a trunk's source is an inbound trunk
// call; one that does not even claim to be a phone of this domain is
// refused (403, counted by the failed-auth throttle). Everything else goes
// through digest authentication as a device.
func (s *Server) handleInvite(req *sip.Request, tx sip.ServerTransaction) {
	if isInDialog(req) {
		s.handleInDialog(req, tx)
		return
	}
	if s.refuseIfNotReady(req, tx) {
		return
	}
	if _, ok := s.lookup(req.CallID().Value()); ok {
		// Same Call-ID as a call in progress but a new transaction: a
		// merged or looped request (RFC 3261 §8.2.2.2).
		s.respond(tx, req, sip.StatusLoopDetected, "Loop Detected")
		return
	}
	snap := s.deps.Snapshots.Current() // one snapshot for the whole request
	if snap == nil {
		s.unavailable(tx, req)
		return
	}
	if req.GetHeader("Authorization") == nil {
		if t, ok := s.trunkSource(req, snap); ok {
			s.inboundCall(req, tx, snap, t)
			return
		}
		if !s.looksLikePhone(req, snap) {
			s.authFailed(tx, req, s.clientIP(req), "INVITE from a source that is neither a trunk nor a phone", false)
			return
		}
	}
	dev, ok := s.verify(req, tx, snap)
	if !ok {
		return
	}
	c := s.newCall(req)
	c.callerNum, c.callerName, c.callerDevice = dev.Extension, dev.ExtensionName, dev.Username
	// A feature code dialled as a call (*78, *72599, *97) is dispatched here
	// and never reaches routing.
	if fc, arg, ok := matchFeatureCode(snap, c.dialled); ok {
		c.handleFeatureInvite(tx, fc, arg)
		return
	}
	// A ring group number resolves as a group instead of through routing.
	if _, _, ok := snap.RingGroup(c.dialled); ok {
		c.ringTarget(req, tx, snap, c.dialled)
		return
	}
	rs := snap.Routing()
	var dec routing.Decision
	if len(rs.Errors) > 0 && snap.HasExtension(c.dialled) {
		// Routing is frozen on the last good table, whose extension list
		// is that older revision's. An extension of the current revision
		// must still be found internally, never routed out to a carrier.
		dec.Trace.Add(fmt.Sprintf("Internal extension lookup %q -> extension %s", c.dialled, c.dialled))
		dec.Kind, dec.Extension, dec.CallerID = routing.KindInternal, c.dialled, dev.Extension
	} else {
		dec = s.decide(rs, routing.Call{
			FromExtension: dev.Extension, Number: c.dialled, CallerID: dev.Extension,
			SIPDomain: req.Recipient.Host, Header: headerOf(req), At: time.Now(),
		})
	}
	s.dispatch(c, req, tx, snap, dec)
}

func (s *Server) newCall(req *sip.Request) *call {
	return &call{
		s: s, id: newID(), callID: req.CallID().Value(),
		dialled: req.Recipient.User, start: time.Now(), inv: req, direction: cdr.DirectionInternal,
		canceled: make(chan struct{}), stopHB: make(chan struct{}), setupDone: make(chan struct{}),
		aborted: make(chan struct{}), haStop: make(chan struct{}),
	}
}

func headerOf(req *sip.Request) func(string) string {
	return func(name string) string {
		if h := req.GetHeader(name); h != nil {
			return h.Value()
		}
		return ""
	}
}

// decide runs the routing engine, timed for hello_route_decision_seconds.
func (s *Server) decide(rs *snapshot.RoutingState, c routing.Call) routing.Decision {
	start := time.Now()
	d := rs.Router.Decide(c, s.usability(rs))
	s.m.RouteDecision.Observe(time.Since(start).Seconds())
	return d
}

// dispatch carries out a routing decision.
func (s *Server) dispatch(c *call, req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, dec routing.Decision) {
	c.trace = append(c.trace, dec.Trace...)
	c.route, c.rewritten = dec.Route, dec.Number
	switch {
	case dec.Kind == routing.KindOutbound:
		if c.direction == cdr.DirectionInternal {
			c.direction = cdr.DirectionOutbound
		}
		c.considerAnchor(snap, EndpointInfo{}) // external: caller-side triggers only
		if c.begin(req, tx) {
			c.setupOutbound(dec)
		}
	case (dec.Kind == routing.KindInternal || dec.Kind == routing.KindInbound) && dec.Extension != "":
		c.ringTarget(req, tx, snap, dec.Extension)
	case dec.Kind == routing.KindInbound && dec.SIPURI != "":
		c.considerAnchor(snap, EndpointInfo{}) // external URI: caller-side triggers only
		c.ringURI(req, tx, dec)
	default:
		code, reason := dec.RejectCode, dec.Reason
		if code < 300 {
			code = sip.StatusNotFound
		}
		if reason == "" {
			reason = "no route"
		}
		result := ResultFailed
		switch code {
		case sip.StatusNotFound:
			result = ResultNotFound
		case sip.StatusServiceUnavailable, sip.StatusTemporarilyUnavailable:
			result = ResultUnavailable
		}
		s.respond(tx, req, code, statusText(code))
		c.record(code, cdr.SideSystem, reason, result)
	}
}

// ringURI sends the call to a SIP URI destination.
func (c *call) ringURI(req *sip.Request, tx sip.ServerTransaction, dec routing.Decision) {
	raw := dec.SIPURI
	var u sip.Uri
	if err := sip.ParseUri(strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">"), &u); err != nil {
		c.s.respond(tx, req, sip.StatusInternalServerError, "Server Internal Error")
		c.record(sip.StatusInternalServerError, cdr.SideSystem, "bad SIP URI destination", ResultFailed)
		return
	}
	if u.IsEncrypted() {
		// sips: requires TLS end to end; Hello sends SIP over UDP only.
		c.addTrace(fmt.Sprintf("SIP URI destination %s uses sips:, which needs TLS; Hello only sends UDP", raw))
		c.s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable")
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "sips: destinations are not supported", ResultFailed)
		return
	}
	if !c.begin(req, tx) {
		return
	}
	c.mu.Lock()
	l := c.addLeg(&leg{uri: &u})
	c.mu.Unlock()
	go l.run()
	c.setup(1)
}

// addLeg registers a new leg of the call; c.mu must be held.
func (c *call) addLeg(l *leg) *leg {
	ctx, cancel := context.WithCancel(context.Background())
	l.c, l.callID, l.ctx, l.cancel = c, newID(), ctx, cancel
	c.legs = append(c.legs, l)
	c.s.bind(l.callID, dialogRef{c: c, leg: l})
	return l
}

// begin takes over the caller's INVITE as a dialog, watches for CANCEL and
// publishes the call; false means it has already answered the caller.
func (c *call) begin(req *sip.Request, tx sip.ServerTransaction) bool {
	s := c.s
	dss, err := s.uas.ReadInvite(req, tx)
	if err != nil {
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return false
	}
	c.dss = dss
	c.mu.Lock()
	c.invTx = tx
	c.mu.Unlock()
	if !tx.OnCancel(func(*sip.Request) {
		s.m.request(sip.CANCEL.String())
		c.cancelled()
	}) {
		// Cancelled (or gone) before we could watch for it.
		c.cancelled()
	}
	s.bind(c.callID, dialogRef{c: c})
	s.m.ActiveCalls.Inc()
	c.events = make(chan legEvent, 16)
	c.publish()
	go c.heartbeat()
	return true
}

func (c *call) addTrace(text string) {
	c.mu.Lock()
	c.trace.Add(text)
	c.mu.Unlock()
}

// setup waits for the first fork to answer, every fork to fail, the caller
// to cancel, or the ring timeout.
func (c *call) setup(pending int) {
	defer close(c.setupDone)
	timer := time.NewTimer(c.s.cfg.RingTimeout)
	defer timer.Stop()
	total, busy := pending, 0
	for {
		select {
		case ev := <-c.events:
			if ev.leg != nil && ev.leg.stage != c.curStage() {
				continue // a leg the forwarding stage abandoned
			}
			switch ev.kind {
			case evRinging:
				c.ringing()
			case evAnswered:
				c.answer(ev.leg)
				return
			case evFailed:
				if ev.leg != nil && ev.leg.stage != c.curStage() {
					continue // a leg the forwarding stage abandoned
				}
				pending--
				if isBusy(ev.code) {
					busy++
				}
				if pending == 0 {
					switch c.exhausted(busy == total) {
					case exhRetry: // forwarding opened a new stage
						pending, total, busy = c.pendingLegs(), c.pendingLegs(), 0
						timer.Reset(c.s.cfg.RingTimeout)
						continue
					case exhDone:
						return
					}
					if c.closeSetup() {
						code, reason, result := sip.StatusTemporarilyUnavailable, "Temporarily Unavailable", ResultUnavailable
						if busy == total {
							code, reason, result = sip.StatusBusyHere, "Busy Here", ResultBusy
						}
						c.respondA(code, reason)
						c.end(code, cdr.SideCallee, reason, result)
						return
					}
					if c.setupAbort() {
						return
					}
				}
			}
		case <-c.canceled:
			if c.closeSetup() {
				c.cancelForks(nil)
				// The transaction layer has answered 487 already.
				c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
				return
			}
			if c.setupAbort() {
				return
			}
			// A fork already won; answer() sees the cancellation.
		case <-c.aborted:
			if c.setupAbort() {
				return
			}
			// A fork already won; answer() sees the abort.
		case <-timer.C:
			switch c.exhausted(false) {
			case exhRetry:
				pending, total, busy = c.pendingLegs(), c.pendingLegs(), 0
				timer.Reset(c.s.cfg.RingTimeout)
				continue
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

func isBusy(code int) bool {
	return code == sip.StatusBusyHere || code == sip.StatusGlobalBusyEverywhere || code == sip.StatusGlobalDecline
}

func (c *call) cancelled() {
	c.cancelMu.Do(func() {
		c.mu.Lock()
		c.isCancelled = true
		c.mu.Unlock()
		close(c.canceled)
	})
}

// closeSetup stops any fork from winning; false means one already has, or
// setup already ended.
func (c *call) closeSetup() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setupClosed || c.winner != nil {
		return false
	}
	c.setupClosed = true
	return true
}

// claim makes l the winner if no fork has won and setup is still open.
func (c *call) claim(l *leg) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.setupClosed || c.winner != nil || l.abandoned {
		return false
	}
	c.winner = l
	c.answerTime = time.Now()
	return true
}

// abandon stops l from winning (it was failed over); false means it has
// already won.
func (c *call) abandon(l *leg) bool {
	c.mu.Lock()
	won := c.winner == l
	if !won {
		l.abandoned = true
	}
	c.mu.Unlock()
	if !won {
		l.cancel()
	}
	return !won
}

func (c *call) cancelForks(except *leg) {
	c.mu.Lock()
	legs := c.legs
	c.mu.Unlock()
	for _, l := range legs {
		if l != except {
			l.cancel()
		}
	}
}

func (c *call) ringing() {
	c.mu.Lock()
	first := c.ringTime.IsZero()
	if first {
		c.ringTime = time.Now()
	}
	ext := c.dialled
	c.mu.Unlock()
	if first {
		c.respondA(sip.StatusRinging, "Ringing")
		if snap := c.s.deps.Snapshots.Current(); snap != nil {
			c.s.publishDeviceState(c.s.devicesOf(snap, ext), ext, StateRinging)
		}
	}
}

// respondA sends a provisional or failure response to the caller; for a
// final response it blocks until the caller's ACK. It is counted when sent.
func (c *call) respondA(code int, reason string) {
	c.mu.Lock()
	up := c.callerDialogUp
	c.mu.Unlock()
	if up {
		// The caller's INVITE was answered long ago (a transferred call
		// reuses its confirmed dialog): its transaction is over, and
		// sipgo would still store this response as the dialog's
		// InviteResponse, under the original call reading it.
		return
	}
	c.s.m.response(code)
	if err := c.dss.Respond(code, reason, nil); err != nil {
		c.s.log.Debug("respond to caller failed", "code", code, "error", err)
	}
}

// answer connects the caller to the winning fork.
func (c *call) answer(w *leg) {
	c.cancelForks(w)
	dcs := w.session()
	bres := dcs.InviteResponse
	if len(c.inv.Body()) > 0 {
		w.ack(nil, "") // early offer: the 2xx carries the answer
	} else {
		c.mu.Lock()
		c.lateAck = true // the caller's ACK will carry the answer
		c.mu.Unlock()
	}
	if h := c.s.answerHook.Load(); h != nil {
		(*h)()
	}
	// An abort (drain timeout) that came after the fork won is honoured
	// here: terminate read connected before it was set, so nothing else
	// would end this call. Checked under mu with connected, so terminate
	// either sees the call connected or this sees the abort.
	c.mu.Lock()
	cancelled := c.isCancelled
	aborted := closed(c.aborted)
	c.connected = !cancelled && !aborted
	c.mu.Unlock()
	if cancelled {
		w.bye()
		c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
		return
	}
	if aborted {
		w.bye()
		c.abortCaller()
		return
	}
	c.addTrace("Call established")
	c.publish()
	// record_default recordings start when the call is answered (spec
	// S-4); the flow re-anchors when needed, but an anchored-for-recording
	// call is already set up.
	if c.anchorTriggerWas(AnchorRecording) {
		go c.startRecordingFlow("default")
	}
	if snap := c.s.deps.Snapshots.Current(); snap != nil {
		c.s.publishDeviceState(c.s.devicesOf(snap, c.dialled), c.dialled, StateOnCall)
		if c.callerDevice != "" {
			c.s.publishDeviceState([]string{c.callerDevice}, c.callerNum, StateOnCall)
		}
	}
	body := bres.Body()
	if c.isAnchored() {
		// The caller's answer is the anchor's leg-a SDP, never the
		// callee's body (spec S-2); the callee's answer aims leg b.
		if len(bres.Body()) > 0 {
			if ans, err := media.ParseAudioSDP(bres.Body()); err == nil {
				c.relay.SetTarget(legCallee, udpAddr(ans.Address, ans.Port))
			}
		}
		off, _ := media.ParseAudioSDP(c.inv.Body())
		body = c.anchoredAnswerA(off)
	}
	res := sip.NewResponseFromRequest(c.dss.InviteRequest, sip.StatusOK, "OK", body)
	if ct := bres.ContentType(); ct != nil {
		res.AppendHeader(sip.HeaderClone(ct))
	}
	res.AppendHeader(sip.NewHeader("Allow", allow))
	// Counted when sent; WriteResponse blocks until the caller ACKs (or
	// 64*T1).
	c.s.m.response(sip.StatusOK)
	err := c.dss.WriteResponse(res)
	if err == nil {
		c.mu.Lock()
		if !c.ended {
			// debt: a fixed cap stands in for RFC 4028 session timers, which
			// would clear a call within minutes of a phone vanishing without
			// BYE. Revisit when calls must be cleared that fast, or when a
			// 4h call is a real use case.
			c.maxTimer = time.AfterFunc(c.s.cfg.MaxCallDuration, c.expire)
		}
		c.mu.Unlock()
		// Replication starts with the dialog confirmed: the snapshot is
		// taken here and after every in-dialog transaction, and the
		// heartbeat's write cadence is the owner's liveness.
		if c.s.deps.HAState != nil {
			c.haRefresh()
			go c.haLoop()
		}
		return
	}
	c.mu.Lock()
	cancelled = c.isCancelled
	c.connected = false
	c.mu.Unlock()
	w.bye()
	if cancelled {
		c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
		return
	}
	c.s.log.Info("caller did not acknowledge answer", "correlation_id", c.id, "error", err)
	c.byeA()
	c.end(sip.StatusOK, cdr.SideSystem, "no ACK from caller", ResultFailed)
}

// hangup ends a connected call from one side and sends BYE to the other.
// The dialogs stay routable until that BYE completes, so a caller ACK still
// in flight confirms the A dialog the BYE needs.
func (c *call) hangup(side string) {
	c.mu.Lock()
	if c.hungUp {
		c.mu.Unlock()
		return
	}
	c.hungUp = true
	w := c.winner
	hom := c.homedCall
	c.mu.Unlock()
	c.end(sip.StatusOK, side, "", ResultAnswered)
	go func() {
		defer c.release()
		defer contain(c.s.log, "hangup")
		if hom != nil {
			// A taken-over call has no sipgo sessions: raw leg BYEs.
			if side == cdr.SideCaller {
				c.s.haBye(hom.leg(true))
			} else {
				c.s.haBye(hom.leg(false))
			}
			return
		}
		if side == cdr.SideCaller {
			w.bye()
		} else {
			c.byeA()
		}
	}()
}

// inDialogWait bounds how long ending a call waits for an in-dialog
// transaction being relayed (Timer B/F, 64*T1).
const inDialogWait = 32 * time.Second

// beginInDialog counts an in-dialog transaction (re-INVITE, UPDATE) being
// relayed; false means the call is being ended and takes no new ones.
func (c *call) beginInDialog() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.hungUp || c.ended {
		return false
	}
	c.inDialog.Add(1)
	return true
}

func (c *call) endInDialog() { c.inDialog.Done() }

// waitInDialog waits, at most max, until no in-dialog transaction is being
// relayed.
func (c *call) waitInDialog(max time.Duration) {
	idle := make(chan struct{})
	go func() { c.inDialog.Wait(); close(idle) }()
	t := time.NewTimer(max)
	defer t.Stop()
	select {
	case <-idle:
	case <-t.C:
		c.s.log.Warn("ending a call with an in-dialog transaction still open", "correlation_id", c.id)
	}
}

// closed reports whether ch is closed.
func closed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// abort asks the setup goroutine to end a call that is not connected yet.
// Under the same lock that guards setupClosed it takes the setup's
// ownership, so a fork failure, the ring timer or fork exhaustion cannot
// win closeSetup afterwards and record a normal callee or timeout CDR:
// the drain's system side and reason survive.
func (c *call) abort(reason string) {
	c.abortOnce.Do(func() {
		c.mu.Lock()
		c.abortReason = reason
		if !c.setupClosed && c.winner == nil {
			c.setupClosed = true
			c.abortOwned = true
		}
		c.mu.Unlock()
		// Run after the ownership is taken, before the signal: this is
		// exactly the window the ownership closes (tests only).
		if h := c.s.abortHook.Load(); h != nil {
			(*h)(c)
		}
		close(c.aborted)
	})
}

// setupAbort ends setup on behalf of an abort that owns it (drain timeout):
// it reports whether the abort owned the setup's end, and answers the
// caller 503 when it did. Losing closeSetup to that ownership is how the
// setup goroutine observes the abort.
func (c *call) setupAbort() bool {
	c.mu.Lock()
	owned := c.abortOwned
	c.mu.Unlock()
	if !owned {
		return false
	}
	c.cancelForks(nil)
	c.abortCaller()
	return true
}

// abortCaller answers the caller 503 and ends an aborted call; setup has
// closed and the forks are cancelled.
func (c *call) abortCaller() {
	c.mu.Lock()
	reason := c.abortReason
	c.mu.Unlock()
	c.addTrace("Call ended by the node: " + reason)
	c.respondA(sip.StatusServiceUnavailable, "Service Unavailable")
	c.end(sip.StatusServiceUnavailable, cdr.SideSystem, reason, ResultFailed)
}

// expire ends a call that reached MaxCallDuration: BYE to both legs.
func (c *call) expire() {
	c.s.log.Info("call reached the maximum duration", "correlation_id", c.id, "max", c.s.cfg.MaxCallDuration.String())
	c.endBoth("max duration")
}

// endBoth ends a connected call for a system reason (max duration, drain
// timeout): BYE to both legs, CDR side system.
func (c *call) endBoth(reason string) {
	c.mu.Lock()
	if c.hungUp || c.ended {
		c.mu.Unlock()
		return
	}
	c.hungUp = true
	w := c.winner
	hom := c.homedCall
	c.mu.Unlock()
	c.end(sip.StatusOK, cdr.SideSystem, reason, ResultAnswered)
	go func() {
		defer c.release()
		defer contain(c.s.log, "end both legs")
		if hom != nil {
			done := make(chan struct{})
			go func() { defer close(done); c.s.haBye(hom.leg(true)) }()
			c.s.haBye(hom.leg(false))
			<-done
			return
		}
		// A re-INVITE or UPDATE being relayed finishes first: the BYE
		// follows its transaction's end, bounded by the transaction
		// timeout (spec edge case "drain timeout during a re-INVITE").
		c.waitInDialog(inDialogWait)
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer contain(c.s.log, "end both legs")
			w.bye()
		}()
		c.byeA()
		<-done
	}()
}

func (c *call) byeA() {
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	if err := c.dss.Bye(ctx); err != nil {
		c.s.log.Debug("BYE to caller failed", "correlation_id", c.id, "error", err)
	}
}

// end finishes the call once: CDR, metrics, live state. An answered call's
// dialogs are released by hangup once its BYE completes; any other call's
// are released here.
func (c *call) end(status int, side, reason, result string) {
	c.endOnce.Do(func() {
		c.mu.Lock()
		c.ended = true
		held := c.held
		if c.maxTimer != nil {
			c.maxTimer.Stop()
		}
		c.mu.Unlock()
		c.closeMedia()
		if held {
			c.s.m.HoldActive.Dec()
		}
		c.s.dropBuffer(c)
		c.mu.Lock()
		cn, cd, de := c.callerNum, c.callerDevice, c.dialled
		c.mu.Unlock()
		c.s.restorePresenceOf(cn, cd, de)
		if c.haStop != nil {
			close(c.haStop)
		}
		c.haDelete()
		if result != ResultAnswered {
			c.release()
		}
		c.s.m.ActiveCalls.Dec()
		c.mu.Lock()
		yielded := c.haYielded
		c.mu.Unlock()
		if yielded {
			// A call handed to (or taken over by) another node is not
			// over: the live record (same correlation id) and the one CDR
			// of the logical call are the taker's. Deleting the record
			// here would hide the call the taker just published, and a
			// CDR here would take the correlation id the taker's final
			// CDR needs (one CDR per call).
			c.releaseSlots()
			return
		}
		c.unpublish()
		c.record(status, side, reason, result)
	})
}

// unpublish removes the live call, after any publish in flight; ended is
// already set, so none can follow.
func (c *call) unpublish() {
	c.pubMu.Lock()
	defer c.pubMu.Unlock()
	ctx, cancel := c.s.stateCtx()
	defer cancel()
	if err := c.s.deps.State.DeleteCall(ctx, c.id); err != nil {
		c.s.log.Warn("could not remove live call", "correlation_id", c.id, "error", err)
	}
}

// release stops routing in-dialog requests to this call.
func (c *call) release() {
	c.mu.Lock()
	legs := c.legs
	hom := c.homedCall
	c.mu.Unlock()
	c.s.unbind(c.callID, c)
	for _, l := range legs {
		c.s.unbind(l.callID, c)
	}
	if hom != nil {
		// A taken-over call's callee dialog is bound outside c.legs.
		for _, l := range hom.legs {
			if l != nil {
				c.s.unbind(l.callID, c)
			}
		}
	}
}

// record counts the attempt and queues its CDR. An attempt that did not
// connect ends its trace with the reason, which is the CDR's explanation.
func (c *call) record(status int, side, reason, result string) {
	c.releaseSlots() // every way an attempt ends passes here exactly once
	end := time.Now()
	c.mu.Lock()
	ring, answer := c.ringTime, c.answerTime
	if result != ResultAnswered {
		answer = time.Time{}
		if reason != "" {
			c.trace.Add(fmt.Sprintf("Call not connected: %s (%d %s)", reason, status, statusText(status)))
		}
	}
	trace := append(routing.Trace(nil), c.trace...)
	r := cdr.Record{
		CorrelationID: c.id, SIPCallID: c.callID, Source: c.callerNum, Destination: c.dialled,
		StartTime: c.start, RingTime: ring, AnswerTime: answer, EndTime: end,
		DurationMs: end.Sub(c.start).Milliseconds(), SIPNode: c.s.cfg.NodeID, MediaMode: "direct",
		FinalStatus: status, TerminationSide: side, FailureReason: reason,
		Direction: c.direction, OriginalDestination: c.dialled, RewrittenDestination: c.rewritten,
		Route: c.route, Trunk: c.trunkName, Trace: trace,
	}
	c.mu.Unlock()
	if !answer.IsZero() {
		r.BillableMs = end.Sub(answer).Milliseconds()
	}
	c.s.m.Calls.WithLabelValues(result).Inc()
	if c.groupName != "" {
		c.s.m.GroupCalls.WithLabelValues(c.groupName, c.groupStrategy, result).Inc()
	}
	c.s.noteCallEnd(c.callerNum, c.dialled)
	c.mu.Lock()
	media := c.mediaMode
	c.mu.Unlock()
	if media != "" {
		r.MediaMode = media
	}
	if r.MediaMode == "anchored" && !answer.IsZero() {
		if p, l, j, ok := c.qualityStats(); ok {
			r.RTPPackets, r.RTPLost, r.RTPJitterMs = &p, &l, &j
		}
	}
	c.s.deps.CDRs.Enqueue(r)
}

func (c *call) live() livestate.Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	state := "ringing"
	var answered time.Time
	if c.connected {
		state, answered = "connected", c.answerTime
	}
	ha := livestate.HAOwned
	if c.homedCall != nil {
		ha = livestate.HATakenOver
	}
	return livestate.Call{
		ID: c.id, SIPCallID: c.callID, From: c.callerNum, To: c.dialled, State: state,
		Node: c.s.cfg.NodeID, Media: "direct", StartedAt: c.start, AnsweredAt: answered, HA: ha,
	}
}

func (c *call) publish() {
	c.pubMu.Lock()
	defer c.pubMu.Unlock()
	c.mu.Lock()
	ended, handoff := c.ended, c.haHandoff && c.haHandoffWritten
	c.mu.Unlock()
	if ended {
		return
	}
	if handoff {
		// A call handed off for takeover: once a survivor has claimed it,
		// the live record (same correlation id) is the taker's, and this
		// node's copy must not overwrite it before the yield.
		if taker := c.s.haClaimedBy(c.callID); taker != "" && taker != c.s.cfg.NodeID {
			return
		}
	}
	ctx, cancel := c.s.stateCtx()
	defer cancel()
	if err := c.s.deps.State.PutCall(ctx, c.live(), c.s.cfg.CallTTL); err != nil {
		c.s.log.Warn("could not publish live call", "correlation_id", c.id, "error", err)
	}
}

func (c *call) heartbeat() {
	t := time.NewTicker(c.s.cfg.CallHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-c.stopHB:
			return
		case <-c.s.done:
			return
		case <-t.C:
			c.publish()
			c.refreshSlots()
		}
	}
}

// contain logs a panic from sipgo instead of crashing the node. sipgo
// v1.6.0's DialogClientSession can dereference a nil response when a
// transaction is terminated concurrently (e.g. while the UA shuts down).
func contain(log interface{ Error(string, ...any) }, what string) {
	if r := recover(); r != nil {
		log.Error("recovered panic", "in", what, "panic", fmt.Sprint(r))
	}
}

// run sends this fork's INVITE and reports its outcome to the call exactly
// once, even if sipgo panics.
func (l *leg) run() {
	reported := false
	report := func(ev legEvent) {
		if ev.kind == evAnswered || ev.kind == evFailed {
			reported = true
		}
		l.c.report(ev)
	}
	defer func() {
		if r := recover(); r != nil {
			l.c.s.log.Error("recovered panic", "in", "fork", "panic", fmt.Sprint(r))
			if !reported {
				report(legEvent{leg: l, kind: evFailed, code: sip.StatusInternalServerError})
			}
		}
	}()
	l.drive(report)
}

func (l *leg) drive(report func(legEvent)) {
	s := l.c.s
	req, err := l.invite()
	if err != nil {
		s.log.Warn("bad binding contact", "contact", l.binding.ContactURI, "error", err)
		report(legEvent{leg: l, kind: evFailed, code: sip.StatusTemporarilyUnavailable})
		return
	}
	dcs, err := s.uac.WriteInvite(context.Background(), req)
	if err != nil {
		s.log.Debug("fork INVITE failed", "contact", l.binding.ContactURI, "error", err)
		report(legEvent{leg: l, kind: evFailed, code: sip.StatusServiceUnavailable})
		return
	}
	l.mu.Lock()
	l.dcs = dcs
	l.mu.Unlock()
	opts := sipgo.AnswerOptions{OnResponse: func(r *sip.Response) error {
		switch {
		case r.StatusCode > 100 && r.StatusCode < 200:
			select {
			case l.c.events <- legEvent{leg: l, kind: evRinging}:
			default:
			}
		case l.trunk != nil && (r.StatusCode == sip.StatusUnauthorized || r.StatusCode == sip.StatusProxyAuthRequired):
			report(legEvent{leg: l, kind: evChallenged, code: r.StatusCode, reason: r.Reason})
		}
		return nil
	}}
	if l.trunk != nil {
		// A carrier's 401 or 407 is answered with the trunk credentials.
		opts.Username, opts.Password = l.trunk.Username, l.trunk.Password
	}
	err = dcs.WaitAnswer(l.ctx, opts)
	if res := dcs.InviteResponse; err == nil || (res != nil && res.IsSuccess()) {
		if l.c.claim(l) {
			report(legEvent{leg: l, kind: evAnswered})
			return
		}
		// Answered after another fork won or the caller left: ACK, then BYE.
		l.bye()
		return
	}
	code, reason := sip.StatusTemporarilyUnavailable, ""
	var de *sipgo.ErrDialogResponse
	switch {
	case errors.As(err, &de):
		code, reason = de.Res.StatusCode, de.Res.Reason
	case errors.Is(err, sip.ErrTransactionTimeout):
		code = sip.StatusRequestTimeout
	}
	report(legEvent{leg: l, kind: evFailed, code: code, reason: reason})
}

// report hands a fork's outcome to setup, or drops it when setup has
// already returned (so the fork's goroutine never blocks on a full queue).
func (c *call) report(ev legEvent) {
	select {
	case c.events <- ev:
	case <-c.setupDone:
	}
}

// invite builds the fork's INVITE: new Call-ID and tags, the caller's SDP
// unchanged, sent to the binding's source address.
func (l *leg) invite() (*sip.Request, error) {
	c, s := l.c, l.c.s
	var ruri sip.Uri
	switch {
	case l.trunk != nil:
		ruri = sip.Uri{Scheme: "sip", User: l.number, Host: l.dest.Host, Port: l.dest.Port}
	case l.uri != nil:
		ruri = *l.uri.Clone()
	default:
		if err := sip.ParseUri(l.binding.ContactURI, &ruri); err != nil {
			return nil, err
		}
	}
	req := sip.NewRequest(sip.INVITE, ruri)
	from := &sip.FromHeader{
		DisplayName: c.callerName,
		Address:     sip.Uri{Scheme: "sip", User: c.callerNum, Host: s.cfg.Domain},
		Params:      sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(16)}},
	}
	to := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: c.dialled, Host: s.cfg.Domain}}
	switch {
	case l.trunk != nil:
		// The carrier sees the decision's caller ID in the trunk's domain
		// and the rewritten number.
		from.DisplayName = ""
		from.Address = sip.Uri{Scheme: "sip", User: l.callerID, Host: trunkDomain(*l.trunk)}
		to.Address = sip.Uri{Scheme: "sip", User: l.number, Host: ruri.Host}
	case l.uri != nil:
		to.Address = *ruri.Clone()
	}
	callID := sip.CallIDHeader(l.callID)
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	maxFwd := sip.MaxForwardsHeader(70)
	if mf := c.inv.MaxForwards(); mf != nil && mf.Val() > 1 {
		maxFwd = sip.MaxForwardsHeader(mf.Val() - 1)
	}
	req.AppendHeader(&maxFwd)
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Allow", allow))
	if body := c.inv.Body(); len(body) > 0 {
		if ct := c.inv.ContentType(); ct != nil {
			req.AppendHeader(sip.HeaderClone(ct))
		}
		req.SetBody(body)
	}
	// An anchored call terminates media: the fork's offer is the anchor's
	// leg-b port, not the caller's SDP (spec S-2).
	if l.c.isAnchored() {
		if off, err := media.ParseAudioSDP(c.inv.Body()); err == nil {
			if body := l.c.anchoredOfferB(off); len(body) > 0 {
				req.SetBody(body)
				req.RemoveHeader("Content-Type")
				req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			}
		}
	}
	req.SetTransport("UDP")
	switch {
	case l.trunk != nil:
		req.SetDestination(l.addr)
	case l.uri != nil:
		// sipgo resolves the URI's host.
	case len(l.binding.Path) > 0 && !strings.Contains(l.binding.Path[0], "hflow="):
		// Registered through a trusted edge proxy (Kamailio): its Path
		// reaches the phone's NAT flow from any node (plan contract 7).
		var first sip.Uri
		if err := sip.ParseUri(strings.Trim(l.binding.Path[0], "<> "), &first); err != nil {
			return nil, err
		}
		for _, p := range l.binding.Path {
			req.AppendHeader(sip.NewHeader("Route", p))
		}
		req.SetDestination(hostPort(first))
	case l.binding.ReceivedNode != s.cfg.NodeID && len(l.binding.Path) > 0:
		// Registered through another node: only that node's flow reaches
		// the phone, so route via its Path (it edge-proxies the INVITE).
		var first sip.Uri
		if err := sip.ParseUri(strings.Trim(l.binding.Path[0], "<> "), &first); err != nil {
			return nil, err
		}
		for _, p := range l.binding.Path {
			req.AppendHeader(sip.NewHeader("Route", p))
		}
		req.SetDestination(hostPort(first))
	case l.binding.Source != "":
		req.SetDestination(l.binding.Source)
	}
	return req, nil
}

// ack acknowledges this fork's 2xx once, optionally carrying the caller's
// SDP.
func (l *leg) ack(body []byte, contentType string) {
	l.mu.Lock()
	dcs, done := l.dcs, l.acked
	l.acked = true
	l.mu.Unlock()
	if done || dcs == nil || dcs.InviteResponse == nil || !dcs.InviteResponse.IsSuccess() {
		return
	}
	target := dcs.InviteRequest.Recipient
	if ct := dcs.InviteResponse.Contact(); ct != nil {
		target = ct.Address
	}
	ack := sip.NewRequest(sip.ACK, *target.Clone())
	if len(body) > 0 {
		if contentType != "" {
			ack.AppendHeader(sip.NewHeader("Content-Type", contentType))
		}
		ack.SetBody(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	if err := dcs.WriteAck(ctx, ack); err != nil {
		l.c.s.log.Debug("ACK to fork failed", "error", err)
	}
}

// bye ends this fork's dialog; a 2xx not yet acknowledged is ACKed first,
// as RFC 3261 requires and sipgo enforces.
func (l *leg) bye() {
	dcs := l.session()
	if dcs == nil || dcs.InviteResponse == nil || !dcs.InviteResponse.IsSuccess() {
		return
	}
	l.ack(nil, "")
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	if err := dcs.Bye(ctx); err != nil {
		l.c.s.log.Debug("BYE to fork failed", "error", err)
	}
}

// handleAck routes an ACK for a 2xx: the caller's ACK for the answer
// confirms the A dialog (and is forwarded for a late offer); an ACK for a
// relayed re-INVITE is relayed to the other leg.
func (s *Server) handleAck(req *sip.Request, tx sip.ServerTransaction) {
	ref, ok := s.matchDialog(req)
	if !ok {
		return
	}
	if ref.c.homed() != nil {
		return // a taken-over call's ACKs need no relay (its 200s are terminal)
	}
	c := ref.c
	inviteAck := ref.leg == nil && req.CSeq().SeqNo == c.inv.CSeq().SeqNo
	c.mu.Lock()
	w, lateAck, dss := c.winner, c.lateAck, c.dss
	if inviteAck {
		c.lateAck = false
	}
	c.mu.Unlock()
	// The caller's ACK confirms its dialog whichever call the dialog is
	// bound to by now: a REFER that overtook it binds the Call-ID to the
	// transferred call (no winner while its target rings), and the
	// original answer would otherwise wait out 64*T1 for an ACK it never
	// reads, unreplicated.
	if inviteAck && dss != nil {
		if err := dss.ReadAck(req, tx); err != nil {
			s.log.Debug("caller ACK rejected", "error", err)
			return
		}
	}
	if w == nil {
		return
	}
	ct := ""
	if h := req.ContentType(); h != nil {
		ct = h.Value()
	}
	switch {
	case inviteAck:
		if lateAck {
			w.ack(req.Body(), ct)
		}
	case ref.leg == nil:
		s.relayAck(req, ct, func(ack *sip.Request) error { return w.session().WriteRequest(ack) }, w.target())
	case ref.leg == w:
		s.relayAck(req, ct, c.dss.WriteRequest, c.target())
	}
}

func (s *Server) relayAck(req *sip.Request, ct string, write func(*sip.Request) error, target sip.Uri) {
	ack := sip.NewRequest(sip.ACK, target)
	if body := req.Body(); len(body) > 0 {
		if ct != "" {
			ack.AppendHeader(sip.NewHeader("Content-Type", ct))
		}
		ack.SetBody(body)
	}
	if err := write(ack); err != nil {
		s.log.Debug("ACK relay failed", "error", err)
	}
}

// target is the remote target of the caller's dialog.
func (c *call) target() sip.Uri {
	if ct := c.inv.Contact(); ct != nil {
		return *ct.Address.Clone()
	}
	return *c.inv.Recipient.Clone()
}

// target is the remote target of this fork's dialog.
func (l *leg) target() sip.Uri {
	dcs := l.session()
	if ct := dcs.InviteResponse.Contact(); ct != nil {
		return *ct.Address.Clone()
	}
	return *dcs.InviteRequest.Recipient.Clone()
}

// matchDialog finds the call leg an in-dialog request belongs to. The
// Call-ID only selects a candidate; the request must carry both of that
// dialog's tags (RFC 3261 §12.2.2) and come from the leg's peer: the
// caller's source for the A leg, the address the winning fork's 2xx came
// from (the phone, or the edge node it is reached through) for the B leg.
// Losing forks never match.
func (s *Server) matchDialog(req *sip.Request) (dialogRef, bool) {
	ref, ok := s.lookup(req.CallID().Value())
	if !ok {
		return dialogRef{}, false
	}
	c := ref.c
	if hom := c.homed(); hom != nil {
		// A taken-over call matches from its replicated dialogs (the tags
		// and the endpoint's source are the old owner's); other in-dialog
		// methods (REFER and friends) are not recovered and answer 481.
		switch req.Method {
		case sip.INVITE, sip.ACK, sip.BYE, sip.UPDATE:
			return s.matchHomed(c, hom, req, ref)
		}
		return dialogRef{}, false
	}
	if ref.leg == nil {
		id, err := sip.DialogIDFromRequestUAS(req)
		if err == nil && c.dss != nil && id == c.dss.ID && req.Source() == c.inv.Source() {
			return ref, true
		}
		// A callee-less answer (voicemail) carries its own To tag instead
		// of a sipgo dialog.
		c.mu.Lock()
		tag := c.vmTag
		c.mu.Unlock()
		if v, has := req.To().Params.Get("tag"); tag != "" {
			return ref, has && v == tag && req.Source() == c.inv.Source()
		}
		return ref, false
	}
	c.mu.Lock()
	w := c.winner
	c.mu.Unlock()
	if ref.leg != w {
		return dialogRef{}, false
	}
	res := w.session().InviteResponse // stable once the fork has won
	want, err1 := sip.DialogIDFromResponse(res)
	got, err2 := sip.DialogIDFromRequestUAC(req)
	return ref, err1 == nil && err2 == nil && got == want && req.Source() == res.Source()
}

// matchHomed verifies an in-dialog request against a taken-over call's
// replicated dialogs: the Call-ID selects the leg, and the request must
// carry exactly that dialog's tags from the endpoint's side. It must come
// from the endpoint's replicated source or from any trusted edge proxy
// (HELLO_SIP_TRUSTED_PROXIES): the source the old owner recorded is the
// edge as that node saw it, and the edge reaches a taker on another node
// from another address (kw: replicated 10.42.0.128:5070, the edge pod;
// arriving from 192.168.10.102:5070, its node), so an exact match would
// refuse every BYE after a takeover with 481.
func (s *Server) matchHomed(c *call, hom *homedCall, req *sip.Request, ref dialogRef) (dialogRef, bool) {
	l := hom.leg(ref.leg != nil)
	if l == nil {
		return dialogRef{}, false
	}
	l.mu.Lock()
	localTag, remoteTag, source := l.localTag, l.remoteTag, l.source
	l.mu.Unlock()
	if tagParam(req.From()) != remoteTag || tagParam(req.To()) != localTag {
		return dialogRef{}, false
	}
	if req.Source() != source && !s.fromTrustedProxy(req) {
		return dialogRef{}, false
	}
	return ref, true
}

// haByeRequest ends a taken-over call from one of its endpoints: 200, then
// the other leg's BYE, then the dialogs release.
func (s *Server) haByeRequest(c *call, hom *homedCall, fromCallee bool, req *sip.Request, tx sip.ServerTransaction) {
	c.mu.Lock()
	if c.hungUp || c.ended {
		c.mu.Unlock()
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	c.hungUp = true
	c.mu.Unlock()
	s.respond(tx, req, sip.StatusOK, "OK")
	// The side that sent the BYE ended the call (as handleBye records it).
	side := cdr.SideCaller
	if fromCallee {
		side = cdr.SideCallee
	}
	c.end(sip.StatusOK, side, "", ResultAnswered)
	go func() {
		defer c.release()
		defer contain(c.s.log, "homed bye")
		c.s.haBye(hom.leg(!fromCallee))
	}()
}

// handleBye ends a connected call from either side.
func (s *Server) handleBye(req *sip.Request, tx sip.ServerTransaction) {
	ref, ok := s.matchDialog(req)
	if !ok {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	c := ref.c
	if hom := c.homed(); hom != nil {
		s.haByeRequest(c, hom, ref.leg != nil, req, tx)
		return
	}
	c.mu.Lock()
	w, connected := c.winner, c.connected
	c.mu.Unlock()
	switch {
	case !connected || (w == nil && ref.leg != nil):
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
	case ref.leg == nil:
		// The caller's BYE (including on a callee-less voicemail call).
		if c.dss != nil {
			if err := c.dss.ReadBye(req, tx); err != nil {
				s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
				return
			}
		}
		s.m.response(sip.StatusOK)
		c.hangup(cdr.SideCaller)
	default: // matchDialog only matches the caller's leg or the winner's
		if err := w.session().ReadBye(req, tx); err != nil {
			s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
			return
		}
		s.m.response(sip.StatusOK)
		c.hangup(cdr.SideCallee)
	}
}

// handleInDialog relays a re-INVITE or UPDATE to the other leg and its final
// response back, bodies unchanged.
func (s *Server) handleInDialog(req *sip.Request, tx sip.ServerTransaction) {
	ref, ok := s.matchDialog(req)
	if !ok {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	c := ref.c
	c.mu.Lock()
	w, connected := c.winner, c.connected
	c.mu.Unlock()
	if !connected || w == nil || (ref.leg != nil && ref.leg != w) {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	if !c.beginInDialog() {
		// The call is being ended (BYE on its way): no new transactions.
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	defer c.endInDialog()
	if hom := c.homed(); hom != nil {
		// A taken-over call terminates media in dialog from the replicated
		// relay: hold direction mirrored, the peer re-INVITEd the same way.
		c.haInDialog(hom, req, tx, ref.leg == nil)
		return
	}
	// An anchored call terminates media in dialog too: a re-INVITE offer
	// is answered with the anchor's SDP and mirrored to the peer (hold
	// included), so both dialogs keep pointing at the relay (spec S-2).
	if c.isAnchored() {
		c.handleAnchoredInDialog(req, tx, ref.leg == nil)
		return
	}
	// Hold (S-1) is signalled by the phone's re-INVITE SDP direction: a
	// sendonly (or inactive, or zero-connection) offer holds the call from
	// whichever side sends it; a sendrecv re-INVITE resumes it. The body is
	// relayed unchanged, so both phones agree on the direction.
	c.setHeld(sdpHeld(req.Body()))
	var (
		out *sip.Request
		do  func(context.Context, *sip.Request) (*sip.Response, error)
	)
	if ref.leg == nil {
		out, do = sip.NewRequest(req.Method, w.target()), w.session().Do
	} else {
		out, do = sip.NewRequest(req.Method, c.target()), c.dss.Do
	}
	if body := req.Body(); len(body) > 0 {
		if ct := req.ContentType(); ct != nil {
			out.AppendHeader(sip.HeaderClone(ct))
		}
		out.SetBody(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := do(ctx, out)
	if err != nil {
		s.respond(tx, req, sip.StatusRequestTimeout, "Request Timeout")
		return
	}
	relayed := sip.NewResponseFromRequest(req, res.StatusCode, res.Reason, res.Body())
	if ct := res.ContentType(); ct != nil {
		relayed.AppendHeader(sip.HeaderClone(ct))
	}
	if res.IsSuccess() {
		relayed.AppendHeader(sip.HeaderClone(&s.contact))
	}
	s.send(tx, relayed)
	c.haRefresh()
}
