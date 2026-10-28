// Click-to-call (spec integrations I-37): hello-control queues a request in
// Valkey (POST /api/v1/calls/originate) and one READY SIP node places it.
//
// Ring first: leg A is an INVITE to the extension's phone, with the
// destination as the caller so the user knows why it rings. When the phone
// answers (not on 180), the destination is dialled as if the extension had
// dialled it at the desk phone: every special number, class and permission
// check and rewrite of the ordinary call path applies, because the call IS an
// ordinary call whose caller is the phone that answered.
//
// That is how the two legs are bridged without a second call engine. The
// phone's answer to our INVITE is its SDP offer (the INVITE carries none), and
// a synthetic INVITE built from it, with the phone's dialog identifiers
// mirrored (Call-ID, tags, Contact, route set, source), enters the ordinary
// path through an originateTx in place of a server transaction. The engine
// believes the phone called it: it dials the destination, answers the "caller"
// with the destination's SDP, and handles BYE, re-INVITE, hold, recording and
// takeover exactly as for a call the phone placed. The originateTx turns what
// the engine sends the caller into what the phone's dialog needs: the answer
// becomes the ACK of the phone's 200, a final failure becomes a BYE.
package sip

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Originates is the queue of click-to-call requests and the place their
// state is posted; *livestate.Store satisfies it.
type Originates interface {
	TakeOriginate(ctx context.Context) (livestate.OriginateRequest, bool, error)
	PutOriginateState(ctx context.Context, st livestate.OriginateState) error
	OriginateCancelled(ctx context.Context, id string) (bool, error)
	ReleaseOriginateSetup(ctx context.Context, extension, id string) error
}

const (
	originatePoll       = 250 * time.Millisecond
	originateCancelPoll = 500 * time.Millisecond
	// defaultOriginateRing is how long leg A rings before the request fails
	// agent_no_answer.
	defaultOriginateRing = 30 * time.Second
)

// dialable is what a click-to-call destination may be: digits, +, * and #,
// never a SIP URI.
var dialable = regexp.MustCompile(`^[0-9+*#]{1,64}$`)

// originateLoop takes queued requests while this node is READY.
func (s *Server) originateLoop(ctx context.Context) {
	defer contain(s.log, "originate loop")
	t := time.NewTicker(originatePoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if st, _ := s.state(); st != cluster.Ready {
			continue
		}
		for {
			sctx, cancel := s.stateCtx()
			rq, ok, err := s.deps.Originates.TakeOriginate(sctx)
			cancel()
			if err != nil {
				s.log.Warn("originate queue unavailable", "error", err)
				break
			}
			if !ok {
				break
			}
			s.bg.Go(func() {
				defer contain(s.log, "originate")
				s.runOriginate(ctx, rq)
			})
		}
	}
}

// originated is one click-to-call on this node.
type originated struct {
	s     *Server
	rq    livestate.OriginateRequest
	start time.Time

	mu    sync.Mutex
	state string
	done  bool // a terminal state was posted
	corr  string
	// The answered phone's dialog (leg A) and the tag it knows us by.
	agent    *leg
	agentTag string
	c        *call
	tx       *originateTx
	// bridged is set once the destination answered; cancelRequested when the
	// client asked to stop; agentGone when the phone hung up first.
	bridged         bool
	cancelRequested bool
	agentGone       bool
	// stopRing ends leg A's forks while the phone rings.
	stopRing context.CancelFunc
	released bool
}

// set posts a state change: Valkey for the API, a call.originate.progress
// event for the stream, the metrics. A terminal state is final.
func (o *originated) set(state, reason string) {
	o.mu.Lock()
	if o.done {
		o.mu.Unlock()
		return
	}
	if state == livestate.OriginateEnded || state == livestate.OriginateFailed {
		o.done = true
	}
	o.state = state
	corr := o.corr
	o.mu.Unlock()
	s := o.s
	if state != livestate.OriginateRingingAgent {
		o.releaseSetup()
	}
	st := livestate.OriginateState{ID: o.rq.ID, State: state, Reason: reason, CorrelationID: corr, Node: s.cfg.NodeID, UpdatedAt: time.Now().UTC()}
	ctx, cancel := s.stateCtx()
	if err := s.deps.Originates.PutOriginateState(ctx, st); err != nil {
		s.log.Warn("could not post an originate state", "id", o.rq.ID, "state", state, "error", err)
	}
	cancel()
	o.emit(state, reason, corr)
	switch state {
	case livestate.OriginateConnected:
		s.m.Originates.WithLabelValues("connected").Inc()
		s.m.OriginateSetup.Observe(time.Since(o.start).Seconds())
	case livestate.OriginateFailed:
		s.m.Originates.WithLabelValues(reason).Inc()
	}
}

