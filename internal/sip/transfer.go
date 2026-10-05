// Transfers (S-2, S-3). REFER from a connected phone: a blind transfer
// terminates the transferee's leg and originates a new call to the Refer-To
// target with the original caller kept; an attended transfer (Refer-To with
// Replaces) bridges the two existing calls and drops the transferee. The
// transferee's phone learns the outcome from NOTIFYs; a failed transfer
// leaves the original call untouched.
package sip

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// referTimeout bounds one transfer's origination.
const referTimeout = 60 * time.Second

// handleRefer serves an in-dialog REFER from a connected phone.
func (s *Server) handleRefer(req *sip.Request, tx sip.ServerTransaction) {
	ref, ok := s.matchDialog(req)
	if !ok {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	c := ref.c
	c.mu.Lock()
	w, connected := c.winner, c.connected
	c.mu.Unlock()
	if !connected || w == nil {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	rt := req.GetHeader("Refer-To")
	if rt == nil {
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return
	}
	raw, replaces, err := parseReferTo(rt.Value())
	if err != nil || raw == "" {
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return
	}
	// A transfer's dialogs are being rewired; the detail names the dialog
	// the REFER came on, so a taker can tell that transferor it failed.
	referSide := "caller"
	if ref.leg != nil {
		referSide = "callee"
	}
	c.noteHAState(haPhaseTransferring, haReferDetailPrefix+referSide)
	// The transfer target is the URI's user part: an extension number or an
	// external number, routed as the transferee would dial it.
	var tu sip.Uri
	if err := sip.ParseUri(raw, &tu); err != nil {
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return
	}
	target := tu.User
	if target == "" {
		target = tu.Host
	}
	s.respond(tx, req, sip.StatusAccepted, "Accepted")
	if ref.leg != nil {
		if err := w.session().ReadRequest(req, tx); err != nil {
			s.log.Debug("refer read failed", "error", err)
		}
	} else if err := c.dss.ReadRequest(req, tx); err != nil {
		s.log.Debug("refer read failed", "error", err)
	}
	c.addTrace(fmt.Sprintf("REFER to %s", target))
	if replaces != "" {
		// The outcome goes to the dialog the REFER arrived on.
		if ref.leg != nil {
			c.transferAttendedReplaces(w, target, replaces)
		} else {
			c.transferAttendedViaCaller(w, target, replaces)
		}
		return
	}
	if ref.leg != nil {
		c.transferBlind(c.dialled, target, TransferBlind)
		return
	}
	// The caller transferred: its dialog is kept, the transferee leg drops
	// when the target answers, and the NOTIFYs go to the caller's dialog.
	c.transferBlindVia(c.callerNum, target, TransferBlind, nil)
}

// parseReferTo splits a Refer-To header value into the target URI and the
// Replaces parameter of its query ("<sip:300@x?Replaces=...>") when present.
func parseReferTo(v string) (target, replaces string, err error) {
	v = strings.TrimSpace(v)
	v = strings.TrimSuffix(strings.TrimPrefix(v, "<"), ">")
	if i := strings.IndexByte(v, '?'); i >= 0 {
		q, qerr := url.QueryUnescape(v[i+1:])
		if qerr != nil {
			return "", "", qerr
		}
		for _, kv := range strings.Split(q, ";") {
			if strings.HasPrefix(strings.ToLower(kv), "replaces=") {
				replaces = kv[len("replaces="):]
			}
		}
		v = v[:i]
	}
	return v, replaces, nil
}

// transferBlind originates the call to target with the original caller kept,
// and drops the transferee's leg once the target answers. On failure the
// NOTIFY reports it and both legs stay up (spec edge case).
func (c *call) transferBlind(transfereeExt, target, kind string) {
	c.mu.Lock()
	w := c.winner
	c.mu.Unlock()
	c.transferBlindVia(transfereeExt, target, kind, w)
}

// transferBlindVia is transferBlind with an explicit NOTIFY target: viaLeg
// (the transferee's fork) when the REFER came from the callee's phone, nil
// for the caller's dialog.
func (c *call) transferBlindVia(transfereeExt, target, kind string, viaLeg *leg) {
	if kind == "" {
		kind = TransferBlind
	}
	c.mu.Lock()
	c.transferKind = kind
	w := c.winner
	c.mu.Unlock()
	s := c.s
	notify := func(fragment string, final bool) {
		if viaLeg != nil {
			c.notifyRefer(viaLeg, fragment, final)
			return
		}
		c.notifyReferDialog(c.inv, c.dss.InviteResponse, fragment, final, true)
	}
	if target == transfereeExt {
		// Transferring to oneself is refused; the call stays up.
		c.addTrace("Transfer refused: target is the transferee")
		s.m.Transfers.WithLabelValues(kind, TransferFailed).Inc()
		notify("SIP/2.0 403 Forbidden", true)
		c.noteHAState("", "")
		return
	}
	notify("SIP/2.0 100 Trying", false)
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		notify("SIP/2.0 503 Service Unavailable", true)
		c.noteHAState("", "")
		return
	}
	c2, legs, ok := c.originateFor(target, snap)
	if !ok {
		c.addTrace(fmt.Sprintf("Transfer to %s failed: nothing to ring", target))
		s.m.Transfers.WithLabelValues(kind, TransferFailed).Inc()
		notify("SIP/2.0 404 Not Found", true)
		c.noteHAState("", "")
		return
	}
	c2.transferNotify = notify
	go c2.awaitTransfer(c, w, legs, kind, transfereeExt)
}

// originateFor builds the call that carries a transferred caller to target
// and forks its legs: a group, an extension's bindings, or outbound routing
// for an external number. ok=false when nothing can be rung (404).
func (c *call) originateFor(target string, snap *snapshot.Snapshot) (*call, []*leg, bool) {
	s := c.s
	c.mu.Lock()
	c2 := s.newCall(c.inv)
	c2.callID = c.callID
	c2.dialled = target
	c2.callerNum, c2.callerName, c2.callerDevice = c.callerNum, c.callerName, c.callerDevice
	c2.direction, c2.dss = c.direction, c.dss
	c2.events = make(chan legEvent, 16)
	c2.setupDone = make(chan struct{})
	c2.ringTime = c.ringTime
	c.mu.Unlock()
	var legs []*leg
	if snap.HasExtension(target) {
		for _, d := range snap.DevicesForExtension(target) {
			ctx, cancel := s.stateCtx()
			bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
			cancel()
			if err != nil {
				continue
			}
			for _, b := range bs {
				legs = append(legs, c2.addLeg(&leg{binding: b}))
			}
		}
		return c2, legs, len(legs) > 0
	}
	dec := s.decide(snap.Routing(), routing.Call{
		FromExtension: c.dialled, Number: target, CallerID: c.callerNum, At: time.Now(),
	})
	c2.trace = append(c2.trace, dec.Trace...)
	c2.route, c2.rewritten = dec.Route, dec.Number
	switch {
	case dec.Kind == routing.KindOutbound:
		c2.direction = cdr.DirectionOutbound
		for _, cand := range dec.Candidates {
			t := cand.Trunk
			if t == nil {
				continue
			}
			if ok, _ := c2.acquireSlot(t, dec.Emergency, c2.id); !ok {
				continue
			}
			for _, d := range cand.Destinations {
				legs = append(legs, c2.addLeg(&leg{trunk: t, dest: d, addr: s.trunks.destAddr(d), number: dec.Number, callerID: dec.CallerID}))
			}
			break
		}
	case dec.Kind == routing.KindInternal && dec.Extension != "":
		for _, d := range snap.DevicesForExtension(dec.Extension) {
			ctx, cancel := s.stateCtx()
			bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
			cancel()
			if err != nil {
				continue
			}
			for _, b := range bs {
				legs = append(legs, c2.addLeg(&leg{binding: b}))
			}
		}
	}
	return c2, legs, len(legs) > 0
}

// awaitTransfer waits for the transferred call's target: on answer the
// transferee's leg drops, the caller hears the target's answer, and the
// original call's CDR closes as transferred; on failure the NOTIFY reports
// the code, the caller dialog returns to the original call, and both legs
// stay up.
func (c2 *call) awaitTransfer(from *call, w *leg, legs []*leg, kind, transfereeExt string) {
	s := c2.s
	for _, l := range legs {
		go l.run()
	}
	s.m.ActiveCalls.Inc()
	s.bind(c2.callID, dialogRef{c: c2})
	go c2.heartbeat()
	c2.publish()
	left := len(legs)
	timer := time.NewTimer(referTimeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-c2.events:
			switch ev.kind {
			case evRinging:
				c2.ringing()
			case evAnswered:
				w.bye() // the transferee's leg drops once the target is up
				// drive already claimed the winning fork.
				c2.mu.Lock()
				c2.connected = true
				c2.mu.Unlock()
				ev.leg.ack(nil, "") // confirm the target's answer first
				c2.handOver(from, ev.leg)
				c2.publish()
				if c2.maxTimer == nil {
					c2.mu.Lock()
					c2.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, c2.expire)
					c2.mu.Unlock()
				}
				from.end(sip.StatusOK, cdr.SideCallee, kind+" transfer", ResultAnswered)
				// The transferred call replicates from here on (incall-ha
				// S-5): after from.end, whose record deletion shares the
				// caller dialog's key.
				c2.haStart()
				s.noteCallEnd(from.callerNum, c2.dialled)
				s.m.Transfers.WithLabelValues(kind, TransferAnswered).Inc()
				c2.addTrace(fmt.Sprintf("Transferred call answered by %s; transferee %s released", c2.dialled, transfereeExt))
				if c2.transferNotify != nil {
					c2.transferNotify("SIP/2.0 200 OK", true)
				} else {
					c2.notifyRefer(w, "SIP/2.0 200 OK", true)
				}
				return
			case evFailed:
				left--
				if left > 0 {
					continue
				}
				c2.transferFailed(from, w, kind, sip.StatusTemporarilyUnavailable)
				return
			}
		case <-c2.canceled:
			// The caller hung up during the transfer: everything ends.
			c2.cancelForks(nil)
			c2.closeSetup()
			c2.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled during transfer", ResultCancelled)
			from.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled during transfer", ResultCancelled)
			if c2.transferNotify != nil {
				c2.transferNotify("SIP/2.0 487 Request Terminated", true)
			} else {
				c2.notifyRefer(w, "SIP/2.0 487 Request Terminated", true)
			}
			return
		case <-timer.C:
			c2.cancelForks(nil)
			c2.transferFailed(from, w, kind, sip.StatusRequestTimeout)
			return
		}
	}
}

