// Session timers on the dialogs a call moves to (phone-feature-parity S-9,
// follow-up 9b): the party a parked call keeps, the phone that retrieves or
// picks a call up, the parker a returned call rings, the target of a
// transferred call and the parties of an attended transfer's bridge. The
// negotiation and the refresh are those of sessiontimer.go; this file only
// carries the state to the call that owns each dialog now.
package sip

import (
	"context"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// linkedPartyIdx is the session dialog of a linked call's own party: the
// only dialog the call still holds (the parker's was released, the peer's
// belongs to the peer call). ok is false for a call that is not linked.
func (c *call) linkedPartyIdx() (idx int, ok bool) {
	side, linked := c.linkSide()
	if !linked {
		return 0, false
	}
	if side == cdr.SideCaller {
		return sessCaller, true
	}
	return sessCallee, true
}

// carrierDialog reports whether dialog idx is with a trunk: the inbound
// trunk of the call, or the trunk leg that won it.
func (c *call) carrierDialog(idx int) bool {
	if idx == sessCaller {
		return c.inTrunk != nil
	}
	c.mu.Lock()
	w := c.winner
	c.mu.Unlock()
	return w != nil && w.trunk != nil
}

// carrierAlive reports whether a refused refresh still proves a trunk's far
// end is there: a carrier that challenges (401, 407), forbids (403) or does
// not take the SDP (488) in dialog answered, and ending the call over it
// would drop a call that works. Only a silent carrier, 408 or 481 end it.
func (c *call) carrierAlive(idx int, code int) bool {
	switch code {
	case sip.StatusUnauthorized, sip.StatusProxyAuthRequired, sip.StatusForbidden, sip.StatusNotAcceptableHere:
		return c.carrierDialog(idx)
	}
	return false
}

// sessionSelf negotiates the session timer of a linked call's caller dialog
// that Hello answers by itself (answerSelf: the phone that retrieved a parked
// call or picked one up) and returns the headers of the 2xx. A call that is
// not linked (a voicemail, an announcement) gets none.
func (c *call) sessionSelf(body []byte) []sip.Header {
	c.mu.Lock()
	linked := c.link != nil
	c.mu.Unlock()
	if !linked || c.inv == nil {
		return nil
	}
	c.mu.Lock()
	c.rawCaller = true
	c.mu.Unlock()
	c.ses.mu.Lock()
	defer c.ses.mu.Unlock()
	c.ses.sdp[sessCaller] = append([]byte(nil), body...)
	if off, err := media.ParseAudioSDP(c.inv.Body()); err == nil {
		c.ses.addr[sessCaller] = sdpAddr(off)
	}
	c.ses.leg[sessCaller] = c.callerLeg()
	return callerSessionHeaders(c.inv, c.ses.leg[sessCaller])
}

// sessionPickedUp negotiates the caller dialog of a picked-up call, which
// reaches its caller through the call's own dialog session, and returns the
// headers of the 2xx.
func (c *call) sessionPickedUp(body []byte) []sip.Header {
	if c.inv == nil {
		return nil
	}
	c.ses.mu.Lock()
	defer c.ses.mu.Unlock()
	c.ses.sdp[sessCaller] = append([]byte(nil), body...)
	if off, err := media.ParseAudioSDP(c.inv.Body()); err == nil {
		c.ses.addr[sessCaller] = sdpAddr(off)
	}
	c.ses.leg[sessCaller] = c.callerLeg()
	return callerSessionHeaders(c.inv, c.ses.leg[sessCaller])
}

// sessionReturned starts the timer of the parker's dialog on the call that
// rang the parker again (rc), once the parker answered on leg l.
func (c *call) sessionReturned(l *leg) {
	var res *sip.Response
	var inv *sip.Request
	if dcs := l.session(); dcs != nil {
		res, inv = dcs.InviteResponse, dcs.InviteRequest
	}
	c.ses.mu.Lock()
	c.ses.leg[sessCallee] = c.s.calleeSession(l, res)
	if inv != nil {
		c.ses.sdp[sessCallee] = append([]byte(nil), inv.Body()...)
	}
	if res != nil {
		if ans, err := media.ParseAudioSDP(res.Body()); err == nil {
			c.ses.addr[sessCallee] = sdpAddr(ans)
		}
	}
	c.ses.mu.Unlock()
	c.sessionArm()
}

// sessionTransferred gives the call that carries a transferred caller to the
// target (c2, whose caller dialog is from's) its two timers: the caller's
// negotiation moves over from the original call, and the target's leg l is
// negotiated by its answer. SDPs the hand-over already noted are kept.
func (c2 *call) sessionTransferred(from *call, l *leg) {
	from.ses.mu.Lock()
	var callerLeg *sessLeg
	if st := from.ses.leg[sessCaller]; st != nil {
		cp := *st
		cp.timer = nil
		callerLeg = &cp
	}
	callerSDP, callerAddr := from.ses.sdp[sessCaller], from.ses.addr[sessCaller]
	from.ses.mu.Unlock()
	c2.adoptSessionLegs(callerLeg, callerSDP, callerAddr, l)
}

// adoptSessionLegs installs the caller dialog's negotiated timer on c and
// negotiates the callee dialog of leg l from its answer, then arms both and
// starts the dead-media watch.
func (c *call) adoptSessionLegs(callerLeg *sessLeg, callerSDP []byte, callerAddr string, l *leg) {
	var res *sip.Response
	var inv *sip.Request
	if dcs := l.session(); dcs != nil {
		res, inv = dcs.InviteResponse, dcs.InviteRequest
	}
	c.ses.mu.Lock()
	c.ses.leg[sessCaller] = callerLeg
	if len(c.ses.sdp[sessCaller]) == 0 {
		c.ses.sdp[sessCaller] = append([]byte(nil), callerSDP...)
	}
	c.ses.addr[sessCaller] = callerAddr
	c.ses.leg[sessCallee] = c.s.calleeSession(l, res)
	if inv != nil && len(c.ses.sdp[sessCallee]) == 0 {
		c.ses.sdp[sessCallee] = append([]byte(nil), inv.Body()...)
	}
	if res != nil {
		if ans, err := media.ParseAudioSDP(res.Body()); err == nil {
			c.ses.addr[sessCallee] = sdpAddr(ans)
		}
	}
	c.ses.mu.Unlock()
	c.sessionArm()
	c.startMediaWatch()
}

// sessionBridged gives the call an attended transfer bridges (c3: the
// original caller's dialog and the second party's leg wB) the timers of
// those two dialogs, taken from the call each came from.
func (c3 *call) sessionBridged(caller, second *call, wB *leg) {
	caller.ses.mu.Lock()
	var callerLeg *sessLeg
	if st := caller.ses.leg[sessCaller]; st != nil {
		cp := *st
		cp.timer = nil
		callerLeg = &cp
	}
	callerSDP, callerAddr := caller.ses.sdp[sessCaller], caller.ses.addr[sessCaller]
	bridge := caller.ses.bridge // what the bridge re-INVITEd both dialogs with
	caller.ses.mu.Unlock()
	if len(bridge[sessCaller]) > 0 {
		callerSDP = bridge[sessCaller]
	}
	second.ses.mu.Lock()
	var calleeLeg *sessLeg
	if st := second.ses.leg[sessCallee]; st != nil {
		cp := *st
		cp.timer = nil
		calleeLeg = &cp
	}
	calleeSDP, calleeAddr := second.ses.sdp[sessCallee], second.ses.addr[sessCallee]
	second.ses.mu.Unlock()
	if len(bridge[sessCallee]) > 0 {
		calleeSDP = bridge[sessCallee]
	}
	c3.ses.mu.Lock()
	c3.ses.leg = [2]*sessLeg{callerLeg, calleeLeg}
	c3.ses.sdp[sessCaller] = append([]byte(nil), callerSDP...)
	c3.ses.sdp[sessCallee] = append([]byte(nil), calleeSDP...)
	c3.ses.addr = [2]string{callerAddr, calleeAddr}
	c3.ses.mu.Unlock()
	c3.sessionArm()
	c3.startMediaWatch()
}

// sendRawCallerRefresh sends a refresh on a caller dialog Hello answered
// through the raw transaction (c.rawCaller): the request is built from the
// INVITE and vmTag, the way byeCallerRaw builds its BYE, and a re-INVITE's
// answer is ACKed.
func (c *call) sendRawCallerRefresh(ctx context.Context, method sip.RequestMethod, interval time.Duration, sdp []byte) (*sip.Response, error) {
	s := c.s
	c.mu.Lock()
	inv, tag := c.inv, c.vmTag
	c.mu.Unlock()
	if inv == nil || tag == "" {
		return nil, fmt.Errorf("no caller dialog")
	}
	target := inv.Recipient
	if ct := inv.Contact(); ct != nil {
		target = ct.Address
	}
	req := sip.NewRequest(method, *target.Clone())
	req.AppendHeader(&sip.FromHeader{Address: *inv.To().Address.Clone(),
		Params: sip.HeaderParams{{K: "tag", V: tag}}})
	req.AppendHeader(&sip.ToHeader{Address: *inv.From().Address.Clone(),
		Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.From())}}})
	callID := sip.CallIDHeader(inv.CallID().Value())
	req.AppendHeader(&callID)
	seq := s.nextCSeq(callID.Value(), inv.CSeq().SeqNo)
	s.noteSentCSeq(callID.Value(), seq)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: seq, MethodName: method})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Supported", "timer"))
	req.AppendHeader(sip.NewHeader("Session-Expires", secondsOf(interval)+";refresher=uac"))
	req.AppendHeader(sip.NewHeader("Min-SE", secondsOf(s.cfg.SessionMinSE)))
	if method == sip.INVITE && len(sdp) > 0 {
		req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		req.SetBody(sdp)
	}
	req.SetTransport("UDP")
	res, err := s.do(ctx, req)
	if err == nil && method == sip.INVITE {
		ack := sip.NewRequest(sip.ACK, *target.Clone())
		ack.AppendHeader(sip.HeaderClone(req.From()))
		ack.AppendHeader(sip.HeaderClone(req.To()))
		ack.AppendHeader(sip.HeaderClone(req.CallID()))
		ack.AppendHeader(&sip.CSeqHeader{SeqNo: seq, MethodName: sip.ACK})
		ack.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
		ack.SetTransport("UDP")
		if werr := s.client.WriteRequest(ack, sipgo.ClientRequestAddVia); werr != nil {
			s.log.Debug("session refresh ACK to the retriever failed", "correlation_id", c.id, "error", werr)
		}
	}
	return res, err
}