// releaseSetup frees the extension's one setup slot once the request is
// past setup.
func (o *originated) releaseSetup() {
	o.mu.Lock()
	done := o.released
	o.released = true
	o.mu.Unlock()
	if done {
		return
	}
	ctx, cancel := o.s.stateCtx()
	defer cancel()
	if err := o.s.deps.Originates.ReleaseOriginateSetup(ctx, o.rq.Extension, o.rq.ID); err != nil {
		o.s.log.Debug("could not release an originate setup slot", "id", o.rq.ID, "error", err)
	}
}

// emit sends the call.originate.progress event, about the extension that
// rings and from it to the destination.
func (o *originated) emit(state, reason, corr string) {
	s := o.s
	if s.deps.Events == nil {
		return
	}
	callID := corr
	if callID == "" {
		callID = o.rq.ID
	}
	d := events.Data{
		CallID: callID, Direction: cdr.DirectionInternal, Extension: o.rq.Extension,
		From:        events.Party{Number: o.rq.Extension, Name: s.extName(o.rq.Extension), Extension: o.rq.Extension},
		To:          events.Party{Number: o.rq.Destination},
		OriginateID: o.rq.ID, State: state, Reason: reason, StartedAt: o.start.UTC(),
	}
	if s.isExtension(o.rq.Destination) {
		d.To.Extension, d.To.Name = o.rq.Destination, s.extName(o.rq.Destination)
	}
	s.deps.Events.Emit(events.New(events.CallOriginateProgress, d, state, time.Now()))
}

func (o *originated) fail(reason string) { o.set(livestate.OriginateFailed, reason) }

// ringSeconds is how long leg A may ring.
func (o *originated) ring() time.Duration {
	if o.rq.RingSeconds > 0 {
		return time.Duration(o.rq.RingSeconds) * time.Second
	}
	if o.s.cfg.OriginateRing > 0 {
		return o.s.cfg.OriginateRing
	}
	return defaultOriginateRing
}

// runOriginate places one click-to-call and returns once the destination
// has been handed to the call engine (or the request failed).
func (s *Server) runOriginate(ctx context.Context, rq livestate.OriginateRequest) {
	o := &originated{s: s, rq: rq, start: time.Now()}
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("recovered panic", "in", "originate", "panic", fmt.Sprint(r))
			o.fail(livestate.OriginateNodeGone)
		}
	}()
	// The watcher outlives this function: it follows the call until the
	// request is over.
	go o.watchCancel(ctx)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	snap := s.deps.Snapshots.Current()
	if snap == nil {
		o.fail(livestate.OriginateNodeGone)
		return
	}
	ext, _ := snap.Extension(rq.Extension)
	ext.Number = rq.Extension
	if ext.Name == "" {
		ext.Name = s.displayName(snap, rq.Extension)
	}
	if !snap.HasExtension(rq.Extension) || !dialable.MatchString(rq.Destination) {
		o.fail(livestate.OriginateRouteDenied)
		return
	}
	if !o.permitted(snap, ext) {
		o.fail(livestate.OriginateRouteDenied)
		return
	}
	if ext.DND {
		o.fail(livestate.OriginateAgentDND)
		return
	}
	if s.busyExtensions()[rq.Extension] {
		o.fail(livestate.OriginateAgentBusy)
		return
	}
	bs, err := o.bindings(snap)
	if err != nil {
		o.fail(livestate.OriginateNodeGone)
		return
	}
	if len(bs) == 0 {
		o.fail(livestate.OriginateNoRegistration)
		return
	}
	o.set(livestate.OriginateRingingAgent, "")
	win, reason := o.ringAgent(ctx, bs)
	if win == nil {
		o.fail(reason)
		return
	}
	o.set(livestate.OriginateAgentAnswered, "")
	o.mu.Lock()
	cancelled := o.cancelRequested
	o.mu.Unlock()
	if cancelled {
		win.bye()
		o.fail(livestate.OriginateCancelled)
		return
	}
	res := win.session().InviteResponse
	offer := res.Body()
	if len(offer) == 0 {
		// A phone that answers a late-offer INVITE without an offer cannot be
		// bridged.
		win.bye()
		o.fail(livestate.OriginateAgentNoAnswer)
		return
	}
	o.mu.Lock()
	o.agent = win
	o.agentTag = tagParam(win.session().InviteRequest.From())
	o.mu.Unlock()

	dev := snap.DevicesForExtension(rq.Extension)
	var d snapshot.Device
	for _, c := range dev {
		if c.Username == win.binding.Device {
			d = c
		}
	}
	if d.Username == "" && len(dev) > 0 {
		d = dev[0]
	}
	req := o.syntheticInvite(win, res, offer, ext)
	tx := &originateTx{o: o, req: req, done: make(chan struct{}), acks: make(chan *sip.Request)}
	o.mu.Lock()
	o.tx = tx
	o.mu.Unlock()
	o.set(livestate.OriginateDialingTarget, "")
	// The call lives on its own from here; its end posts the final state.
	s.originateInvite(o, req, tx, snap, d)
}

