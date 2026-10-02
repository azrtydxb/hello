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
	s       *Server
	id      string // correlation ID
	callID  string // caller's Call-ID
	caller  snapshot.Device
	dialled string
	start   time.Time
	inv     *sip.Request
	dss     *sipgo.DialogServerSession

	events    chan legEvent
	setupDone chan struct{} // closed when setup stops reading events
	canceled  chan struct{}
	maxTimer  *time.Timer // ends a connected call at MaxCallDuration
	cancelMu  sync.Once
	stopHB    chan struct{}
	endOnce   sync.Once

	mu          sync.Mutex
	legs        []*leg
	winner      *leg
	setupClosed bool // no fork may win any more
	isCancelled bool
	connected   bool
	ended       bool
	hungUp      bool
	lateAck     bool // the winner's ACK waits for the caller's (late offer)
	ringTime    time.Time
	answerTime  time.Time
}

type legEventKind int

const (
	evRinging legEventKind = iota
	evAnswered
	evFailed
)

type legEvent struct {
	leg  *leg
	kind legEventKind
	code int
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
func (s *Server) handleInvite(req *sip.Request, tx sip.ServerTransaction) {
	if isInDialog(req) {
		s.handleInDialog(req, tx)
		return
	}
	if _, ok := s.lookup(req.CallID().Value()); ok {
		// Same Call-ID as a call in progress but a new transaction: a
		// merged or looped request (RFC 3261 §8.2.2.2).
		s.respond(tx, req, sip.StatusLoopDetected, "Loop Detected")
		return
	}
	dev, snap, ok := s.authenticate(req, tx)
	if !ok {
		return
	}
	c := &call{
		s: s, id: newID(), callID: req.CallID().Value(), caller: dev,
		dialled: req.Recipient.User, start: time.Now(), inv: req,
		canceled: make(chan struct{}), stopHB: make(chan struct{}), setupDone: make(chan struct{}),
	}
	if !snap.HasExtension(c.dialled) {
		s.respond(tx, req, sip.StatusNotFound, "Not Found")
		c.record(sip.StatusNotFound, cdr.SideSystem, "unknown number", ResultNotFound)
		return
	}
	var targets []livestate.Binding
	for _, d := range snap.DevicesForExtension(c.dialled) {
		if d.Username == dev.Username {
			continue // never ring the calling device
		}
		ctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
		cancel()
		if err != nil {
			s.stateDown(tx, req, "bindings", err)
			c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "live state unavailable", ResultFailed)
			return
		}
		targets = append(targets, bs...)
	}
	if len(targets) == 0 {
		s.respond(tx, req, sip.StatusTemporarilyUnavailable, "Temporarily Unavailable")
		c.record(sip.StatusTemporarilyUnavailable, cdr.SideSystem, "no registered device", ResultUnavailable)
		return
	}
	dss, err := s.uas.ReadInvite(req, tx)
	if err != nil {
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return
	}
	c.dss = dss
	if !tx.OnCancel(func(*sip.Request) {
		s.m.request(sip.CANCEL.String())
		c.cancelled()
	}) {
		// Cancelled (or gone) before we could watch for it.
		c.cancelled()
	}
	s.bind(c.callID, dialogRef{c: c})
	s.m.ActiveCalls.Inc()
	c.publish()
	go c.heartbeat()

	c.events = make(chan legEvent, len(targets)+4)
	c.mu.Lock()
	for _, b := range targets {
		ctx, cancel := context.WithCancel(context.Background())
		l := &leg{c: c, binding: b, callID: newID(), ctx: ctx, cancel: cancel}
		c.legs = append(c.legs, l)
		s.bind(l.callID, dialogRef{c: c, leg: l})
	}
	legs := c.legs
	c.mu.Unlock()
	for _, l := range legs {
		go l.run()
	}
	c.setup(len(legs))
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
			switch ev.kind {
			case evRinging:
				c.ringing()
			case evAnswered:
				c.answer(ev.leg)
				return
			case evFailed:
				pending--
				if isBusy(ev.code) {
					busy++
				}
				if pending == 0 && c.closeSetup() {
					code, reason, result := sip.StatusTemporarilyUnavailable, "Temporarily Unavailable", ResultUnavailable
					if busy == total {
						code, reason, result = sip.StatusBusyHere, "Busy Here", ResultBusy
					}
					c.respondA(code, reason)
					c.end(code, cdr.SideCallee, reason, result)
					return
				}
			}
		case <-c.canceled:
			if c.closeSetup() {
				c.cancelForks(nil)
				// The transaction layer has answered 487 already.
				c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
				return
			}
			// A fork already won; answer() sees the cancellation.
		case <-timer.C:
			if c.closeSetup() {
				c.cancelForks(nil)
				c.respondA(sip.StatusRequestTimeout, "Request Timeout")
				c.end(sip.StatusRequestTimeout, cdr.SideSystem, "ring timeout", ResultNoAnswer)
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
	if c.setupClosed || c.winner != nil {
		return false
	}
	c.winner = l
	c.answerTime = time.Now()
	return true
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
	c.mu.Unlock()
	if first {
		c.respondA(sip.StatusRinging, "Ringing")
	}
}

// respondA sends a provisional or failure response to the caller; for a
// final response it blocks until the caller's ACK. It is counted when sent.
func (c *call) respondA(code int, reason string) {
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
	c.mu.Lock()
	cancelled := c.isCancelled
	c.connected = !cancelled
	c.mu.Unlock()
	if cancelled {
		w.bye()
		c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
		return
	}
	c.publish()
	res := sip.NewResponseFromRequest(c.dss.InviteRequest, sip.StatusOK, "OK", bres.Body())
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
	c.mu.Unlock()
	c.end(sip.StatusOK, side, "", ResultAnswered)
	go func() {
		defer c.release()
		defer contain(c.s.log, "hangup")
		if side == cdr.SideCaller {
			w.bye()
		} else {
			c.byeA()
		}
	}()
}

// expire ends a call that reached MaxCallDuration: BYE to both legs.
func (c *call) expire() {
	c.mu.Lock()
	if c.hungUp || c.ended {
		c.mu.Unlock()
		return
	}
	c.hungUp = true
	w := c.winner
	c.mu.Unlock()
	c.s.log.Info("call reached the maximum duration", "correlation_id", c.id, "max", c.s.cfg.MaxCallDuration.String())
	c.end(sip.StatusOK, cdr.SideSystem, "max duration", ResultAnswered)
	go func() {
		defer c.release()
		defer contain(c.s.log, "expire")
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer contain(c.s.log, "expire")
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
		if c.maxTimer != nil {
			c.maxTimer.Stop()
		}
		c.mu.Unlock()
		close(c.stopHB)
		if result != ResultAnswered {
			c.release()
		}
		c.s.m.ActiveCalls.Dec()
		ctx, cancel := c.s.stateCtx()
		if err := c.s.deps.State.DeleteCall(ctx, c.id); err != nil {
			c.s.log.Warn("could not remove live call", "correlation_id", c.id, "error", err)
		}
		cancel()
		c.record(status, side, reason, result)
	})
}