// isRawCaller reports whether the caller dialog was answered through the raw
// transaction.
func (c *call) isRawCaller() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.rawCaller
}

// setLinkHeld records a hold (or resume) signalled across the link, by the
// call's own party or by its peer, so the dead-media watch leaves a held pair
// alone. A resume restarts its silence clock.
func (c *call) setLinkHeld(held bool) {
	c.mu.Lock()
	was := c.linkHeld
	c.linkHeld = held
	c.mu.Unlock()
	if was && !held {
		c.mediaTouched()
	}
}

// noteBridgeSDP records the SDP an attended transfer's bridge sent on a
// dialog of this call, for the bridged call that takes the dialog over.
func (c *call) noteBridgeSDP(idx int, body []byte) {
	c.ses.mu.Lock()
	c.ses.bridge[idx] = append([]byte(nil), body...)
	c.ses.mu.Unlock()
}

// sessionSolo negotiates the caller's dialog of a call Hello answers itself
// and keeps on the relay (an IVR menu, a call queue), and returns the headers
// of the 2xx. A caller that names itself the refresher is honoured: its
// refreshes are answered by soloInDialog.
func (c *call) sessionSolo(body []byte) []sip.Header {
	if c.inv == nil {
		return nil
	}
	st := c.callerLeg()
	c.ses.mu.Lock()
	defer c.ses.mu.Unlock()
	c.ses.sdp[sessCaller] = append([]byte(nil), body...)
	if off, err := media.ParseAudioSDP(c.inv.Body()); err == nil {
		c.ses.addr[sessCaller] = sdpAddr(off)
	}
	c.ses.leg[sessCaller] = st
	return callerSessionHeaders(c.inv, st)
}