// transferFailed tears the origination down silently, hands the caller's
// dialog back to the original call, and reports the failure to the
// transferee, whose call stays up.
func (c2 *call) transferFailed(from *call, w *leg, kind string, code int) {
	s := c2.s
	c2.cancelForks(nil)
	c2.closeSetup()
	c2.end(code, cdr.SideCallee, kind+" transfer failed", ResultFailed)
	s.mu.Lock()
	s.dialogs[c2.callID] = dialogRef{c: from}
	delete(s.calls, c2)
	s.mu.Unlock()
	c2.addTrace(fmt.Sprintf("Transfer to %s failed: %d", c2.dialled, code))
	s.m.Transfers.WithLabelValues(kind, TransferFailed).Inc()
	from.noteHAState("", "") // the original call talks on, untransferred
	if from.transferNotify != nil {
		from.transferNotify(fmt.Sprintf("SIP/2.0 %d %s", code, statusText(code)), true)
		return
	}
	from.notifyRefer(w, fmt.Sprintf("SIP/2.0 %d %s", code, statusText(code)), true)
}

// handOver moves the caller's media from the transferee's leg to the new
// leg: the bridge sequence of re-INVITEs, each answering the other.
func (c2 *call) handOver(from *call, l *leg) {
	if c2.handOverAnchored(from, l) {
		return
	}
	aSDP := from.inv.Body()
	if len(aSDP) == 0 {
		aSDP = l.session().InviteResponse.Body()
	}
	cSDP, ok := reinvite(l.session(), aSDP)
	if !ok {
		c2.addTrace("Transferred caller re-INVITE failed; media follows the old path")
		return
	}
	aAns, ok := reinviteA(from.dss, cSDP)
	if !ok {
		return
	}
	ackLate(l.session(), aAns)
	c2.addTrace("Caller media handed to the transferred leg")
}

