package sip

// Fax calls (pbx-gaps S-15, S-16, S-17). hello-sip answers or places the call
// and relays its media, G.711 audio or T.38 UDPTL, to a UDP port of the
// hello-fax service, which runs T.30. hello-sip never links spandsp: it owns
// the signalling (the answer, the T.38 re-INVITE and its 488 fallback), the
// media relay, the fax table row, the PDF in object storage and the CDR.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/fax"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/emiago/sipgo/sip"
)

// FaxDeps wires the fax feature into a SIP node.
type FaxDeps struct {
	// Enabled is HELLO_FAX_ENABLED; off, fax calls are answered 488.
	Enabled bool
	Client  *fax.Client
	Store   fax.Store
	Objects fax.Objects
}

// haPhaseFax is the replicated phase of an answered call that moved to the fax
// engine; no node can resume it.
const haPhaseFax = "fax"

// How often a running fax session is polled, and how many failed polls end it.
const (
	faxPollEvery  = 500 * time.Millisecond
	faxPollErrors = 6
	faxStartWait  = 20 * time.Second
	faxStoreWait  = 30 * time.Second
)

// faxCall is the fax state of one call.
type faxCall struct {
	outbound bool
	rowID    int64
	dest     snapshot.FaxDestination
	peer     string // the other party's number
	local    string // the DID, or the caller ID sent
	host     string // our media address (the anchor host)
	relay    *media.Relay
	// carrier and engine name the relay legs facing the carrier and
	// hello-fax: the caller's leg faces the carrier on an inbound call, the
	// callee's on a call we placed.
	carrier, engine string
	w               *leg // the carrier's dialog on an outbound call
	pdf             []byte
	t38OK           bool // the trunk allows T.38 (fax_mode t38)

	mu     sync.Mutex
	cl     *fax.Client
	sid    string
	mode   string
	pt     uint8
	t38Try atomic.Bool
	done   atomic.Bool // finish has run
	lost   atomic.Bool // the call ended
}

func (c *call) faxState() *faxCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.fx
}

func (fx *faxCall) curMode() string {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.mode
}

func (fx *faxCall) client() (*fax.Client, string) {
	fx.mu.Lock()
	defer fx.mu.Unlock()
	return fx.cl, fx.sid
}

func (s *Server) faxOn() *FaxDeps {
	if f := s.deps.Fax; f != nil && f.Enabled && f.Client != nil && f.Store != nil && f.Objects != nil {
		return f
	}
	return nil
}

// faxRefusal is why a fax call cannot be taken: the SIP status and the reason
// for the trace and the CDR.
type faxRefusal struct {
	code int
	why  string
}

func (r *faxRefusal) Error() string { return r.why }

// startFax answers a call routed to the fax destination name (an inbound
// route, 488 when the engine or the trunk cannot take it).
func (c *call) startFax(tx sip.ServerTransaction, snap *snapshot.Snapshot, name string) {
	s := c.s
	if tx != nil && !c.begin(c.inv, tx) {
		return
	}
	fx, ref := c.faxAnswerInbound(snap, name)
	if ref != nil {
		c.addTrace("Fax refused: " + ref.why)
		s.m.FaxTotal.WithLabelValues(fax.DirInbound, "refused").Inc()
		c.respondA(ref.code, statusText(ref.code))
		c.end(ref.code, cdr.SideSystem, ref.why, ResultFailed)
		return
	}
	go c.faxRun(fx)
}

// ivrFaxExit sends a call an IVR menu answered to the fax engine: the call
// is moved to G.711 with a re-INVITE and the menu ends. next and done follow
// ivrLeave: a destination that cannot take the call lets the menu say so.
func (c *call) ivrFaxExit(snap *snapshot.Snapshot, name string) (next string, done bool) {
	fx, ref := c.faxMoveAnswered(snap, name)
	if ref != nil {
		c.addTrace("Fax refused: " + ref.why)
		c.s.m.FaxTotal.WithLabelValues(fax.DirInbound, "refused").Inc()
		return "", false
	}
	go c.faxRun(fx)
	return "", true
}

