// INVITE with Replaces (RFC 3891, phone-feature-parity S-17). A phone sends
// one to take over a dialog Hello already has:
//
//   - an early dialog (a call still ringing): the phone picks the call up if
//     its ringing extension is in the phone's pickup scope (S-50), as the
//     pickup codes do;
//   - a confirmed dialog (an answered call): the phone takes over one party of
//     the call, which is how a user moves a call from one of their phones to
//     another (call pull, a shared line).
//
// Who may replace a confirmed dialog: only a device of the same extension as
// the party whose dialog is named, so a user can move their own call between
// their own phones and nobody else's. A phone in the pickup scope may answer a
// ringing call, but never take over an answered one; a trunk's dialog is never
// replaced. RFC 3891 matching: the Call-ID and both tags must name the dialog
// (481 otherwise), and early-only refuses a confirmed dialog (486).
//
// The taken-over party's media is re-anchored on the call's relay: the new
// phone is pointed at the relay leg the old phone used, answered with that
// leg's port and the call's codec, so the other party hears no change; a
// party the old phone had held is resumed. The old phone gets a BYE. Both
// CDRs name each other in their traces: the replaced call closes with
// "replaced by", the new phone's call records that it replaced it.
//
// How the call continues depends on which dialog was named:
//
//   - the caller's dialog of a plain call: the new phone's call becomes the
//     call, with the original callee leg as its answering leg. It is an
//     ordinary bridged call from then on (transfers, park, hold), and it is
//     replicated for in-call HA when the original was;
//   - the callee's dialog: the original call keeps its caller, and the new
//     phone's call is linked to it like a picked-up call (park.go callLink).
//     A linked pair is not replicated (park_ha.go); the original's replicated
//     record is removed, so no survivor takes over a dialog that left;
//   - the own party's dialog of a linked call (a call that was picked up,
//     retrieved or replaced before): the new phone's call takes its place in
//     the link.
//
// A call held on another node is reached through the park command mailbox
// (ParkOpReplace), which links the two calls across nodes. A call that is
// parked, paging, in a conference, carrying video or a browser leg, taken
// over from a dead node, or whose linked peer is on another node is not
// replaced (486).
package sip