// notifyRefer sends the transfer outcome to the transferee's phone as an
// in-dialog NOTIFY (Event: refer) with a SIP fragment body; final reports
// close the implied subscription.
func (c *call) notifyRefer(w *leg, fragment string, final bool) {
	defer contain(c.s.log, "refer notify")
	dcs := w.session()
	if dcs == nil || dcs.InviteRequest == nil {
		return
	}
	c.notifyReferDialog(dcs.InviteRequest, dcs.InviteResponse, fragment, final, false)
}

// notifyReferDialog builds and sends one refer NOTIFY from the dialog's own
// INVITE request and response (works for either direction of the dialog).
func (c *call) notifyReferDialog(inv *sip.Request, res *sip.Response, fragment string, final bool, weAreUAS bool) {
	s := c.s
	if inv == nil || res == nil {
		return
	}
	// The remote target: for a dialog we called, the 200's Contact; for a
	// dialog that called us, the INVITE's Contact.
	target := inv.Recipient
	if weAreUAS {
		if ct := inv.Contact(); ct != nil {
			target = ct.Address
		}
	} else if ct := res.Contact(); ct != nil {
		target = ct.Address
	}
	state := "active"
	if final {
		state = "terminated;reason=noresource"
	}
	req := sip.NewRequest(sip.NOTIFY, *target.Clone())
	// We are the dialog's To side: our tag is the one the response carried
	// (the request's To has none when we answered).
	fromTag := tagParam(inv.To())
	if res != nil {
		fromTag = tagParam(res.To())
	}
	from := &sip.FromHeader{Address: *inv.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: fromTag}}}
	to := &sip.ToHeader{Address: *inv.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.From())}}}
	callID := sip.CallIDHeader(inv.CallID().Value())
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	seq := res.CSeq().SeqNo + 1
	req.AppendHeader(&sip.CSeqHeader{SeqNo: seq, MethodName: sip.NOTIFY})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Event", "refer"))
	// The NOTIFY's CSeq is outside sipgo's dialog counters: a taker's
	// requests on this dialog must continue past it.
	s.noteSentCSeq(callID.Value(), seq)
	if s.deps.HAState != nil {
		go c.replicate()
	}
	req.AppendHeader(sip.NewHeader("Subscription-State", state))
	req.AppendHeader(sip.NewHeader("Content-Type", "message/sipfrag"))
	req.SetBody([]byte(fragment + "\r\n"))
	req.SetTransport("UDP")
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	r, err := s.client.Do(ctx, req)
	if err != nil {
		s.log.Debug("refer NOTIFY failed", "error", err)
		return
	}
	s.m.response(r.StatusCode)
}