// sessionBridgedTo gives the call an application (an IVR menu, a queue) has
// connected to the destination leg w the destination's session timer, and
// starts the dead-media watch, which the application phase left off.
func (c *call) sessionBridgedTo(w *leg, bres *sip.Response) {
	var inv *sip.Request
	if dcs := w.session(); dcs != nil {
		inv = dcs.InviteRequest
	}
	c.ses.mu.Lock()
	if old := c.ses.leg[sessCallee]; old != nil && old.timer != nil {
		old.timer.Stop()
	}
	c.ses.leg[sessCallee] = c.s.calleeSession(w, bres)
	if inv != nil {
		c.ses.sdp[sessCallee] = append([]byte(nil), inv.Body()...)
	}
	if bres != nil {
		if ans, err := media.ParseAudioSDP(bres.Body()); err == nil {
			c.ses.addr[sessCallee] = sdpAddr(ans)
		}
	}
	c.ses.mu.Unlock()
	c.mediaTouched()
	c.sessionArm()
	c.startMediaWatch()
}

// sessionQueued carries the caller dialog's timer of a call a transfer puts
// in a queue (c2 takes the caller dialog from from), whoever refreshes.
func (c2 *call) sessionQueued(from *call, aOffer []byte) {
	from.ses.mu.Lock()
	var st *sessLeg
	if l := from.ses.leg[sessCaller]; l != nil {
		cp := *l
		cp.timer = nil
		st = &cp
	}
	addr := from.ses.addr[sessCaller]
	from.ses.mu.Unlock()
	c2.ses.mu.Lock()
	c2.ses.leg[sessCaller] = st
	c2.ses.sdp[sessCaller] = append([]byte(nil), aOffer...)
	c2.ses.addr[sessCaller] = addr
	c2.ses.mu.Unlock()
	c2.sessionArm()
}