import (
	"encoding/xml"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// The labels of hello_replaces_total.
const (
	replacesEarly     = "early"
	replacesConfirmed = "confirmed"

	replacesOK        = "ok"
	replacesNotFound  = "not_found"
	replacesForbidden = "forbidden"
	replacesBusy      = "busy"
	replacesRefused   = "refused"
)

// replacesHdr is a parsed Replaces header (RFC 3891 section 6.1).
type replacesHdr struct {
	callID, toTag, fromTag string
	earlyOnly              bool
}

// parseReplaces reads "call-id;to-tag=a;from-tag=b[;early-only]"; ok is
// false without a Call-ID.
func parseReplaces(v string) (replacesHdr, bool) {
	parts := strings.Split(strings.TrimSpace(v), ";")
	callID, err := url.QueryUnescape(strings.TrimSpace(parts[0]))
	if err != nil {
		return replacesHdr{}, false
	}
	r := replacesHdr{callID: callID}
	for _, p := range parts[1:] {
		k, val, _ := strings.Cut(strings.TrimSpace(p), "=")
		switch strings.ToLower(k) {
		case "to-tag":
			r.toTag = val
		case "from-tag":
			r.fromTag = val
		case "early-only":
			r.earlyOnly = true
		}
	}
	return r, r.callID != ""
}

// tagsMatch reports whether the header's tags name the dialog whose tags are
// local (Hello's) and remote (the phone's). RFC 3891 gives them from the
// replaced dialog's own point of view, which phones read either way round
// from a dialog event, so both orders are accepted: the tags are random, so
// only the dialog's own pair matches.
func (r replacesHdr) tagsMatch(local, remote string) bool {
	if local == "" || remote == "" {
		return false
	}
	return (r.toTag == local && r.fromTag == remote) || (r.toTag == remote && r.fromTag == local)
}

func (s *Server) replacesMetric(dialog, result string) {
	if s.m.Replaces != nil {
		s.m.Replaces.WithLabelValues(dialog, result).Inc()
	}
}

// replacesFail refuses the replacing INVITE and records its CDR. The refusal
// is counted before the response goes out, so whoever sees the refusal (the
// phone, a scrape after it) sees it counted.
func (c *call) replacesFail(tx sip.ServerTransaction, dialog string, code int, why string) {
	result := replacesRefused
	switch code {
	case sip.StatusCallTransactionDoesNotExists, sip.StatusNotFound:
		result = replacesNotFound
	case sip.StatusForbidden:
		result = replacesForbidden
	case sip.StatusBusyHere:
		result = replacesBusy
	}
	c.s.replacesMetric(dialog, result)
	c.addTrace("Replaces refused: " + why)
	c.s.respond(tx, c.inv, code, statusText(code))
	c.record(code, cdr.SideSystem, why, ResultNotFound)
}

// handleReplaces serves an INVITE with Replaces from an authenticated phone;
// c is the phone's call.
func (c *call) handleReplaces(tx sip.ServerTransaction, snap *snapshot.Snapshot, raw string) {
	s := c.s
	rh, ok := parseReplaces(raw)
	if !ok {
		c.replacesFail(tx, replacesConfirmed, sip.StatusBadRequest, "malformed Replaces header")
		return
	}
	if ref, found := s.lookup(rh.callID); found && ref.c != c {
		ref.c.mu.Lock()
		connected := ref.c.connected
		ref.c.mu.Unlock()
		if !connected {
			c.replacesPickup(tx, snap, ref.c)
			return
		}
		c.replaceConfirmed(tx, ref, rh)
		return
	}
	if lc, found := s.liveDialog(rh.callID); found {
		if lc.State != "connected" {
			c.replacesPickupRemote(tx, snap, lc)
			return
		}
		c.replaceRemote(tx, lc, raw)
		return
	}
	c.replacesFail(tx, replacesConfirmed, sip.StatusCallTransactionDoesNotExists, "the Replaces dialog does not exist")
}

// liveDialog finds the live call on another node one of whose dialogs is
// callID.
func (s *Server) liveDialog(callID string) (livestate.Call, bool) {
	if s.deps.Park == nil {
		return livestate.Call{}, false // no command mailbox: nothing on another node can be reached
	}
	ctx, cancel := s.stateCtx()
	live, err := s.deps.State.Calls(ctx)
	cancel()
	if err != nil {
		return livestate.Call{}, false
	}
	for _, lc := range live {
		if lc.Node != s.cfg.NodeID && (lc.SIPCallID == callID || slices.Contains(lc.LegCallIDs, callID)) {
			return lc, true
		}
	}
	return livestate.Call{}, false
}

// replacesPickup serves Replaces naming an early dialog of a call on this
// node: in the phone's pickup scope it is picked up (S-50).
func (c *call) replacesPickup(tx sip.ServerTransaction, snap *snapshot.Snapshot, target *call) {
	var cands []pickupCandidate
	for _, cand := range c.s.pickupCandidates(snap, c.callerNum, "") {
		if cand.local == target {
			cands = append(cands, cand)
		}
	}
	if len(cands) == 0 {
		c.replacesFail(tx, replacesEarly, sip.StatusForbidden, "the Replaces dialog is not in your pickup scope")
		return
	}
	c.s.replacesMetric(replacesEarly, replacesOK)
	c.pickupFrom(tx, snap, cands)
}

// replacesPickupRemote is replacesPickup for a ringing call on another node.
func (c *call) replacesPickupRemote(tx sip.ServerTransaction, snap *snapshot.Snapshot, lc livestate.Call) {
	var cands []pickupCandidate
	for _, cand := range c.s.pickupCandidates(snap, c.callerNum, "") {
		if cand.local == nil && cand.live.ID == lc.ID {
			cands = append(cands, cand)
		}
	}
	if len(cands) == 0 {
		c.replacesFail(tx, replacesEarly, sip.StatusForbidden, "the Replaces dialog is not in your pickup scope")
		return
	}
	c.s.replacesMetric(replacesEarly, replacesOK)
	c.pickupFrom(tx, snap, cands)
}

// dialogTags are Hello's and the phone's tags of the dialog ref names; ok
// is false while it has none (not answered on that dialog).
func (t *call) dialogTags(ref dialogRef) (local, remote string, ok bool) {
	if l := ref.leg; l != nil {
		dcs := l.session()
		if dcs == nil || dcs.InviteRequest == nil || dcs.InviteResponse == nil {
			return "", "", false
		}
		return tagParam(dcs.InviteRequest.From()), tagParam(dcs.InviteResponse.To()), true
	}
	t.mu.Lock()
	vmTag, inv, dss := t.vmTag, t.inv, t.dss
	t.mu.Unlock()
	if inv == nil {
		return "", "", false
	}
	remote = tagParam(inv.From())
	switch {
	case vmTag != "":
		local = vmTag
	case dss != nil:
		t.answerWrite.Lock()
		res := dss.InviteResponse
		t.answerWrite.Unlock()
		if res == nil {
			return "", "", false
		}
		local = tagParam(res.To())
	}
	return local, remote, local != ""
}

// dialogParty is the extension of the phone on the dialog ref names; ok is
// false when that party is not a phone of an extension (a trunk, a SIP URI,
// a click-to-call's own leg).
func (t *call) dialogParty(ref dialogRef) (ext string, ok bool) {
	if l := ref.leg; l != nil {
		if l.trunk != nil || l.uri != nil || l.binding.Device == "" {
			return "", false
		}
		return l.binding.Extension, l.binding.Extension != ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.callerDevice == "" || t.inTrunk != nil || t.orig != nil {
		return "", false
	}
	return t.callerNum, t.callerNum != ""
}

// replaceCheck applies RFC 3891 matching and the authorization rule to a
// confirmed dialog: code 0 means the phone of extension ext may replace it.
func (t *call) replaceCheck(ref dialogRef, rh replacesHdr, ext string) (code int, why string) {
	if rh.earlyOnly {
		return sip.StatusBusyHere, "early-only, and the dialog is answered"
	}
	local, remote, ok := t.dialogTags(ref)
	if !ok || !rh.tagsMatch(local, remote) {
		return sip.StatusCallTransactionDoesNotExists, "the Replaces tags do not name the dialog"
	}
	party, ok := t.dialogParty(ref)
	if !ok {
		return sip.StatusForbidden, "the dialog's party is not a phone"
	}
	if party != ext {
		return sip.StatusForbidden, "only a phone of extension " + party + " may take over its call"
	}
	return 0, ""
}

// plainReplaceable reports whether t is an ordinary answered, anchored call
// between two parties whose dialogs may be rewired. Callers hold t.mu.
func (t *call) plainReplaceable() bool {
	return t.connected && !t.ended && !t.hungUp && !t.replaced && t.link == nil && t.park == nil &&
		t.homedCall == nil && t.page == nil && t.conf == nil && t.confFwd == nil && t.fx == nil &&
		!t.browserOrVideoLocked() && t.orig == nil && t.anchored && t.relay != nil && t.winner != nil && t.vmTag == ""
}

// browserOrVideoLocked reports whether the call has a browser leg or carries
// video: its media is more than the audio leg a phone can take over. Callers
// hold t.mu.
func (t *call) browserOrVideoLocked() bool {
	if w := t.web; w != nil && (w.caller || w.altLeg || w.used || w.slot || len(w.video) > 0) {
		return true
	}
	return t.relay != nil && (t.relay.IsWebLeg(legCaller) || t.relay.IsWebLeg(legCallee))
}

// replaceConfirmed serves Replaces naming a confirmed dialog on this node.
func (c *call) replaceConfirmed(tx sip.ServerTransaction, ref dialogRef, rh replacesHdr) {
	t := ref.c
	if code, why := t.replaceCheck(ref, rh, c.callerNum); code != 0 {
		c.replacesFail(tx, replacesConfirmed, code, why)
		return
	}
	t.mu.Lock()
	plain := t.plainReplaceable() && (ref.leg == nil || ref.leg == t.winner)
	ownParty := false
	if lk := t.link; lk != nil && !plain {
		ownParty = (lk.selfSide == cdr.SideCaller && ref.leg == nil) ||
			(lk.selfSide == cdr.SideCallee && ref.leg != nil && ref.leg == t.winner)
	}
	t.mu.Unlock()
	switch {
	case plain && ref.leg == nil:
		c.replaceCaller(tx, t)
	case plain:
		c.replaceCallee(tx, t, ref.leg)
	case ownParty:
		c.replaceLinkedParty(tx, t)
	default:
		c.replacesFail(tx, replacesConfirmed, sip.StatusBusyHere, "the call cannot be taken over in its present state")
	}
}

// replacerOffer is the replacing phone's SDP offer, checked against what the
// call runs on: it must offer the call's codec, and a phone that requires
// SRTP must have offered it. why is set when it cannot be used.
func (c *call) replacerOffer(base media.AudioSDP) (offer media.AudioSDP, why string) {
	offer, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil {
		return offer, "taking over a call needs an SDP offer"
	}
	if !offersCodec(offer, base.Codec) {
		return offer, "the phone does not offer the call's codec " + base.Codec.Name
	}
	if c.callerSRTP().required() && !offer.OffersCrypto() {
		return offer, "the phone requires SRTP and offered none"
	}
	return offer, ""
}

// offersCodec reports whether an offer lists the codec cd.
func offersCodec(offer media.AudioSDP, cd media.Codec) bool {
	if cd.Name == "" {
		return len(offer.Codecs) > 0
	}
	for _, o := range offer.Codecs {
		if o.Kind() == cd.Kind() && o.ClockRate == cd.ClockRate {
			return true
		}
	}
	return false
}

// claimReplace takes a plain call for one replacement; false when another
// got there first or the call changed. Its base codec set and hold state come
// back with it.
func (t *call) claimReplace(w *leg) (base media.AudioSDP, held, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.plainReplaceable() || (w != nil && t.winner != w) {
		return base, false, false
	}
	t.replaced = true
	base = t.negotiated
	if len(base.Codecs) == 0 {
		if off, err := media.ParseAudioSDP(t.inv.Body()); err == nil {
			base = off.Narrow(off.PayloadType)
		}
	}
	return base, t.held, true
}

// unclaimReplace gives a claimed call back when its replacement failed
// before anything was rewired.
func (t *call) unclaimReplace() {
	t.mu.Lock()
	t.replaced = false
	t.mu.Unlock()
}

// replaceCaller moves the caller's dialog of plain call t to this phone's
// call c: c gets t's callee leg and relay and carries on as the call; t ends
// and its caller is sent BYE.
func (c *call) replaceCaller(tx sip.ServerTransaction, t *call) {
	s := c.s
	base, held, ok := t.claimReplace(nil)
	if !ok {
		c.replacesFail(tx, replacesConfirmed, sip.StatusBusyHere, "the call cannot be taken over in its present state")
		return
	}
	offer, why := c.replacerOffer(base)
	if why != "" {
		t.unclaimReplace()
		c.replacesFail(tx, replacesConfirmed, sip.StatusNotAcceptableHere, why)
		return
	}
	if !c.begin(c.inv, tx) {
		t.unclaimReplace()
		return
	}
	t.mu.Lock()
	w, relay, host := t.winner, t.relay, t.anchorHost
	dialled, direction, route, trunk, rewritten := t.dialled, t.direction, t.route, t.trunkName, t.rewritten
	t.mu.Unlock()
	// The media: c takes the relay over and the phone the caller's leg of it.
	c.mu.Lock()
	c.anchorHost = host
	c.mu.Unlock()
	c.adoptRelay(t, relay)
	handOverLeg(relay, legCaller, udpAddr(offer.Address, offer.Port), c.inv.Source())
	ans, secured := c.cryptoAnswerOn(relay, legCaller, c.callerSRTP(), base.PlainOffer(), offer)
	if !secured {
		c.srtpNote("refused")
		c.addTrace("Replaces refused: the phone's SRTP offer could not be used")
		c.respondA(sip.StatusNotAcceptableHere, "Not Acceptable Here")
		c.end(sip.StatusNotAcceptableHere, cdr.SideSystem, "the phone's SRTP offer could not be used", ResultFailed)
		t.addTrace("A takeover of the caller's dialog failed after the media moved; the call ends")
		t.endBoth("takeover failed")
		s.replacesMetric(replacesConfirmed, replacesRefused)
		return
	}
	c.applyNegotiated(base, "replaces", "")
	body := ans.Build(host, relay.LegPort(legCaller), "sendrecv")
	// The answering leg now belongs to c.
	c.mu.Lock()
	c.dialled, c.direction, c.route, c.trunkName, c.rewritten = dialled, direction, route, trunk, rewritten
	c.winner = w
	c.legs = append(c.legs, w)
	c.connected = true
	c.answerTime = time.Now()
	c.mu.Unlock()
	w.c = c // the leg reports to the call that owns it now
	s.mu.Lock()
	s.dialogs[w.callID] = dialogRef{c: c, leg: w}
	s.mu.Unlock()
	c.adoptCalleeSession(t)
	c.addTrace(fmt.Sprintf("Took over the caller's dialog of call %s (Replaces)", t.callID))
	t.addTrace(fmt.Sprintf("The caller's dialog was replaced by %s's phone; the call goes on as call %s", c.callerNum, c.callID))
	res := sip.NewResponseFromRequest(c.dss.InviteRequest, sip.StatusOK, "OK", body)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res.AppendHeader(sip.NewHeader("Allow", allow))
	for _, h := range c.sessionPickedUp(body) {
		res.AppendHeader(h)
	}
	s.m.response(sip.StatusOK)
	c.settleReliable(true)
	c.publish()
	c.answerWrite.Lock()
	err := c.dss.WriteResponse(res)
	c.answerWrite.Unlock()
	// The original call is over either way: its caller leaves now.
	t.end(sip.StatusOK, cdr.SideSystem, "replaced by "+c.callerNum, ResultAnswered)
	go func() {
		defer t.release()
		defer contain(s.log, "replaced caller BYE")
		t.byeA()
	}()
	if err != nil {
		s.log.Info("the replacing phone did not acknowledge", "correlation_id", c.id, "error", err)
		c.endBoth("no ACK from the replacing phone")
		s.replacesMetric(replacesConfirmed, replacesRefused)
		return
	}
	c.mu.Lock()
	if !c.ended {
		c.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, c.expire)
	}
	c.mu.Unlock()
	c.sessionArm()
	c.startMediaWatch()
	c.mediaTouched()
	c.noteSDP(sessCaller, body)
	if held {
		// The phone that left had held the other party: the new phone talks.
		c.resumeParty(relay, host, base, legCallee)
	}
	if snap := s.deps.Snapshots.Current(); snap != nil {
		s.publishDeviceState(s.devicesOf(snap, c.dialled), c.dialled, StateOnCall)
		s.publishDeviceState([]string{c.callerDevice}, c.callerNum, StateOnCall)
	}
	s.replacesMetric(replacesConfirmed, replacesOK)
	// The call replicates under its new caller dialog (incall-ha): the
	// original's record went with its end.
	c.haStart()
	if h := s.answerHook.Load(); h != nil {
		(*h)()
	}
}

// adoptCalleeSession moves the session timer of t's callee dialog to c,
// which now owns that dialog.
func (c *call) adoptCalleeSession(t *call) {
	t.ses.mu.Lock()
	var st *sessLeg
	if cur := t.ses.leg[sessCallee]; cur != nil {
		cp := *cur
		cp.timer = nil
		st = &cp
	}
	sdp, addr := t.ses.sdp[sessCallee], t.ses.addr[sessCallee]
	t.ses.mu.Unlock()
	c.ses.mu.Lock()
	c.ses.leg[sessCallee] = st
	c.ses.sdp[sessCallee] = append([]byte(nil), sdp...)
	c.ses.addr[sessCallee] = addr
	c.ses.mu.Unlock()
}

// resumeParty re-INVITEs the party on relay leg leg in sendrecv: the phone
// that held it was replaced by one that is talking.
func (c *call) resumeParty(relay *media.Relay, host string, base media.AudioSDP, leg string) {
	body := keepSecurity(relay, leg, base).Build(host, relay.LegPort(leg), "sendrecv")
	ok := false
	if leg == legCallee {
		c.mu.Lock()
		w := c.winner
		c.mu.Unlock()
		if w != nil {
			if _, ok = reinvite(c.s, w.session(), body); ok {
				ackLate(w.session(), nil)
			}
		}
	} else {
		c.mu.Lock()
		dss := c.dss
		c.mu.Unlock()
		_, ok = reinviteA(c.s, dss, body)
	}
	if !ok {
		c.addTrace("The held party could not be resumed after the takeover")
		return
	}
	c.noteSDP(map[string]int{legCaller: sessCaller, legCallee: sessCallee}[leg], body)
	c.setHeld(false)
}

// replaceCallee moves the callee's dialog w of plain call t to this phone's
// call c: t keeps its caller and the relay, c is linked to it like a
// picked-up call, and the old callee is sent BYE.
func (c *call) replaceCallee(tx sip.ServerTransaction, t *call, w *leg) {
	s := c.s
	base, held, ok := t.claimReplace(w)
	if !ok {
		c.replacesFail(tx, replacesConfirmed, sip.StatusBusyHere, "the call cannot be taken over in its present state")
		return
	}
	offer, why := c.replacerOffer(base)
	if why != "" {
		t.unclaimReplace()
		c.replacesFail(tx, replacesConfirmed, sip.StatusNotAcceptableHere, why)
		return
	}
	t.mu.Lock()
	relay, host := t.relay, t.anchorHost
	t.link = &callLink{selfSide: cdr.SideCaller, byeSelf: t.byeA, relayOwner: t, relayLeg: legCaller, peer: c, base: base}
	t.mu.Unlock()
	answer, code, why2, attached := t.attachParty(legCallee, offer, c.inv.Source(), c)
	if !attached {
		t.mu.Lock()
		t.link = nil
		t.mu.Unlock()
		t.unclaimReplace()
		c.replacesFail(tx, replacesConfirmed, code, why2)
		return
	}
	if !c.begin(c.inv, tx) {
		// The relay's callee leg already points at the phone: the call
		// cannot go back to the old callee.
		t.linkedHangup(cdr.SideSystem, "takeover failed")
		go w.bye()
		return
	}
	lk := &callLink{selfSide: cdr.SideCaller, byeSelf: c.byeCallerRaw, relayOwner: t, relayLeg: legCallee,
		peer: t, answer: answer, base: base}
	c.mu.Lock()
	c.link = lk
	c.mu.Unlock()
	// The old callee leaves the call; t keeps only its caller.
	t.mu.Lock()
	t.winner = nil
	t.mu.Unlock()
	s.unbind(w.callID, t)
	t.addTrace(fmt.Sprintf("The answering dialog was replaced by %s's phone (call %s)", c.callerNum, c.callID))
	c.addTrace(fmt.Sprintf("Took over the answering dialog of call %s (Replaces)", t.callID))
	// A linked pair is not replicated: the record of t's two dialogs would
	// send a survivor to a phone that left.
	t.haDelete()
	go func() {
		defer contain(s.log, "replaced callee BYE")
		w.bye()
	}()
	if !c.answerSelf(answer) {
		s.notifyPeer(c, lk)
		s.replacesMetric(replacesConfirmed, replacesRefused)
		return
	}
	c.startMediaWatch()
	if held {
		t.resumeParty(relay, host, base, legCaller)
	}
	if snap := s.deps.Snapshots.Current(); snap != nil {
		s.publishDeviceState([]string{c.callerDevice}, c.callerNum, StateOnCall)
	}
	s.replacesMetric(replacesConfirmed, replacesOK)
}

// replaceLinkedParty moves the own party of linked call t (a picked-up,
// retrieved or replaced call) to this phone's call c, which takes t's place
// in the link; t ends and its party is sent BYE.
func (c *call) replaceLinkedParty(tx sip.ServerTransaction, t *call) {
	s := c.s
	refuse := func(why string) {
		c.replacesFail(tx, replacesConfirmed, sip.StatusBusyHere, why)
	}
	t.mu.Lock()
	lk := t.link
	if lk == nil || t.park != nil || t.homedCall != nil || t.ended || t.hungUp || t.replaced {
		t.mu.Unlock()
		refuse("the call cannot be taken over in its present state")
		return
	}
	if lk.node != "" {
		t.mu.Unlock()
		refuse("the call's other party is on another node")
		return
	}
	t.replaced = true
	owner, leg, peer, base := lk.relayOwner, lk.relayLeg, lk.peer, lk.base
	t.mu.Unlock()
	fail := func(code int, why string) {
		t.unclaimReplace()
		c.replacesFail(tx, replacesConfirmed, code, why)
	}
	if owner == nil {
		// A picker or retriever: its party is on the peer's relay, on the
		// leg the peer's own party does not use.
		if peer == nil {
			fail(sip.StatusBusyHere, "the call's media cannot be found")
			return
		}
		peer.mu.Lock()
		plk := peer.link
		peer.mu.Unlock()
		if plk == nil || plk.relayOwner != peer {
			fail(sip.StatusBusyHere, "the call's media cannot be found")
			return
		}
		owner, leg = peer, otherLeg(plk.relayLeg)
	}
	owner.mu.Lock()
	relay, host := owner.relay, owner.anchorHost
	if len(base.Codecs) == 0 && owner.link != nil {
		base = owner.link.base
	}
	if len(base.Codecs) == 0 {
		base = owner.negotiated
	}
	owner.mu.Unlock()
	if relay == nil || len(base.Codecs) == 0 {
		fail(sip.StatusBusyHere, "the call's media cannot be found")
		return
	}
	offer, why := c.replacerOffer(base)
	if why != "" {
		fail(sip.StatusNotAcceptableHere, why)
		return
	}
	if !c.begin(c.inv, tx) {
		t.unclaimReplace()
		return
	}
	if owner == t {
		// t holds the relay: it moves to c, which outlives t.
		c.mu.Lock()
		c.anchorHost = host
		c.mu.Unlock()
		c.adoptRelay(t, relay)
		owner = c
	}
	handOverLeg(relay, leg, udpAddr(offer.Address, offer.Port), c.inv.Source())
	ans, secured := c.cryptoAnswerOn(relay, leg, c.callerSRTP(), base.PlainOffer(), offer)
	if !secured {
		c.srtpNote("refused")
		c.respondA(sip.StatusNotAcceptableHere, "Not Acceptable Here")
		c.end(sip.StatusNotAcceptableHere, cdr.SideSystem, "the phone's SRTP offer could not be used", ResultFailed)
		t.linkedHangup(cdr.SideSystem, "takeover failed")
		s.replacesMetric(replacesConfirmed, replacesRefused)
		return
	}
	owner.syncSRTP()
	answer := ans.Build(host, relay.LegPort(leg), "sendrecv")
	nlk := &callLink{selfSide: cdr.SideCaller, byeSelf: c.byeCallerRaw, relayOwner: owner, relayLeg: leg,
		peer: peer, answer: answer, base: base}
	c.mu.Lock()
	c.link = nlk
	c.mu.Unlock()
	if peer != nil {
		peer.mu.Lock()
		if plk := peer.link; plk != nil {
			if plk.peer == t {
				plk.peer = c
			}
			if plk.relayOwner == t {
				plk.relayOwner = c
			}
		}
		peer.mu.Unlock()
	}
	c.addTrace(fmt.Sprintf("Took over the party of call %s (Replaces)", t.callID))
	t.addTrace(fmt.Sprintf("The party was replaced by %s's phone (call %s)", c.callerNum, c.callID))
	// t leaves without telling the peer, which is c's now; its party gets BYE.
	t.mu.Lock()
	lk.peer, lk.relayOwner = nil, nil
	t.mu.Unlock()
	t.linkedHangup(cdr.SideSystem, "replaced by "+c.callerNum)
	if !c.answerSelf(answer) {
		s.notifyPeer(c, nlk)
		s.replacesMetric(replacesConfirmed, replacesRefused)
		return
	}
	c.mediaTouched()
	c.startMediaWatch()
	if snap := s.deps.Snapshots.Current(); snap != nil {
		s.publishDeviceState([]string{c.callerDevice}, c.callerNum, StateOnCall)
	}
	s.replacesMetric(replacesConfirmed, replacesOK)
}

// otherLeg is the relay leg that is not leg.
func otherLeg(leg string) string {
	if leg == legCaller {
		return legCallee
	}
	return legCaller
}

// replaceRemote serves Replaces naming a confirmed dialog of a call on
// another node: that node rewires the call and links it to c (park command
// mailbox), as a cross-node pickup does.
func (c *call) replaceRemote(tx sip.ServerTransaction, lc livestate.Call, raw string) {
	s := c.s
	offer, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil {
		c.replacesFail(tx, replacesConfirmed, sip.StatusNotAcceptableHere, "taking over a call needs an SDP offer")
		return
	}
	body, ok := c.crossNodeOffer(offer)
	if !ok {
		c.srtpNote("refused")
		c.replacesFail(tx, replacesConfirmed, sip.StatusNotAcceptableHere, "the call is on another node and SRTP cannot be negotiated across nodes")
		return
	}
	rh, _ := parseReplaces(raw)
	rep, ok := s.parkCommand(lc.Node, livestate.ParkCommand{Op: livestate.ParkOpReplace, CallID: rh.callID,
		Offer: body, PeerCallID: c.callID, Retriever: c.callerNum, Source: c.inv.Source(), Replaces: raw})
	if !ok {
		code, why := rep.Code, rep.Reason
		if code == 0 {
			code, why = sip.StatusRequestTimeout, "the holding node did not answer"
		}
		c.replacesFail(tx, replacesConfirmed, code, why)
		return
	}
	lk := &callLink{selfSide: cdr.SideCaller, byeSelf: c.byeCallerRaw, node: lc.Node, callID: rep.CallID, answer: []byte(rep.Answer)}
	if !c.begin(c.inv, tx) {
		s.notifyPeer(c, lk)
		return
	}
	c.mu.Lock()
	c.link = lk
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Took over a dialog of call %s on node %s (Replaces)", lc.SIPCallID, lc.Node))
	if !c.answerSelf([]byte(rep.Answer)) {
		s.notifyPeer(c, lk)
		s.replacesMetric(replacesConfirmed, replacesRefused) // as replaceCallee and replaceLinkedParty count it
		return
	}
	if snap := s.deps.Snapshots.Current(); snap != nil {
		s.publishDeviceState([]string{c.callerDevice}, c.callerNum, StateOnCall)
	}
	s.replacesMetric(replacesConfirmed, replacesOK)
}

// replaceForPeer is the holding node's half of a cross-node replace: the
// named dialog's party leaves and the phone on node From takes its relay
// leg, linked to the call. It returns the answer for that phone and the
// Call-ID the peer names this call by.
func (s *Server) replaceForPeer(cmd livestate.ParkCommand) livestate.ParkReply {
	rep := livestate.ParkReply{ID: cmd.ID}
	fail := func(code int, why string) livestate.ParkReply {
		rep.Code, rep.Reason = code, why
		return rep
	}
	rh, ok := parseReplaces(cmd.Replaces)
	if !ok {
		return fail(sip.StatusBadRequest, "malformed Replaces header")
	}
	offer, err := media.ParseAudioSDP([]byte(cmd.Offer))
	if err != nil {
		return fail(sip.StatusNotAcceptableHere, "bad offer")
	}
	ref, found := s.lookup(rh.callID)
	if !found {
		return fail(sip.StatusCallTransactionDoesNotExists, "the Replaces dialog does not exist")
	}
	t := ref.c
	if code, why := t.replaceCheck(ref, rh, cmd.Retriever); code != 0 {
		return fail(code, why)
	}
	base, held, ok := t.claimReplace(ref.leg)
	if !ok {
		return fail(sip.StatusBusyHere, "the call cannot be taken over in its present state")
	}
	if !offersCodec(offer, base.Codec) {
		t.unclaimReplace()
		return fail(sip.StatusNotAcceptableHere, "the phone does not offer the call's codec "+base.Codec.Name)
	}
	t.mu.Lock()
	w, relay, host := t.winner, t.relay, t.anchorHost
	// The party that stays is the other side of the named dialog.
	stay := &callLink{selfSide: cdr.SideCallee, byeSelf: t.partyBye, relayOwner: t, relayLeg: legCallee,
		node: cmd.From, callID: cmd.PeerCallID, base: base}
	key := w.callID
	if ref.leg != nil {
		stay.selfSide, stay.byeSelf, stay.relayLeg = cdr.SideCaller, t.byeA, legCaller
		key = t.callID
	}
	t.link = stay
	t.mu.Unlock()
	answer, code, why, attached := t.attachParty(otherLeg(stay.relayLeg), offer, cmd.Source, nil)
	if !attached {
		t.mu.Lock()
		t.link = nil
		t.mu.Unlock()
		t.unclaimReplace()
		return fail(code, why)
	}
	t.addTrace(fmt.Sprintf("A dialog was replaced by %s's phone on node %s", cmd.Retriever, cmd.From))
	t.haDelete() // a linked pair is not replicated
	if ref.leg != nil {
		t.mu.Lock()
		t.winner = nil
		t.mu.Unlock()
		s.unbind(w.callID, t)
		go func() {
			defer contain(s.log, "replaced callee BYE")
			w.bye()
		}()
	} else {
		go func() {
			defer contain(s.log, "replaced caller BYE")
			t.byeA()
		}()
	}
	if held {
		t.resumeParty(relay, host, base, stay.relayLeg)
	}
	s.replacesMetric(replacesConfirmed, replacesOK)
	rep.OK, rep.Answer, rep.CallID = true, string(answer), key
	return rep
}

// dialogIdent names an extension's dialog in a dialog-info document (RFC
// 4235 dialog element attributes), seen from the extension's phone: the
// phone's tag is local, Hello's remote.
type dialogIdent struct {
	callID, localTag, remoteTag, direction string
}

// attrs renders the dialog element's attributes; "" for nil.
func (d *dialogIdent) attrs() string {
	if d == nil || d.callID == "" {
		return ""
	}
	var b strings.Builder
	attr := func(k, v string) {
		if v == "" {
			return
		}
		b.WriteString(" " + k + `="`)
		_ = xml.EscapeText(&b, []byte(v))
		b.WriteString(`"`)
	}
	attr("call-id", d.callID)
	attr("local-tag", d.localTag)
	attr("remote-tag", d.remoteTag)
	attr("direction", d.direction)
	return b.String()
}

// extDialog names the dialog behind ext's state on this node: the leg
// ringing one of its phones, or the answered call it is on. nil when there
// is none here (idle, DND, or the call is on another node).
func (s *Server) extDialog(ext, state string) *dialogIdent {
	if state != StateRinging && state != StateOnCall {
		return nil
	}
	s.mu.Lock()
	calls := make([]*call, 0, len(s.calls))
	for c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	for _, c := range calls {
		if id := c.dialogOf(ext, state); id != nil {
			return id
		}
	}
	return nil
}

// dialogOf is c's dialog with a phone of ext in the given state, nil when c
// has none.
func (c *call) dialogOf(ext, state string) *dialogIdent {
	c.mu.Lock()
	ended := c.ended || c.hungUp
	connected, w := c.connected, c.winner
	callerExt, callerDev, inv, vmTag, dss := c.callerNum, c.callerDevice, c.inv, c.vmTag, c.dss
	var ringing []*leg
	if !connected && w == nil {
		for _, l := range c.legs {
			if l.trunk == nil && l.uri == nil && !l.abandoned && l.stage == c.stageSeq && l.binding.Extension == ext {
				ringing = append(ringing, l)
			}
		}
	}
	c.mu.Unlock()
	if ended {
		return nil
	}
	if state == StateRinging {
		for _, l := range ringing {
			if dcs := l.session(); dcs != nil && dcs.InviteRequest != nil {
				return &dialogIdent{callID: l.callID, remoteTag: tagParam(dcs.InviteRequest.From()), direction: "recipient"}
			}
		}
		return nil
	}
	if !connected {
		return nil
	}
	if w != nil && w.trunk == nil && w.uri == nil && w.binding.Extension == ext {
		if dcs := w.session(); dcs != nil && dcs.InviteRequest != nil && dcs.InviteResponse != nil {
			return &dialogIdent{callID: w.callID, localTag: tagParam(dcs.InviteResponse.To()),
				remoteTag: tagParam(dcs.InviteRequest.From()), direction: "recipient"}
		}
	}
	if callerExt == ext && callerDev != "" && inv != nil {
		hello := vmTag
		if hello == "" && dss != nil {
			c.answerWrite.Lock()
			if res := dss.InviteResponse; res != nil && res.IsSuccess() {
				hello = tagParam(res.To())
			}
			c.answerWrite.Unlock()
		}
		if hello != "" {
			return &dialogIdent{callID: c.callID, localTag: tagParam(inv.From()), remoteTag: hello, direction: "initiator"}
		}
	}
	return nil
}