// release stops routing in-dialog requests to this call.
func (c *call) release() {
	c.mu.Lock()
	legs := c.legs
	c.mu.Unlock()
	c.s.unbind(c.callID, c)
	for _, l := range legs {
		c.s.unbind(l.callID, c)
	}
}

// record counts the attempt and queues its CDR.
func (c *call) record(status int, side, reason, result string) {
	end := time.Now()
	c.mu.Lock()
	ring, answer := c.ringTime, c.answerTime
	if result != ResultAnswered {
		answer = time.Time{}
	}
	c.mu.Unlock()
	r := cdr.Record{
		CorrelationID: c.id, SIPCallID: c.callID, Source: c.caller.Extension, Destination: c.dialled,
		StartTime: c.start, RingTime: ring, AnswerTime: answer, EndTime: end,
		DurationMs: end.Sub(c.start).Milliseconds(), SIPNode: c.s.cfg.NodeID, MediaMode: "direct",
		FinalStatus: status, TerminationSide: side, FailureReason: reason,
	}
	if !answer.IsZero() {
		r.BillableMs = end.Sub(answer).Milliseconds()
	}
	c.s.m.Calls.WithLabelValues(result).Inc()
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
	return livestate.Call{
		ID: c.id, SIPCallID: c.callID, From: c.caller.Extension, To: c.dialled, State: state,
		Node: c.s.cfg.NodeID, Media: "direct", StartedAt: c.start, AnsweredAt: answered,
	}
}

func (c *call) publish() {
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
		reported = true
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
	err = dcs.WaitAnswer(l.ctx, sipgo.AnswerOptions{OnResponse: func(r *sip.Response) error {
		if r.StatusCode > 100 && r.StatusCode < 200 {
			select {
			case l.c.events <- legEvent{leg: l, kind: evRinging}:
			default:
			}
		}
		return nil
	}})
	if res := dcs.InviteResponse; err == nil || (res != nil && res.IsSuccess()) {
		if l.c.claim(l) {
			report(legEvent{leg: l, kind: evAnswered})
			return
		}
		// Answered after another fork won or the caller left: ACK, then BYE.
		l.bye()
		return
	}
	code := sip.StatusTemporarilyUnavailable
	var de *sipgo.ErrDialogResponse
	switch {
	case errors.As(err, &de):
		code = de.Res.StatusCode
	case errors.Is(err, sip.ErrTransactionTimeout):
		code = sip.StatusRequestTimeout
	}
	report(legEvent{leg: l, kind: evFailed, code: code})
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
	if err := sip.ParseUri(l.binding.ContactURI, &ruri); err != nil {
		return nil, err
	}
	req := sip.NewRequest(sip.INVITE, ruri)
	from := &sip.FromHeader{
		DisplayName: c.caller.ExtensionName,
		Address:     sip.Uri{Scheme: "sip", User: c.caller.Extension, Host: s.cfg.Domain},
		Params:      sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(16)}},
	}
	to := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: c.dialled, Host: s.cfg.Domain}}
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
	req.SetTransport("UDP")
	switch {
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
	c := ref.c
	c.mu.Lock()
	w, lateAck := c.winner, c.lateAck
	if ref.leg == nil && req.CSeq().SeqNo == c.inv.CSeq().SeqNo {
		c.lateAck = false
	}
	c.mu.Unlock()
	if w == nil {
		return
	}
	ct := ""
	if h := req.ContentType(); h != nil {
		ct = h.Value()
	}
	switch {
	case ref.leg == nil && req.CSeq().SeqNo == c.inv.CSeq().SeqNo:
		if err := c.dss.ReadAck(req, tx); err != nil {
			s.log.Debug("caller ACK rejected", "error", err)
			return
		}
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
	if ref.leg == nil {
		id, err := sip.DialogIDFromRequestUAS(req)
		return ref, err == nil && c.dss != nil && id == c.dss.ID && req.Source() == c.inv.Source()
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

// handleBye ends a connected call from either side.
func (s *Server) handleBye(req *sip.Request, tx sip.ServerTransaction) {
	ref, ok := s.matchDialog(req)
	if !ok {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	c := ref.c
	c.mu.Lock()
	w, connected := c.winner, c.connected
	c.mu.Unlock()
	switch {
	case !connected || w == nil:
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
	case ref.leg == nil:
		if err := c.dss.ReadBye(req, tx); err != nil {
			s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
			return
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
}