// soloInDialog answers a re-INVITE or UPDATE the caller sends on the dialog
// of a call an application (an IVR menu, a queue) holds: a session refresh, a
// keep-alive re-INVITE or a roam. It answers 200 with the SDP Hello last gave
// the caller (direction mirrored, the relay re-aimed at a new media address)
// instead of 481, which would end the caller's session. It reports whether it
// answered; any other call goes on to the ordinary handling.
func (c *call) soloInDialog(req *sip.Request, tx sip.ServerTransaction) bool {
	c.mu.Lock()
	ok := c.connected && !c.ended && !c.hungUp && c.winner == nil && c.ivr != nil && c.ivr.active.Load() &&
		c.homedCall == nil && c.link == nil && c.park == nil && c.page == nil
	c.mu.Unlock()
	if !ok || (req.Method != sip.INVITE && req.Method != sip.UPDATE) {
		return false
	}
	var done bool
	if tx, done = c.sessionIn(sessCaller, req, tx); done {
		return true
	}
	c.ses.mu.Lock()
	last := append([]byte(nil), c.ses.sdp[sessCaller]...)
	c.ses.mu.Unlock()
	body := req.Body()
	dir := media.SDPDirection(body)
	var ans []byte
	if len(last) > 0 && len(body) > 0 {
		ans = withDirection(last, mirrorDirection(dir))
		if off, err := media.ParseAudioSDP(body); err == nil {
			c.relearnMedia(true, req.Source(), off)
		}
	} else if len(last) > 0 && req.Method == sip.INVITE {
		ans = last // a late offer: ours goes in the 200
	}
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", ans)
	if len(ans) > 0 {
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}
	res.AppendHeader(sip.HeaderClone(&c.s.contact))
	c.s.send(tx, res)
	return true
}