// faxPlan checks what a fax call needs before any media is set up.
func (c *call) faxPlan(snap *snapshot.Snapshot, name string, answered bool) (fd *FaxDeps, dest snapshot.FaxDestination, t38Offer bool, g711 media.AudioSDP, ref *faxRefusal) {
	s := c.s
	if fd = s.faxOn(); fd == nil {
		return nil, dest, false, g711, &faxRefusal{sip.StatusNotAcceptableHere, "fax is not enabled on this system"}
	}
	d, ok := snap.FaxDestination(name)
	if !ok {
		return nil, dest, false, g711, &faxRefusal{sip.StatusNotFound, "fax destination " + name + " does not exist"}
	}
	c.mu.Lock()
	mode := c.faxMode
	c.mu.Unlock()
	if mode == fax.ModeOff {
		return nil, dest, false, g711, &faxRefusal{sip.StatusNotAcceptableHere, "fax is off on trunk " + c.trunkName}
	}
	var body []byte
	if c.inv != nil {
		body = c.inv.Body()
	}
	if !answered && mode == fax.ModeT38 && media.OffersT38(body) {
		t, _ := media.ParseT38SDP(body)
		if !t.T38Acceptable() {
			return nil, dest, false, g711, &faxRefusal{sip.StatusNotAcceptableHere, "the T.38 offer is not one Hello serves (version 0, transferred TCF, UDP redundancy)"}
		}
		return fd, d, true, g711, nil
	}
	off, err := media.ParseAudioSDP(body)
	if err != nil {
		return nil, dest, false, g711, &faxRefusal{sip.StatusNotAcceptableHere, "a fax call needs a G.711 audio offer or a T.38 offer on a T.38 trunk"}
	}
	g711 = off.Filter([]string{"PCMU", "PCMA"})
	if len(g711.Codecs) == 0 {
		return nil, dest, false, g711, &faxRefusal{sip.StatusNotAcceptableHere, "a fax call needs G.711 (PCMU or PCMA)"}
	}
	return fd, d, false, g711, nil
}