// permitted reports whether the destination may be dialled by click-to-call:
// not a feature code, a paging group or an emergency number, something the
// routing accepts, and inside the administrator's restriction.
func (o *originated) permitted(snap *snapshot.Snapshot, ext snapshot.Extension) bool {
	s, dest := o.s, o.rq.Destination
	if _, ok := snap.PagingGroup(dest); ok {
		return false
	}
	if _, _, ok := matchFeatureCode(snap, dest); ok {
		return false
	}
	// What routeCaller resolves before routing: these are internal.
	if _, ok := snap.Conference(dest); ok {
		return true
	}
	if _, ok := snap.IVRMenuByNumber(dest); ok {
		return true
	}
	if _, ok := snap.QueueByNumber(dest); ok {
		return true
	}
	if _, _, ok := snap.RingGroup(dest); ok {
		return true
	}
	dec := s.decide(snap.Routing(), routing.Call{FromExtension: ext.Number, Number: dest, CallerID: ext.Number, At: time.Now()})
	switch {
	case dec.Emergency, dec.Kind == routing.KindReject:
		return false
	case dec.Kind == routing.KindOutbound && o.rq.Restrict:
		return slices.ContainsFunc(o.rq.Prefixes, func(p string) bool {
			return p != "" && (strings.HasPrefix(dest, p) || strings.HasPrefix(dec.Number, p))
		})
	}
	return true
}

// bindings are the live contacts leg A rings: every contact of the
// extension's devices, or those of the one device the request names.
func (o *originated) bindings(snap *snapshot.Snapshot) ([]livestate.Binding, error) {
	s := o.s
	var out []livestate.Binding
	for _, d := range snap.DevicesForExtension(o.rq.Extension) {
		if o.rq.DeviceID != "" && d.Username != o.rq.DeviceID {
			continue
		}
		ctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
		cancel()
		if err != nil {
			return nil, err
		}
		out = append(out, bs...)
	}
	return out, nil
}

// watchCancel hangs the request up when the client asks to.
func (o *originated) watchCancel(ctx context.Context) {
	t := time.NewTicker(originateCancelPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		o.mu.Lock()
		done := o.done
		o.mu.Unlock()
		if done {
			return
		}
		sctx, cancel := o.s.stateCtx()
		yes, err := o.s.deps.Originates.OriginateCancelled(sctx, o.rq.ID)
		cancel()
		if err == nil && yes {
			o.hangup()
			return
		}
	}
}

// hangup stops the request wherever it is: leg A ringing, the destination
// ringing, or the call connected.
func (o *originated) hangup() {
	o.mu.Lock()
	o.cancelRequested = true
	stop, tx, c, agent, bridged := o.stopRing, o.tx, o.c, o.agent, o.bridged
	o.mu.Unlock()
	switch {
	case bridged && c != nil:
		agent.bye()
		c.hangup(cdr.SideCaller)
	case tx != nil:
		// The destination is being dialled: the same as the phone giving up.
		agent.bye()
		tx.cancelled()
	case stop != nil:
		stop()
	}
}

// agentHungUp is the phone's BYE while the destination was still ringing.
func (o *originated) agentHungUp() {
	o.mu.Lock()
	o.agentGone = true
	tx := o.tx
	o.mu.Unlock()
	if tx != nil {
		tx.cancelled()
	}
}

// byeAgent ends the phone's dialog (the engine's BYE to its caller).
func (o *originated) byeAgent() {
	o.mu.Lock()
	a := o.agent
	o.mu.Unlock()
	a.bye()
}