// tagParam reads a From/To header's tag.
func tagParam(h sip.Header) string {
	switch v := h.(type) {
	case *sip.FromHeader:
		if tag, ok := v.Params.Get("tag"); ok {
			return tag
		}
	case *sip.ToHeader:
		if tag, ok := v.Params.Get("tag"); ok {
			return tag
		}
	}
	return ""
}

// transferAttendedReplaces is the REFER with Replaces: the transferee's two
// calls (this one and the Replaced one) are bridged so the two original
// parties are connected, the transferee's legs drop, and both original
// CDRs close with the bridged-transfer link in their traces.
func (c *call) transferAttendedReplaces(w *leg, target, replaces string) {
	c.attendedReplaces(w, target, replaces, false)
}

// transferAttendedViaCaller is the attended transfer whose REFER arrived on
// the transferee's caller dialog; the NOTIFYs go there.
func (c *call) transferAttendedViaCaller(w *leg, target, replaces string) {
	c.attendedReplaces(w, target, replaces, true)
}

func (c *call) attendedReplaces(w *leg, target, replaces string, viaCaller bool) {
	s := c.s
	ref, ok := s.lookup(replaces)
	if !ok || ref.c == c {
		c.addTrace("Attended transfer: unknown Replaces call")
		c.notifyRefer(w, "SIP/2.0 481 Call/Transaction Does Not Exist", true)
		c.noteHAState("", "")
		return
	}
	other := ref.c
	other.mu.Lock()
	oWinner, oConnected := other.winner, other.connected
	other.mu.Unlock()
	if !oConnected || oWinner == nil {
		// The second call was never answered: treated as a blind transfer
		// (spec edge case).
		c.addTrace("Attended transfer: second call unanswered; blind transfer")
		if viaCaller {
			c.transferBlindVia(c.callerNum, target, TransferAttended, nil)
		} else {
			c.transferBlind(c.dialled, target, TransferAttended)
		}
		return
	}
	notify := func(fragment string, final bool) {
		if viaCaller {
			c.notifyReferDialog(c.inv, c.dss.InviteResponse, fragment, final, true)
			return
		}
		c.notifyRefer(w, fragment, final)
	}
	notify("SIP/2.0 100 Trying", false)
	if !c.bridge(other) {
		notify("SIP/2.0 488 Not Acceptable Here", true)
		c.noteHAState("", "")
		return
	}
	s.m.Transfers.WithLabelValues(TransferAttended, TransferAnswered).Inc()
	notify("SIP/2.0 200 OK", true)
}