// faxAnswerInbound sets up the relay, opens the hello-fax session and
// answers the caller. It returns the refusal when the call cannot be taken.
func (c *call) faxAnswerInbound(snap *snapshot.Snapshot, name string) (*faxCall, *faxRefusal) {
	s := c.s
	fd, dest, t38, g711, ref := c.faxPlan(snap, name, false)
	if ref != nil {
		return nil, ref
	}
	offer := c.inv.Body()
	var target *net.UDPAddr
	mode, pt := fax.ModePassthrough, uint8(0)
	if t38 {
		mode = fax.ModeT38
		t, _ := media.ParseT38SDP(offer)
		target = udpAddr(t.Address, t.Port)
	} else {
		off, _ := media.ParseAudioSDP(offer)
		target = udpAddr(off.Address, off.Port)
		pt = g711.Codecs[0].PT
	}
	if target == nil {
		return nil, &faxRefusal{sip.StatusNotAcceptableHere, "the offer names no usable media address"}
	}
	relay, err := media.NewRelay(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		return nil, &faxRefusal{sip.StatusServiceUnavailable, "media ports: " + err.Error()}
	}
	portCarrier, err1 := relay.AddLeg(legCaller)
	_, err2 := relay.AddLeg(legCallee)
	if err := errors.Join(err1, err2); err != nil {
		relay.Close()
		return nil, &faxRefusal{sip.StatusServiceUnavailable, "media ports: " + err.Error()}
	}
	host := s.anchorHostOr(s.advHost)
	fx := &faxCall{dest: dest, peer: c.callerNum, local: c.dialled, host: host, relay: relay,
		carrier: legCaller, engine: legCallee, t38OK: c.faxModeIs(fax.ModeT38), pt: pt}
	relay.SetTarget(legCaller, target)
	allowSignalSource(relay, legCaller, c.inv.Source())
	if t38 {
		relay.SetUDPTL(true)
	} else {
		relay.SetCodecs(legCaller, g711.Codecs[:1], 0)
		relay.SetCodecs(legCallee, g711.Codecs[:1], 0)
	}
	if err := c.faxOpenSession(fd, fx, mode, pt); err != nil {
		relay.Close()
		return nil, &faxRefusal{sip.StatusNotAcceptableHere, "the fax engine is unavailable: " + err.Error()}
	}
	id, err := fd.Store.BeginReceive(context.Background(), fax.Receive{
		PeerNumber: c.callerNum, LocalNumber: c.dialled, DestinationID: dest.ID, OwnerExtension: dest.Extension, CorrelationID: c.id})
	if err != nil {
		c.faxStopSession(fx)
		relay.Close()
		return nil, &faxRefusal{sip.StatusServiceUnavailable, "the fax could not be recorded: " + err.Error()}
	}
	fx.rowID = id
	var body []byte
	if t38 {
		body = media.BuildT38SDP(host, portCarrier)
	} else {
		body = media.BuildSDP(host, portCarrier, g711.Codecs[:1], nil, "sendrecv")
	}
	c.mu.Lock()
	c.fx, c.kind = fx, cdr.KindFax
	c.anchored, c.anchorReason, c.relay, c.anchorHost, c.mediaMode = true, AnchorFax, relay, host, "anchored"
	c.mu.Unlock()
	c.observeRelay(relay)
	if m := s.deps.Media; m != nil {
		m.NoteStart()
		relay.OnFail(func(string) { m.AnchorFailures.Inc() })
	}
	c.faxWatchTones(fx) // before Start: the relay reads its tap without a lock
	relay.Start()
	if !c.faxSendAnswer(body) {
		c.faxStopSession(fx)
		c.faxFinish(fx, fax.SessionInfo{Text: "the caller hung up before the answer was acknowledged"}, false)
		return nil, &faxRefusal{sip.StatusRequestTerminated, "the call ended before it was answered"}
	}
	c.addTrace(fmt.Sprintf("Fax %s: answered on %s, %s", dest.Name, mode, c.callerNum))
	return fx, nil
}

// faxSendAnswer sends the 200 with body; false when the call was cancelled or
// the caller never acknowledged. It follows soloAnswer.
func (c *call) faxSendAnswer(body []byte) bool {
	s := c.s
	c.mu.Lock()
	cancelled := c.isCancelled
	aborted := closed(c.aborted)
	c.connected = !cancelled && !aborted
	if c.connected {
		c.answerTime = time.Now()
		c.setupClosed = true
	}
	c.mu.Unlock()
	if cancelled || aborted {
		return false
	}
	if h := s.answerHook.Load(); h != nil {
		(*h)()
	}
	c.addTrace("Call established")
	c.publish()
	res := sip.NewResponseFromRequest(c.dss.InviteRequest, sip.StatusOK, "OK", body)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res.AppendHeader(sip.NewHeader("Allow", allow))
	s.m.response(sip.StatusOK)
	if err := c.dss.WriteResponse(res); err != nil {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		if c.dss.LoadState() != sip.DialogStateEnded {
			c.byeA()
		}
		return false
	}
	c.mu.Lock()
	if !c.ended {
		c.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, c.expire)
	}
	if r := c.dss.InviteResponse; r != nil {
		c.vmTag = tagParam(r.To())
	}
	c.mu.Unlock()
	return true
}