// callEnded is the call's CDR being written: the request is over.
func (o *originated) callEnded(result string) {
	o.mu.Lock()
	bridged, cancelled, gone := o.bridged, o.cancelRequested, o.agentGone
	o.mu.Unlock()
	switch {
	case bridged:
		o.set(livestate.OriginateEnded, "")
	case cancelled || gone || result == ResultCancelled:
		o.fail(livestate.OriginateCancelled)
	case result == ResultBusy:
		o.fail(livestate.OriginateTargetBusy)
	case result == ResultNoAnswer || result == ResultUnavailable:
		o.fail(livestate.OriginateTargetNoAnswer)
	default:
		o.fail(livestate.OriginateRouteDenied)
	}
}

// targetFailed is the call engine refusing the "caller" with a final failure
// before the destination answered: the phone that was rung first is hung up.
func (o *originated) targetFailed(code int) {
	reason := livestate.OriginateRouteDenied
	switch {
	case o.cancelledByUser():
		reason = livestate.OriginateCancelled
	case isBusy(code):
		reason = livestate.OriginateTargetBusy
	case code == sip.StatusRequestTimeout || code == sip.StatusTemporarilyUnavailable || code == sip.StatusRequestTerminated:
		reason = livestate.OriginateTargetNoAnswer
	}
	o.fail(reason)
	go func() {
		defer contain(o.s.log, "originate agent BYE")
		o.byeAgent()
	}()
}

func (o *originated) cancelledByUser() bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.cancelRequested || o.agentGone
}

// agentLeg is one INVITE of leg A, to one contact.
type agentLeg struct {
	l      *leg
	cancel context.CancelFunc
}

type agentResult struct {
	a    *agentLeg
	ok   bool
	code int
}

// ringAgent rings every contact and returns the leg that answered first, or
// the failure reason.
func (o *originated) ringAgent(ctx context.Context, bs []livestate.Binding) (*leg, string) {
	s := o.s
	ringCtx, stopRing := context.WithCancel(ctx)
	defer stopRing()
	o.mu.Lock()
	o.stopRing = stopRing
	o.mu.Unlock()
	res := make(chan agentResult, len(bs))
	var legs []*agentLeg
	for _, b := range bs {
		lctx, lcancel := context.WithCancel(ringCtx)
		a := &agentLeg{l: &leg{c: &call{s: s}, binding: b}, cancel: lcancel}
		legs = append(legs, a)
		go func() {
			defer contain(s.log, "originate agent leg")
			ok, code := o.dialAgent(lctx, a)
			res <- agentResult{a: a, ok: ok, code: code}
		}()
	}
	timer := time.NewTimer(o.ring())
	defer timer.Stop()
	var winner *leg
	pending, busy := len(legs), 0
	cancelOthers := func(except *agentLeg) {
		for _, a := range legs {
			if a != except {
				a.cancel()
			}
		}
	}
	ringDone := ringCtx.Done()
	for pending > 0 {
		select {
		case r := <-res:
			pending--
			switch {
			case r.ok && winner == nil && !o.cancelledByUser():
				winner = r.a.l
				cancelOthers(r.a)
			case r.ok:
				// Answered after another contact won or the client left.
				r.a.l.bye()
			case isBusy(r.code):
				busy++
			}
		case <-timer.C:
			cancelOthers(nil)
		case <-ringDone:
			ringDone = nil
			cancelOthers(nil)
		}
	}
	for _, a := range legs {
		a.cancel()
	}
	switch {
	case winner != nil:
		return winner, ""
	case o.cancelledByUser():
		return nil, livestate.OriginateCancelled
	case busy == len(legs):
		return nil, livestate.OriginateAgentBusy
	}
	return nil, livestate.OriginateAgentNoAnswer
}

// dialAgent sends one late-offer INVITE and waits for its answer; ok means a
// 2xx whose dialog is in a.l. code is the failure's final status.
func (o *originated) dialAgent(ctx context.Context, a *agentLeg) (ok bool, code int) {
	s := o.s
	req, err := o.agentInvite(a.l.binding)
	if err != nil {
		return false, sip.StatusTemporarilyUnavailable
	}
	dcs, err := s.uac.WriteInvite(ctx, req)
	if err != nil {
		s.log.Debug("originate INVITE failed", "contact", a.l.binding.ContactURI, "error", err)
		return false, sip.StatusServiceUnavailable
	}
	a.l.mu.Lock()
	a.l.dcs = dcs
	a.l.mu.Unlock()
	err = dcs.WaitAnswer(ctx, sipgo.AnswerOptions{})
	if res := dcs.InviteResponse; err == nil || (res != nil && res.IsSuccess()) {
		return true, 0
	}
	var de *sipgo.ErrDialogResponse
	if errors.As(err, &de) {
		return false, de.Res.StatusCode
	}
	return false, sip.StatusRequestTimeout
}