// bridge connects this call's caller dialog with other's winning leg,
// re-INVITEing both parties to each other and dropping the transferee's
// dialogs. It reports whether the media hand-over succeeded.
func (c *call) bridge(other *call) bool {
	s := c.s
	c.mu.Lock()
	wB := c.winner // the transferee's dialog with the second party (C side)
	other.mu.Lock()
	wA := other.winner // the transferee's leg on the original call (B side)
	c.mu.Unlock()
	other.mu.Unlock()
	if wB == nil || wA == nil {
		return false
	}
	relay, anchoredOK := other.bridgeAnchored(wB)
	if relay == nil && !anchoredOK {
		aSDP := other.inv.Body()
		if len(aSDP) == 0 {
			// Late offer: the caller's answer lives in the ACK; use the leg's
			// negotiated SDP both ways (the phones renegotiate).
			aSDP = wA.session().InviteResponse.Body()
		}
		// 1. re-INVITE C (the second party's leg) with A's SDP as the offer.
		cSDP, ok := reinvite(wB.session(), aSDP)
		if !ok {
			return false
		}
		// 2. re-INVITE A (the original caller) with C's answer.
		aAns, ok := reinviteA(other.dss, cSDP)
		if !ok {
			return false
		}
		// 3. C's ACK carries A's answer.
		ackLate(wB.session(), aAns)
	} else if !anchoredOK {
		return false
	}
	// 4. Build the bridged call that now owns A's dialog and C's leg.
	c3 := s.newCall(other.inv)
	c3.callID = other.callID
	c3.dialled = c.dialled
	c3.callerNum, c3.callerName, c3.callerDevice = other.callerNum, other.callerName, other.callerDevice
	c3.direction, c3.dss = other.direction, other.dss
	c3.start = other.start
	c3.ringTime = other.ringTime
	wB.c = c3 // the leg now reports to the bridged call
	c3.mu.Lock()
	c3.winner = wB
	c3.connected = true
	c3.answerTime = time.Now()
	c3.mu.Unlock()
	if relay != nil {
		// The bridged call keeps A's anchor: the relay moves from the
		// original call (whose end must not close it) to the bridge.
		c3.adoptRelay(other, relay)
	}
	s.mu.Lock()
	s.dialogs[other.callID] = dialogRef{c: c3}
	s.dialogs[wB.callID] = dialogRef{c: c3, leg: wB}
	s.calls[c3] = struct{}{}
	s.mu.Unlock()
	other.addTrace("Attended transfer bridged with call " + c.callID)
	c.addTrace("Attended transfer bridged with call " + other.callID)
	// 5. Close both original calls; their CDRs keep the bridge link above.
	other.end(sip.StatusOK, cdr.SideCallee, "attended transfer", ResultAnswered)
	c.end(sip.StatusOK, cdr.SideCallee, "attended transfer", ResultAnswered)
	// 6. Drop the transferee: BYE both its dialogs. Each BYE recovers on
	// its own, so one failing leg never leaves the other dialog up.
	go func() {
		go func() {
			defer contain(s.log, "attended transfer teardown (leg)")
			wA.bye()
		}()
		func() {
			defer contain(s.log, "attended transfer teardown (caller)")
			byeDialog(s, c.dss)
		}()
	}()
	s.m.ActiveCalls.Inc()
	c3.publish()
	go c3.heartbeat()
	// The bridged call replicates from here on (incall-ha S-5, the bridged
	// half): after both originals ended, whose record deletions share its
	// caller dialog's key.
	c3.haStart()
	if h := s.answerHook.Load(); h != nil {
		(*h)()
	}
	return true
}