// faxMoveAnswered turns a call that is already answered (an IVR menu's) into a
// fax call: the caller is re-INVITEd onto G.711 and the engine takes the
// relay's other leg.
func (c *call) faxMoveAnswered(snap *snapshot.Snapshot, name string) (*faxCall, *faxRefusal) {
	s := c.s
	fd, dest, _, g711, ref := c.faxPlan(snap, name, true)
	if ref != nil {
		return nil, ref
	}
	relay := c.anchorRelay()
	if relay == nil || c.dss == nil {
		return nil, &faxRefusal{sip.StatusNotAcceptableHere, "the call has no media anchor to move to the fax engine"}
	}
	c.mu.Lock()
	host := c.anchorHost
	c.mu.Unlock()
	pt := g711.Codecs[0].PT
	fx := &faxCall{dest: dest, peer: c.callerNum, local: c.dialled, host: host, relay: relay,
		carrier: legCaller, engine: legCallee, t38OK: c.faxModeIs(fax.ModeT38), pt: pt}
	body := media.BuildSDP(host, relay.LegPort(legCaller), g711.Codecs[:1], nil, "sendrecv")
	ans, ok := reinviteA(c.dss, body)
	if !ok {
		return nil, &faxRefusal{sip.StatusNotAcceptableHere, "the caller did not accept G.711 for the fax"}
	}
	a, err := media.ParseAudioSDP(ans)
	if err != nil || a.Port == 0 {
		return nil, &faxRefusal{sip.StatusNotAcceptableHere, "the caller refused the audio stream for the fax"}
	}
	relay.SetTarget(legCaller, udpAddr(a.Address, a.Port))
	relay.SetCodecs(legCaller, g711.Codecs[:1], 0)
	relay.SetCodecs(legCallee, g711.Codecs[:1], 0)
	if err := c.faxOpenSession(fd, fx, fax.ModePassthrough, pt); err != nil {
		return nil, &faxRefusal{sip.StatusNotAcceptableHere, "the fax engine is unavailable: " + err.Error()}
	}
	id, err := fd.Store.BeginReceive(context.Background(), fax.Receive{
		PeerNumber: c.callerNum, LocalNumber: c.dialled, DestinationID: dest.ID, OwnerExtension: dest.Extension, CorrelationID: c.id})
	if err != nil {
		c.faxStopSession(fx)
		return nil, &faxRefusal{sip.StatusServiceUnavailable, "the fax could not be recorded: " + err.Error()}
	}
	fx.rowID = id
	c.mu.Lock()
	c.fx, c.kind = fx, cdr.KindFax
	c.mu.Unlock()
	s.m.IVRTotal.WithLabelValues("fax").Inc()
	// The menu is no longer what a surviving node should restart: a fax call
	// is not recoverable (see docs/fax.md), so its replicated state ends.
	c.noteHAState(haPhaseFax, "")
	c.addTrace(fmt.Sprintf("Fax %s: the IVR menu moved the call to the fax engine", dest.Name))
	return fx, nil
}

func (c *call) faxModeIs(m string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.faxMode == m
}

// faxOpenSession starts the hello-fax session for the call and aims the
// relay's engine leg at it.
func (c *call) faxOpenSession(fd *FaxDeps, fx *faxCall, mode string, pt uint8) error {
	ctx, cancel := context.WithTimeout(context.Background(), faxStartWait)
	defer cancel()
	m := fax.ModePassthrough
	wire := "g711"
	if mode == fax.ModeT38 {
		m, wire = fax.ModeT38, "t38"
	}
	req := fax.SessionRequest{
		Direction: fax.SessionReceive, Mode: wire, Ident: fax.Ident(fx.local), PT: pt,
		Remote: net.JoinHostPort(fx.host, fmt.Sprint(fx.relay.LegPort(fx.engine))),
	}
	var pdf []byte
	if fx.outbound {
		req.Direction, req.AllowT38 = fax.SessionSend, fx.t38OK
		pdf = fx.pdf
	}
	info, err := fd.Client.Start(ctx, req, pdf)
	if err != nil {
		return err
	}
	cl := fd.Client.WithHost(info.Host)
	ip := net.ParseIP(info.Host)
	if ip == nil {
		// A pod that names itself by name: resolve it once.
		ips, lerr := net.DefaultResolver.LookupIPAddr(ctx, info.Host)
		if lerr != nil || len(ips) == 0 {
			_ = fd.Client.Forget(ctx, info.ID)
			return fmt.Errorf("cannot resolve %s", info.Host)
		}
		ip = ips[0].IP
	}
	fx.relay.SetTarget(fx.engine, &net.UDPAddr{IP: ip, Port: info.Port})
	fx.mu.Lock()
	fx.cl, fx.sid, fx.mode, fx.pt = cl, info.ID, m, pt
	fx.mu.Unlock()
	return nil
}