// displayName is an extension's name: its configured one, else the name its
// first device carries; empty for a number that is no extension.
func (s *Server) displayName(snap *snapshot.Snapshot, ext string) string {
	if snap == nil {
		return ""
	}
	if e, ok := snap.Extension(ext); ok && e.Name != "" {
		return e.Name
	}
	if ds := snap.DevicesForExtension(ext); len(ds) > 0 {
		return ds[0].ExtensionName
	}
	return ""
}

// agentInvite builds leg A's INVITE: no SDP (the phone's 200 carries the
// offer), "Call to <target>" as the caller and, when asked and allowed, the
// vendor's auto-answer headers.
func (o *originated) agentInvite(b livestate.Binding) (*sip.Request, error) {
	s := o.s
	var ruri sip.Uri
	if err := sip.ParseUri(b.ContactURI, &ruri); err != nil {
		return nil, err
	}
	dest := o.rq.Destination
	name := dest
	if n := s.displayName(s.deps.Snapshots.Current(), dest); n != "" {
		name = n
	}
	name = strings.NewReplacer(`"`, ``, `\`, ``, "\r", ``, "\n", ``).Replace("Call to " + name)
	req := sip.NewRequest(sip.INVITE, ruri)
	callID := sip.CallIDHeader(newID())
	maxFwd := sip.MaxForwardsHeader(70)
	req.AppendHeader(&sip.FromHeader{
		DisplayName: name,
		Address:     sip.Uri{Scheme: "sip", User: dest, Host: s.cfg.Domain},
		Params:      sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(16)}},
	})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: o.rq.Extension, Host: s.cfg.Domain}})
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	req.AppendHeader(&maxFwd)
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Allow", allow))
	if o.rq.AutoAnswer {
		for _, h := range autoAnswerHeaders(s.cfg.Domain, b.UserAgent) {
			req.AppendHeader(h)
		}
	}
	req.SetTransport("UDP")
	if err := s.routeToBinding(req, b); err != nil {
		return nil, fmt.Errorf("route to contact: %w", err)
	}
	return req, nil
}

// syntheticInvite is the INVITE the answered phone "sent": its offer, from
// the extension to the destination, carrying the phone's dialog as the
// engine will see it. The From tag is the phone's own and the Call-ID,
// CSeq, Contact, route set and source are those of the dialog leg A
// established; the To tag (ours) is fixed by attach.
func (o *originated) syntheticInvite(win *leg, res *sip.Response, offer []byte, ext snapshot.Extension) *sip.Request {
	s := o.s
	inv := win.session().InviteRequest
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: o.rq.Destination, Host: s.cfg.Domain})
	callID := sip.CallIDHeader(inv.CallID().Value())
	maxFwd := sip.MaxForwardsHeader(70)
	host, port, _ := sip.ParseAddr(res.Source())
	req.AppendHeader(&sip.ViaHeader{
		ProtocolName: "SIP", ProtocolVersion: "2.0", Transport: "UDP", Host: host, Port: port,
		Params: sip.HeaderParams{{K: "branch", V: sip.GenerateBranch()}},
	})
	req.AppendHeader(&sip.FromHeader{
		DisplayName: ext.Name,
		Address:     sip.Uri{Scheme: "sip", User: ext.Number, Host: s.cfg.Domain},
		Params:      sip.HeaderParams{{K: "tag", V: tagParam(res.To())}},
	})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: o.rq.Destination, Host: s.cfg.Domain}})
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: inv.CSeq().SeqNo, MethodName: sip.INVITE})
	req.AppendHeader(&maxFwd)
	if ct := res.Contact(); ct != nil {
		req.AppendHeader(sip.HeaderClone(ct))
	} else {
		req.AppendHeader(&sip.ContactHeader{Address: *inv.Recipient.Clone()})
	}
	// The UAC's route set is the 200's Record-Route in reverse; as the
	// engine's UAS dialog reads it from the request, in the order received.
	rrs := res.GetHeaders("Record-Route")
	for i := len(rrs) - 1; i >= 0; i-- {
		req.AppendHeader(sip.NewHeader("Record-Route", rrs[i].Value()))
	}
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(offer)
	req.SetTransport("UDP")
	req.SetSource(res.Source())
	return req
}

// originateInvite runs the synthetic INVITE through the ordinary call path
// as a call the answered phone placed.
func (s *Server) originateInvite(o *originated, req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, dev snapshot.Device) {
	c := s.newCall(req)
	c.callerNum, c.callerName, c.callerDevice = dev.Extension, dev.ExtensionName, dev.Username
	if dev.Extension == "" {
		c.callerNum, c.callerName = o.rq.Extension, s.extName(o.rq.Extension)
	}
	c.orig = o
	o.mu.Lock()
	o.c, o.corr = c, c.id
	o.mu.Unlock()
	s.routeCaller(c, req, tx, snap, dev)
}

// --- the transaction the engine answers ---------------------------------------

// originateTx stands in for the caller INVITE's server transaction. What the
// engine responds becomes what the phone's dialog needs.
type originateTx struct {
	o   *originated
	req *sip.Request

	mu       sync.Mutex
	dss      *sipgo.DialogServerSession
	closed   bool
	err      error
	cancelFn []sip.FnTxCancel
	termFn   []sip.FnTxTerminate
	done     chan struct{}
	acks     chan *sip.Request
}

var _ sip.ServerTransaction = (*originateTx)(nil)

// attach is called by call.begin with the dialog sipgo built from the
// synthetic INVITE: its To tag must be the one the phone knows us by, so the
// dialog identifiers match the real dialog's in both directions.
func (t *originateTx) attach(dss *sipgo.DialogServerSession) {
	to := dss.InviteRequest.To()
	to.Params.Remove("tag")
	to.Params.Add("tag", t.o.agentTag)
	if id, err := sip.DialogIDFromRequestUAS(dss.InviteRequest); err == nil {
		dss.ID = id
	}
	t.mu.Lock()
	t.dss = dss
	t.mu.Unlock()
}

func (t *originateTx) Terminate() { t.finish(nil) }

func (t *originateTx) finish(err error) {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed, t.err = true, err
	fns := t.termFn
	t.termFn = nil
	close(t.done)
	t.mu.Unlock()
	for _, f := range fns {
		f("originate", err)
	}
}

func (t *originateTx) OnTerminate(f sip.FnTxTerminate) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.termFn = append(t.termFn, f)
	return true
}

func (t *originateTx) Done() <-chan struct{} { return t.done }

func (t *originateTx) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

func (t *originateTx) Acks() <-chan *sip.Request { return t.acks }

func (t *originateTx) OnCancel(f sip.FnTxCancel) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return false
	}
	t.cancelFn = append(t.cancelFn, f)
	return true
}

// cancelled is the phone giving up (or the client hanging up) while the
// destination rings: the engine sees the caller's CANCEL.
func (t *originateTx) cancelled() {
	t.mu.Lock()
	fns := t.cancelFn
	t.mu.Unlock()
	for _, f := range fns {
		f(t.req)
	}
	t.finish(sip.ErrTransactionCanceled)
}

// Respond turns the engine's response to its "caller" into leg A's side:
// provisional responses are not forwarded (the phone is already talking), the
// answer is the ACK of the phone's 200, a failure ends the phone's call.
func (t *originateTx) Respond(res *sip.Response) error {
	o := t.o
	switch {
	case res.StatusCode < 200:
		return nil
	case res.IsSuccess():
		o.mu.Lock()
		first := !o.bridged
		o.bridged = true
		a := o.agent
		o.mu.Unlock()
		a.ack(res.Body(), "application/sdp")
		if first {
			o.set(livestate.OriginateConnected, "")
		}
		t.mu.Lock()
		dss := t.dss
		t.mu.Unlock()
		if dss != nil {
			// The phone's ACK is ours to write; the engine's dialog waits to
			// be told it arrived.
			ack := sip.NewRequest(sip.ACK, *t.req.Recipient.Clone())
			ack.AppendHeader(&sip.CSeqHeader{SeqNo: t.req.CSeq().SeqNo, MethodName: sip.ACK})
			go func() { _ = dss.ReadAck(ack, t) }()
		}
		return nil
	default:
		o.targetFailed(res.StatusCode)
		t.finish(nil)
		return nil
	}
}