// reinvite sends an in-dialog re-INVITE with body as the offer and returns
// the answer from the 200.
func reinvite(dcs *sipgo.DialogClientSession, body []byte) ([]byte, bool) {
	if dcs == nil || dcs.InviteResponse == nil {
		return nil, false
	}
	inv := dcs.InviteRequest
	req := sip.NewRequest(sip.INVITE, *dcs.InviteRequest.Recipient.Clone())
	from := &sip.FromHeader{Address: *inv.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.To())}}}
	to := &sip.ToHeader{Address: *inv.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.From())}}}
	callID := sip.CallIDHeader(inv.CallID().Value())
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: inv.CSeq().SeqNo + 1, MethodName: sip.INVITE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	if ct := inv.ContentType(); ct != nil {
		req.AppendHeader(sip.HeaderClone(ct))
	}
	if len(body) > 0 {
		req.SetBody(body)
	}
	req.SetTransport("UDP")
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := dcs.Do(ctx, req)
	if err != nil || res == nil || !res.IsSuccess() {
		return nil, false
	}
	return res.Body(), true
}

// reinviteA is a re-INVITE on the caller's dialog (we are the UAS there).
func reinviteA(dss *sipgo.DialogServerSession, body []byte) ([]byte, bool) {
	if dss == nil {
		return nil, false
	}
	inv := dss.InviteRequest
	req := sip.NewRequest(sip.INVITE, *dss.InviteRequest.Recipient.Clone())
	from := &sip.FromHeader{Address: *inv.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.From())}}}
	to := &sip.ToHeader{Address: *inv.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.To())}}}
	callID := sip.CallIDHeader(inv.CallID().Value())
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: inv.CSeq().SeqNo + 1, MethodName: sip.INVITE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	if ct := inv.ContentType(); ct != nil {
		req.AppendHeader(sip.HeaderClone(ct))
	}
	if len(body) > 0 {
		req.SetBody(body)
	}
	req.SetTransport("UDP")
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := dss.Do(ctx, req)
	if err != nil || res == nil || !res.IsSuccess() {
		return nil, false
	}
	return res.Body(), true
}

// ackLate confirms a leg's re-INVITE 2xx with body.
func ackLate(dcs *sipgo.DialogClientSession, body []byte) {
	if dcs == nil || dcs.InviteResponse == nil {
		return
	}
	ack := sip.NewRequest(sip.ACK, *dcs.InviteRequest.Recipient.Clone())
	if ct := dcs.InviteRequest.ContentType(); ct != nil && len(body) > 0 {
		ack.AppendHeader(sip.HeaderClone(ct))
	}
	if len(body) > 0 {
		ack.SetBody(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	if err := dcs.WriteAck(ctx, ack); err != nil {
		return
	}
}

// byeDialog sends one bounded in-dialog BYE from a server dialog without
// the ACK-retransmission waits sipgo's dialog Bye can queue behind.
func byeDialog(s *Server, dss *sipgo.DialogServerSession) {
	if dss == nil || dss.InviteRequest == nil || dss.InviteResponse == nil {
		return
	}
	inv := dss.InviteRequest
	target := inv.Recipient
	if ct := inv.Contact(); ct != nil {
		target = ct.Address
	}
	req := sip.NewRequest(sip.BYE, *target.Clone())
	from := &sip.FromHeader{Address: *inv.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.To())}}}
	to := &sip.ToHeader{Address: *inv.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.From())}}}
	callID := sip.CallIDHeader(inv.CallID().Value())
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: inv.CSeq().SeqNo + 1, MethodName: sip.BYE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.SetTransport("UDP")
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := s.client.Do(ctx, req)
	if err != nil {
		s.log.Debug("dialog BYE failed", "error", err)
		return
	}
	s.m.response(res.StatusCode)
}