func (c *call) faxStopSession(fx *faxCall) {
	cl, sid := fx.client()
	if cl == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	_, _ = cl.Stop(ctx, sid)
	_ = cl.Forget(ctx, sid)
}

// faxWatchTones listens for CNG on the carrier's audio and, on a T.38 trunk,
// moves the call to T.38 when it hears it (pbx-gaps S-16).
func (c *call) faxWatchTones(fx *faxCall) {
	if !fx.t38OK || fx.outbound {
		return
	}
	det := media.NewFaxToneDetector()
	fx.relay.OnPacket(func(leg string, pkt *media.RTPPacket) {
		if leg != fx.carrier || fx.t38Try.Load() || fx.relay.UDPTL() {
			return
		}
		if det.Feed(pkt.PayloadType, pkt.Payload) == media.ToneCNG {
			go c.faxSwitchT38("CNG heard")
		}
	})
}

// faxSwitchT38 re-INVITEs the call onto T.38. A refusal (488, or a stream
// answered with port 0) leaves the call on G.711 passthrough.
func (c *call) faxSwitchT38(why string) {
	fx := c.faxState()
	if fx == nil || !fx.t38OK || fx.done.Load() || !fx.t38Try.CompareAndSwap(false, true) {
		return
	}
	defer contain(c.s.log, "fax T.38 switch")
	offer := media.BuildT38SDP(fx.host, fx.relay.LegPort(fx.carrier))
	var ans []byte
	var ok bool
	if fx.outbound {
		w := fx.w
		ans, ok = reinvite(w.session(), offer)
	} else {
		ans, ok = reinviteA(c.dss, offer)
	}
	t, isT38 := media.ParseT38SDP(ans)
	if !ok || !isT38 || !t.T38Acceptable() {
		c.addTrace(fmt.Sprintf("T.38 (%s): the carrier did not take it; the fax continues on G.711", why))
		return
	}
	c.faxAdoptT38(fx, t)
	c.addTrace(fmt.Sprintf("T.38 (%s): the call moved to T.38", why))
}

// faxAdoptT38 points the relay at the carrier's UDPTL port and moves the
// engine session to a T.38 terminal.
func (c *call) faxAdoptT38(fx *faxCall, t media.T38SDP) {
	fx.relay.SetTarget(fx.carrier, udpAddr(t.Address, t.Port))
	fx.relay.SetUDPTL(true)
	cl, sid := fx.client()
	ctx, cancel := context.WithTimeout(context.Background(), faxStartWait)
	defer cancel()
	if _, err := cl.SwitchT38(ctx, sid); err != nil {
		c.s.log.Warn("fax: the engine did not switch to T.38", "correlation_id", c.id, "error", err)
	}
	fx.mu.Lock()
	fx.mode = fax.ModeT38
	fx.mu.Unlock()
}

// faxInDialog answers a re-INVITE or UPDATE on a fax call: a T.38 offer on a
// T.38 trunk switches the call, anything else is answered with the current
// media description.
func (c *call) faxInDialog(fx *faxCall, req *sip.Request, tx sip.ServerTransaction) {
	s := c.s
	body := req.Body()
	reply := func(sdp []byte) {
		res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", sdp)
		if len(sdp) > 0 {
			res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		}
		res.AppendHeader(sip.HeaderClone(&s.contact))
		res.AppendHeader(sip.NewHeader("Allow", allow))
		s.m.response(sip.StatusOK)
		s.send(tx, res)
	}
	current := func() []byte {
		port := fx.relay.LegPort(fx.carrier)
		if fx.curMode() == fax.ModeT38 {
			return media.BuildT38SDP(fx.host, port)
		}
		return media.BuildAudioSDP(fx.host, port, fx.pt, 0, 0)
	}
	if t, ok := media.ParseT38SDP(body); ok {
		if !fx.t38OK || !t.T38Acceptable() || fx.done.Load() {
			s.respond(tx, req, sip.StatusNotAcceptableHere, "Not Acceptable Here")
			return
		}
		fx.t38Try.Store(true)
		reply(media.BuildT38SDP(fx.host, fx.relay.LegPort(fx.carrier)))
		c.faxAdoptT38(fx, t)
		c.addTrace("T.38 (the carrier asked): the call moved to T.38")
		return
	}
	if len(body) > 0 && fx.curMode() != fax.ModeT38 {
		if off, err := media.ParseAudioSDP(body); err == nil {
			fx.relay.SetTarget(fx.carrier, udpAddr(off.Address, off.Port))
		}
	}
	reply(current())
}

// faxRenew keeps the lease on an outbound fax; false means it was cancelled.
func (c *call) faxRenew(fx *faxCall) bool {
	fd := c.s.deps.Fax
	if fd == nil || fx.rowID == 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(context.Background(), callbackDBWait)
	defer cancel()
	ok, err := fd.Store.Renew(ctx, fx.rowID, c.s.cfg.NodeID, fax.SendLease)
	if err != nil {
		c.s.log.Warn("fax lease not renewed", "fax_id", fx.rowID, "error", err)
		return true
	}
	return ok
}

// faxRun watches the engine session until T.30 ends or the call does, then
// stores the result.
func (c *call) faxRun(fx *faxCall) {
	defer contain(c.s.log, "fax run")
	ctx, cancel := c.vmCtx()
	defer cancel()
	tick := time.NewTicker(faxPollEvery)
	defer tick.Stop()
	cl, sid := fx.client()
	bad := 0
	for {
		select {
		case <-ctx.Done():
			// The call ended (a BYE, the maximum duration, a drain): whatever
			// arrived is kept.
			c.faxEndedRun(ctx, fx, cl, sid, false)
			return
		case <-c.s.done:
			c.faxEndedRun(ctx, fx, cl, sid, true) // the node is stopping: end the call too
			return
		case <-tick.C:
		}
		pctx, pcancel := context.WithTimeout(ctx, 5*time.Second)
		info, err := cl.Status(pctx, sid)
		pcancel()
		if err != nil {
			if bad++; bad >= faxPollErrors {
				c.faxFinish(fx, fax.SessionInfo{Text: "the fax engine stopped answering"}, true)
				c.faxHangup(fx)
				return
			}
			continue
		}
		bad = 0
		if fx.outbound && !c.faxRenew(fx) {
			c.addTrace("Fax cancelled by an operator")
			sctx, scancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
			_, _ = cl.Stop(sctx, sid)
			scancel()
			_ = cl.Forget(context.WithoutCancel(ctx), sid)
			fx.done.Store(true)
			c.s.m.FaxTotal.WithLabelValues(fax.DirOutbound, "cancelled").Inc()
			c.faxHangup(fx)
			return
		}
		if info.WantT38 && fx.t38OK && !fx.t38Try.Load() {
			go c.faxSwitchT38("the engine asked")
		}
		if info.Done() {
			c.faxFinish(fx, info, false)
			c.faxHangup(fx)
			return
		}
	}
}

// faxEndedRun stops the engine session of a call that ended (or of a node
// that is stopping) and stores what arrived.
func (c *call) faxEndedRun(ctx context.Context, fx *faxCall, cl *fax.Client, sid string, hangup bool) {
	fx.lost.Store(true)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	info, err := cl.Stop(sctx, sid)
	if err != nil {
		info = fax.SessionInfo{Text: "the fax engine did not answer when the call ended"}
	}
	c.faxFinish(fx, info, true)
	if hangup {
		c.faxHangup(fx)
	}
}