// handOverAnchored keeps an anchored call anchored across a blind transfer
// (always-anchor, S-7; incall-ha S-5): the transferred call inherits the
// original call's relay. The caller already sends to the relay's caller
// port; the target is re-INVITEd onto its callee port, and the caller is
// refreshed with its own port in sendrecv (a transferor's hold is over).
// false leaves the hand-over to the direct path (the call was not
// anchored).
func (c2 *call) handOverAnchored(from *call, l *leg) bool {
	from.mu.Lock()
	relay, host := from.relay, from.anchorHost
	if relay == nil || !from.anchored {
		from.mu.Unlock()
		return false
	}
	from.mu.Unlock()
	off, err := media.ParseAudioSDP(from.inv.Body())
	if err != nil {
		if off, err = media.ParseAudioSDP(l.session().InviteResponse.Body()); err != nil {
			return false
		}
	}
	c2.mu.Lock()
	c2.anchorHost = host
	c2.mu.Unlock()
	c2.adoptRelay(from, relay)
	cOffer := media.BuildAudioSDP(host, relay.LegPort(legCallee), off.PayloadType, off.DTMFPayloadType, off.DTMFRate)
	cAns, ok := reinvite(l.session(), cOffer)
	if !ok {
		c2.addTrace("Transferred leg re-INVITE onto the anchor failed; its media follows the original offer")
		return true
	}
	ackLate(l.session(), nil)
	haAim(relay, legCallee, cAns)
	aOffer := media.BuildAudioSDP(host, relay.LegPort(legCaller), off.PayloadType, off.DTMFPayloadType, off.DTMFRate)
	if aAns, ok := reinviteA(from.dss, aOffer); ok {
		haAim(relay, legCaller, aAns)
	}
	c2.addTrace("Caller media handed to the transferred leg through the anchor")
	return true
}

// bridgeAnchored re-points an anchored original call's relay at the
// attended transfer's second party (wB): C is re-INVITEd onto the relay's
// callee port and A refreshed with its own caller port in sendrecv, so the
// bridged call stays anchored. relay is nil when the call is not anchored
// (the direct bridge runs instead); ok is false when C refused.
func (other *call) bridgeAnchored(wB *leg) (*media.Relay, bool) {
	other.mu.Lock()
	relay, host, anchored := other.relay, other.anchorHost, other.anchored
	other.mu.Unlock()
	if relay == nil || !anchored {
		return nil, false
	}
	off, err := media.ParseAudioSDP(other.inv.Body())
	if err != nil {
		return nil, false
	}
	cOffer := media.BuildAudioSDP(host, relay.LegPort(legCallee), off.PayloadType, off.DTMFPayloadType, off.DTMFRate)
	cAns, ok := reinvite(wB.session(), cOffer)
	if !ok {
		return relay, false
	}
	ackLate(wB.session(), nil)
	haAim(relay, legCallee, cAns)
	aOffer := media.BuildAudioSDP(host, relay.LegPort(legCaller), off.PayloadType, off.DTMFPayloadType, off.DTMFRate)
	if aAns, ok := reinviteA(other.dss, aOffer); ok {
		haAim(relay, legCaller, aAns)
	}
	return relay, true
}

// adoptRelay moves an anchored call's relay to c: the original call's end
// no longer closes it (closeMedia sees no relay), its taps report to c, and
// the media metrics keep counting one anchored session.
func (c *call) adoptRelay(from *call, relay *media.Relay) {
	from.mu.Lock()
	if from.relay == relay {
		from.relay = nil
		from.anchored = false
	}
	reason, host := from.anchorReason, from.anchorHost
	from.mu.Unlock()
	relay.OnPacket(c.recTap)
	relay.OnDTMF(func(leg string, digit byte) { c.relayDTMF(leg, digit) })
	c.mu.Lock()
	c.relay, c.anchored, c.anchorReason = relay, true, reason
	if c.anchorHost == "" {
		c.anchorHost = host
	}
	c.mediaMode = "anchored"
	c.rec = &recording{}
	c.haDirs = [2]string{}
	c.mu.Unlock()
}

// haStart begins replicating a call that came out of a transfer: the
// dialog snapshot, then the replication heartbeat.
func (c *call) haStart() {
	if c.s.deps.HAState == nil || !c.isAnchored() {
		return
	}
	c.haRefresh()
	go c.haLoop()
}