// faxHangup ends the call from our side once T.30 is over.
func (c *call) faxHangup(fx *faxCall) {
	c.mu.Lock()
	done := c.hungUp || c.ended
	c.mu.Unlock()
	if done {
		return
	}
	if fx.outbound {
		c.hangup(cdr.SideCaller)
		return
	}
	c.hangup(cdr.SideCallee)
}

// faxCallEnded runs when the call's CDR is written: it frees the relay's
// engine session if the run loop has not (a call that never reached it).
func (c *call) faxCallEnded() {
	fx := c.faxState()
	if fx == nil {
		return
	}
	fx.lost.Store(true)
}

// faxFinish stores the result of a session: the PDF in object storage, the
// row, the metric. It runs once; ended says the call is already over.
func (c *call) faxFinish(fx *faxCall, info fax.SessionInfo, ended bool) {
	if !fx.done.CompareAndSwap(false, true) {
		return
	}
	s := c.s
	fd := s.deps.Fax
	cl, sid := fx.client()
	dir := fax.DirInbound
	if fx.outbound {
		dir = fax.DirOutbound
	}
	ctx, cancel := context.WithTimeout(context.Background(), faxStoreWait)
	defer cancel()
	fin := fax.Finish{
		Pages: info.Pages, Mode: fx.curMode(), BitRate: info.BitRate, ResultCode: info.Code, ResultText: info.Text,
	}
	if fin.Mode == fax.ModePassthrough {
		fin.Mode = "g711"
	}
	if fin.ResultText == "" && info.Success {
		fin.ResultText = "OK"
	}
	pages := info.Pages
	if !fx.outbound && info.HasPDF && cl != nil {
		if pdf, err := cl.PDF(ctx, sid); err == nil && len(pdf) > 0 {
			key := fax.ObjectKey(fx.rowID, time.Now())
			if perr := fax.PutRetry(ctx, fd.Objects, key, pdf); perr == nil {
				fin.ObjectKey = key
			} else {
				s.log.Error("fax: PDF not stored", "fax_id", fx.rowID, "error", perr)
				fin.ResultText = trimTo("the PDF could not be stored: "+perr.Error(), 200)
				info.Success = false
			}
		} else if err != nil {
			s.log.Warn("fax: PDF not read from the engine", "fax_id", fx.rowID, "error", err)
		}
	}
	if !fx.outbound && fin.ObjectKey == "" {
		pages = 0 // nothing was kept
		fin.Pages = 0
	}
	fin.Status = fax.ResultFor(dir, info.Success, pages)
	if !fx.outbound && fin.ObjectKey != "" && (fin.Status == fax.StatusReceived || fin.Status == fax.StatusPartial) {
		fin.MailTo = store.MailToOwner
		if fx.dest.Email != "" {
			fin.MailTo = fx.dest.Email
		}
	}
	if err := fd.Store.Finish(ctx, fx.rowID, fin); err != nil {
		s.log.Error("fax: row not updated", "fax_id", fx.rowID, "error", err)
	}
	if cl != nil {
		_ = cl.Forget(ctx, sid)
	}
	s.m.FaxTotal.WithLabelValues(dir, fin.Status).Inc()
	c.addTrace(fmt.Sprintf("Fax %s: %d page(s), %s", fin.Status, fin.Pages, fin.ResultText))
	s.log.Info("fax finished", "correlation_id", c.id, "fax_id", fx.rowID, "direction", dir, "status", fin.Status,
		"pages", fin.Pages, "mode", fin.Mode, "result", fin.ResultText)
	if fin.Status != fax.StatusReceived && fin.Status != fax.StatusSent {
		c.mu.Lock()
		c.faxNote = "fax " + fin.Status + ": " + fin.ResultText
		c.mu.Unlock()
	}
}

func trimTo(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n]
	}
	return s
}
