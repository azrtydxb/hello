// Call park (phone-feature-parity spec S-6). A connected call is parked by
// dialling the park code (with or without a slot) during it, or by a blind
// REFER to the park code (the Grandstream WP8x6 Park softkey). The parker's
// dialog is released; the other party stays on the call's media anchor,
// hearing hold audio, while a record in Valkey (hello:park:slot:<slot>) says
// which node holds it, so any replica can retrieve it: the retrieve code
// plus the slot, or the slot number itself, is dialled; the retriever's
// phone is pointed at the held party's relay leg, and the two calls are
// linked so either hang-up ends the other.
//
// An unretrieved call rings its parker again after the lot's return time,
// then goes to the parker's voicemail after the lot's ring window.
package sip

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// Park actions (feature-code rows, migration 00027).
const (
	ActionPark            = "park"
	ActionParkRetrieve    = "park_retrieve"
	ActionPickupGroup     = "pickup_group"
	ActionPickupDirected  = "pickup_directed"
	parkRecordTTL         = 20 * time.Second
	parkRefreshEvery      = 5 * time.Second
	parkCommandPoll       = 200 * time.Millisecond
	parkSlotSyncEvery     = time.Second
	parkCommandWait       = 8 * time.Second
	parkDigitWait         = 1500 * time.Millisecond
	parkAnnounceDigitTone = 180 * time.Millisecond
)

// ParkState is the shared park store (Valkey); *livestate.Store satisfies
// it. Nil disables park, retrieve and cross-node pickup.
type ParkState interface {
	ParkReserve(ctx context.Context, p livestate.ParkedCall, ttl time.Duration) (bool, error)
	ParkGet(ctx context.Context, slot string) (livestate.ParkedCall, bool, error)
	ParkClaim(ctx context.Context, slot string) (livestate.ParkedCall, bool, error)
	ParkRefresh(ctx context.Context, slot, callID string, ttl time.Duration) (bool, error)
	ParkUpdate(ctx context.Context, p livestate.ParkedCall, ttl time.Duration) (bool, error)
	ParkRelease(ctx context.Context, slot, callID string) error
	ParkList(ctx context.Context) ([]livestate.ParkedCall, error)
	ParkSend(ctx context.Context, node string, cmd livestate.ParkCommand) error
	ParkTake(ctx context.Context, node string) (livestate.ParkCommand, bool, error)
	ParkPutReply(ctx context.Context, r livestate.ParkReply) error
	ParkGetReply(ctx context.Context, id string) (livestate.ParkReply, bool, error)
}

// HoldAudio is what a parked party hears. The music-on-hold feature
// implements it (Play blocks, streaming into the relay leg until ctx ends);
// without one the built-in tone plays.
type HoldAudio interface {
	Play(ctx context.Context, relay *media.Relay, leg string)
}

// toneHold is the built-in hold audio: a short tone every ten seconds, so
// the parked party knows the line is still up.
type toneHold struct{}

func (toneHold) Play(ctx context.Context, relay *media.Relay, leg string) {
	beep := media.BeepPCM()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(10 * time.Second):
		}
		if relay.PlayTo(ctx, leg, beep) != nil {
			return
		}
	}
}

// Park phases of a held call.
const (
	phaseParked    = "parked"
	phaseReturning = "returning"
)

// parkState is a held call's parking (guarded by the call's mu except stop).
type parkState struct {
	slot           string
	lot            snapshot.ParkLot
	parker         string
	parkedIsCaller bool
	phase          string
	stop           chan struct{}
	stopOnce       sync.Once
	// partyCallID is the Call-ID of the held party's dialog, the one the
	// slot record and a retriever's lookup name.
	partyCallID string
	// When it was parked and when it returns to its parker, and who is
	// parked, for a taker of the call to continue with.
	parkedAt, returnAt time.Time
	party, partyName   string
}

func (p *parkState) halt() { p.stopOnce.Do(func() { close(p.stop) }) }

// callLink joins a call to the call it is bridged with after a park
// retrieve, a pickup or a return to the parker. A linked call has no B leg
// of its own: its party hangs up on the link, and the peer follows.
type callLink struct {
	// selfSide is the CDR side that means "this call's own party hung up";
	// byeSelf ends that party's dialog.
	selfSide string
	byeSelf  func()
	// relayOwner is the call holding the relay and relayLeg the relay leg
	// of this call's party (re-INVITEs from the party re-aim it); nil for
	// a party whose relay is on another node.
	relayOwner *call
	relayLeg   string
	// peer is the same-node call on the other side; node and callID name
	// it on another node.
	peer         *call
	node, callID string
	// answer is the SDP last given to this call's party, echoed to its
	// re-INVITEs when the relay is elsewhere.
	answer []byte
	// base is the codec set the held call negotiated, for the SDP given to
	// whoever is attached to the relay later.
	base media.AudioSDP
}

// parkMetric counts one park or pickup outcome.
func (s *Server) parkMetric(kind, result string) {
	if s.m.Parks != nil {
		s.m.Parks.WithLabelValues(kind, result).Inc()
	}
}

// isLinked reports whether the call is parked, retrieved, picked up or
// returned: its dialogs are not a plain caller-to-callee pair.
func (c *call) isLinked() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.link != nil || c.park != nil
}

// linkSide is the CDR side of the call's own party when the call is parked
// or linked.
func (c *call) linkSide() (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.link == nil {
		return "", false
	}
	return c.link.selfSide, true
}

// partyBye ends the held call's own party: the dialog the call keeps after
// the parker left.
func (c *call) partyBye() {
	c.mu.Lock()
	lk, w := c.link, c.winner
	c.mu.Unlock()
	if hom := c.homed(); hom != nil {
		c.s.haBye(hom.leg(false)) // a taken-over parked call: its one leg
		return
	}
	if lk != nil && lk.selfSide == cdr.SideCaller {
		c.byeA()
		return
	}
	if w != nil {
		w.bye()
	}
}

// linkedHangup ends a call that is parked or linked. side says who ended it:
// the call's own party (lk.selfSide), or the system / the peer. The peer is
// told, and the party's dialog is ended unless it hung up itself.
func (c *call) linkedHangup(side, reason string) {
	c.mu.Lock()
	if c.hungUp || c.ended {
		c.mu.Unlock()
		return
	}
	c.hungUp = true
	lk, pk := c.link, c.park
	c.mu.Unlock()
	if pk != nil {
		pk.halt()
		c.parkRelease(pk)
	}
	selfGone := lk != nil && side == lk.selfSide
	if reason != "" {
		c.addTrace("Call ended: " + reason)
	}
	c.end(sip.StatusOK, side, reason, ResultAnswered)
	go func() {
		defer c.release()
		defer contain(c.s.log, "linked hangup")
		if lk != nil && !selfGone && lk.byeSelf != nil {
			lk.byeSelf()
		}
		c.s.notifyPeer(c, lk)
	}()
}

// notifyPeer tells the other side of a link that this call ended.
func (s *Server) notifyPeer(from *call, lk *callLink) {
	if lk == nil {
		return
	}
	from.mu.Lock()
	peer, node, callID := lk.peer, lk.node, lk.callID
	from.mu.Unlock()
	switch {
	case peer != nil:
		peer.linkedHangup(cdr.SideSystem, "the other party hung up")
	case node != "" && s.deps.Park != nil:
		ctx, cancel := s.stateCtx()
		defer cancel()
		if err := s.deps.Park.ParkSend(ctx, node, livestate.ParkCommand{
			ID: newID(), Op: livestate.ParkOpBye, From: s.cfg.NodeID, CallID: callID}); err != nil {
			s.log.Warn("could not tell the linked call's node it ended", "node", node, "error", err)
		}
	}
}

// attachParty points relay leg at a phone's SDP offer and returns the answer
// to give that phone: the relay port for the leg with the held party's
// codec. Callers hold no lock.
func (c *call) attachParty(leg string, offer media.AudioSDP) ([]byte, bool) {
	c.mu.Lock()
	relay, host := c.relay, c.anchorHost
	var base media.AudioSDP
	if c.link != nil {
		base = c.link.base
	}
	c.mu.Unlock()
	if relay == nil || len(base.Codecs) == 0 {
		return nil, false
	}
	relay.SetTarget(leg, udpAddr(offer.Address, offer.Port))
	return base.Build(host, relay.LegPort(leg), "sendrecv"), true
}

// --- parking --------------------------------------------------------------

// parkCall parks the connected call from parkerIsCaller's side, in wantSlot
// or the first free slot. It returns the slot, or why the call cannot be
// parked (the call is left untouched then).
func (c *call) parkCall(parkerIsCaller bool, wantSlot string) (string, error) {
	s := c.s
	if s.deps.Park == nil {
		return "", fmt.Errorf("call park is not available on this node")
	}
	snap := s.deps.Snapshots.Current()
	if snap == nil || len(snap.ParkLots()) == 0 {
		return "", fmt.Errorf("no park lot is configured")
	}
	c.mu.Lock()
	w, relay := c.winner, c.relay
	ok := c.connected && !c.ended && !c.hungUp && c.anchored && relay != nil && w != nil &&
		c.homedCall == nil && c.page == nil && c.park == nil && c.link == nil
	parkerExt := c.callerNum
	if !parkerIsCaller {
		parkerExt = w.binding.Extension
		if parkerExt == "" {
			parkerExt = c.dialled
		}
	}
	c.mu.Unlock()
	if !ok {
		return "", fmt.Errorf("this call cannot be parked")
	}
	parkedIsCaller := !parkerIsCaller
	partyCallID, party, partyName := c.callID, c.callerNum, c.callerName
	if !parkedIsCaller {
		partyCallID, partyName = w.callID, ""
		party = w.binding.Extension
		if party == "" {
			party = c.dialled
		}
	}
	now := time.Now()
	rec := livestate.ParkedCall{
		Node: s.cfg.NodeID, CallID: partyCallID, Correlation: c.id, ParkedBy: parkerExt,
		Caller: party, CallerName: partyName, ParkedAt: now, Phase: livestate.ParkParked,
	}
	var lot snapshot.ParkLot
	slot := ""
	try := func(l snapshot.ParkLot, n string) bool {
		rec.Slot, rec.Lot = n, l.Name
		rec.ReturnAt = now.Add(time.Duration(l.ReturnSecs) * time.Second)
		ctx, cancel := s.stateCtx()
		defer cancel()
		got, err := s.deps.Park.ParkReserve(ctx, rec, parkRecordTTL)
		if err != nil {
			s.log.Warn("park slot reserve failed", "slot", n, "error", err)
			return false
		}
		return got
	}
	switch {
	case wantSlot != "":
		l, found := snap.ParkLotOf(wantSlot)
		if !found {
			return "", fmt.Errorf("slot %s is not in a park lot", wantSlot)
		}
		if !try(l, wantSlot) {
			return "", fmt.Errorf("slot %s is occupied", wantSlot)
		}
		lot, slot = l, wantSlot
	default:
	search:
		for _, l := range snap.ParkLots() {
			for _, n := range l.Slots() {
				if try(l, n) {
					lot, slot = l, n
					break search
				}
			}
		}
		if slot == "" {
			return "", fmt.Errorf("every park slot is occupied")
		}
	}
	pk := &parkState{slot: slot, lot: lot, parker: parkerExt, parkedIsCaller: parkedIsCaller,
		phase: phaseParked, stop: make(chan struct{})}
	partyLeg, parkerLeg := legCaller, legCallee
	selfSide := cdr.SideCaller
	if !parkedIsCaller {
		partyLeg, parkerLeg = legCallee, legCaller
		selfSide = cdr.SideCallee
	}
	pk.partyCallID, pk.parkedAt, pk.returnAt = partyCallID, rec.ParkedAt, rec.ReturnAt
	pk.party, pk.partyName = party, partyName
	offered, _ := media.ParseAudioSDP(c.inv.Body())
	c.mu.Lock()
	base := c.negotiated
	if len(base.Codecs) == 0 {
		base = offered.Narrow(offered.PayloadType)
	}
	c.park = pk
	c.link = &callLink{selfSide: selfSide, byeSelf: c.partyBye, relayOwner: c, relayLeg: partyLeg, base: base}
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Parked in slot %s (lot %s) by %s", slot, lot.Name, parkerExt))
	s.parkMetric("park", "ok")
	s.slotChanged(slot)
	go c.parkRun(pk, relay, partyLeg, parkerLeg, parkerIsCaller, w)
	return slot, nil
}

// referPark parks the call for a blind REFER to the park code (the REFER
// has been accepted): the referring side is the parker. Its transfer
// subscription gets the outcome as NOTIFYs, then the announcement and BYE
// follow.
func (c *call) referPark(ref dialogRef, w *leg, slot string) {
	fromCaller := ref.leg == nil
	notify := func(fragment string) {
		if fromCaller {
			c.notifyReferDialog(c.inv, c.dss.InviteResponse, fragment, true, true)
			return
		}
		c.notifyRefer(w, fragment, true)
	}
	got, err := c.parkCall(fromCaller, slot)
	c.noteHAState("", "")
	if err != nil {
		c.addTrace("Park refused: " + err.Error())
		c.s.parkMetric("park", "refused")
		notify("SIP/2.0 486 Busy Here")
		return
	}
	c.addTrace("Park by REFER: slot " + got)
	notify("SIP/2.0 200 OK")
}

// parkRun announces the slot to the parker, releases the parker's dialog and
// then holds the parked party until a retrieve or the return time.
func (c *call) parkRun(pk *parkState, relay *media.Relay, partyLeg, parkerLeg string, parkerIsCaller bool, w *leg) {
	s := c.s
	defer contain(s.log, "park")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-pk.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	// The parker hears the slot, spoken when the console generated the park
	// clips and as one tone per digit otherwise, then its dialog goes.
	if pcm, rate, ok := s.slotSpeech(pk.slot); ok {
		c.addTrace("Slot " + pk.slot + " announced by voice")
		_ = relay.PlayPCMTo(ctx, parkerLeg, pcm, rate)
	} else {
		_ = relay.PlayTo(ctx, parkerLeg, slotTones(pk.slot))
	}
	if parkerIsCaller {
		c.byeA()
	} else {
		w.bye()
	}
	relay.SetTarget(parkerLeg, nil)
	cancel()
	c.parkHold(pk, relay, partyLeg, parkerLeg, pk.returnAt)
}

// parkHold keeps the parked party on hold until a retrieve, the return time
// or its own hang-up. A taker of a dead node's parked call starts here too,
// with the return time the dead node had.
func (c *call) parkHold(pk *parkState, relay *media.Relay, partyLeg, parkerLeg string, returnTime time.Time) {
	s := c.s
	defer contain(s.log, "park hold")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-pk.stop:
			cancel()
		case <-ctx.Done():
		}
	}()
	hold := s.deps.Hold
	if hold == nil {
		hold = toneHold{}
	}
	go hold.Play(ctx, relay, partyLeg)

	refresh := time.NewTicker(parkRefreshEvery)
	defer refresh.Stop()
	returnAt := time.NewTimer(max(time.Until(returnTime), 0))
	defer returnAt.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-refresh.C:
			rctx, rcancel := s.stateCtx()
			ok, err := s.deps.Park.ParkRefresh(rctx, pk.slot, pk.partyCallID, parkRecordTTL)
			rcancel()
			if err == nil && !ok {
				// The record is gone: a retrieve is claiming it (it halts
				// us in a moment) or the store lost it; put it back.
				select {
				case <-ctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
				c.parkRestore(pk)
			}
		case <-returnAt.C:
			rctx, rcancel := s.stateCtx()
			_, claimed, err := s.deps.Park.ParkClaim(rctx, pk.slot)
			rcancel()
			if err != nil || !claimed {
				continue // a retrieve won the slot; it halts this loop
			}
			s.slotChanged(pk.slot)
			c.mu.Lock()
			pk.phase = phaseReturning
			c.mu.Unlock()
			c.parkReturn(ctx, pk, relay, partyLeg, parkerLeg)
			return
		}
	}
}

// parkRestore re-reserves a slot record that vanished while the call is
// still parked.
func (c *call) parkRestore(pk *parkState) {
	s := c.s
	c.mu.Lock()
	still := c.park == pk && pk.phase == phaseParked
	c.mu.Unlock()
	if !still {
		return
	}
	rec := livestate.ParkedCall{Slot: pk.slot, Lot: pk.lot.Name, Node: s.cfg.NodeID, CallID: pk.partyCallID,
		Correlation: c.id, ParkedBy: pk.parker, Caller: c.callerNum, CallerName: c.callerName,
		ParkedAt: time.Now(), ReturnAt: time.Now().Add(time.Duration(pk.lot.ReturnSecs) * time.Second), Phase: livestate.ParkParked}
	ctx, cancel := s.stateCtx()
	defer cancel()
	_, _ = s.deps.Park.ParkReserve(ctx, rec, parkRecordTTL)
}

// parkRelease frees the slot record of a call that ends while parked.
func (c *call) parkRelease(pk *parkState) {
	s := c.s
	if s.deps.Park == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	defer cancel()
	if err := s.deps.Park.ParkRelease(ctx, pk.slot, pk.partyCallID); err != nil {
		s.log.Warn("could not free the park slot", "slot", pk.slot, "error", err)
	}
	s.slotChanged(pk.slot)
}

// parkRetrieved connects a retriever to this held call: the retriever's
// offer is attached to the party's counterpart relay leg and the answer for
// the retriever is returned. The slot is already claimed.
func (c *call) parkRetrieved(offer media.AudioSDP, retriever string, takerNode, takerCallID string, taker *call) ([]byte, int, string, bool) {
	c.mu.Lock()
	pk, lk := c.park, c.link
	if pk == nil || lk == nil || pk.phase != phaseParked || c.ended || c.hungUp {
		c.mu.Unlock()
		return nil, sip.StatusGone, "the parked call is gone", false
	}
	pk.phase = "retrieved"
	c.mu.Unlock()
	pk.halt()
	leg := legCallee
	if lk.relayLeg == legCallee {
		leg = legCaller
	}
	answer, ok := c.attachParty(leg, offer)
	if !ok {
		return nil, sip.StatusServiceUnavailable, "the parked call lost its media", false
	}
	c.mu.Lock()
	c.park = nil
	lk.peer, lk.node, lk.callID = taker, takerNode, takerCallID
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Retrieved from slot %s by %s", pk.slot, retriever))
	c.s.parkMetric("retrieve", "ok")
	c.s.slotChanged(pk.slot)
	return answer, 0, "", true
}

// handleRetrieve answers a dialled retrieve code or slot number: the claimed
// call's relay is pointed at the dialler and the dialler answered with it.
func (c *call) handleRetrieve(tx sip.ServerTransaction, snap *snapshot.Snapshot, slot string) {
	s := c.s
	fail := func(code int, why string) {
		s.respond(tx, c.inv, code, statusText(code))
		c.record(code, cdr.SideSystem, why, ResultNotFound)
		s.parkMetric("retrieve", why)
	}
	if s.deps.Park == nil {
		fail(sip.StatusTemporarilyUnavailable, "call park is not available")
		return
	}
	if _, ok := snap.ParkLotOf(slot); !ok {
		fail(sip.StatusNotFound, "no such park slot")
		return
	}
	offer, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil {
		fail(sip.StatusNotAcceptableHere, "retrieve needs an SDP offer")
		return
	}
	ctx, cancel := s.stateCtx()
	p, got, err := s.deps.Park.ParkClaim(ctx, slot)
	cancel()
	switch {
	case err != nil:
		fail(sip.StatusServiceUnavailable, "park store unavailable")
		return
	case !got:
		fail(sip.StatusNotFound, "slot empty")
		return
	}
	var (
		answer []byte
		peerID string
		code   int
		why    string
		ok     bool
		lk     = &callLink{selfSide: cdr.SideCaller, byeSelf: c.byeCallerRaw}
	)
	if p.Node == s.cfg.NodeID {
		ref, found := s.lookup(p.CallID)
		if !found {
			fail(sip.StatusGone, "the parked call is gone")
			return
		}
		lk.peer = ref.c
		peerID = ref.c.callID
		answer, code, why, ok = ref.c.parkRetrieved(offer, c.callerNum, s.cfg.NodeID, c.callID, c)
	} else {
		var rep livestate.ParkReply
		rep, ok = s.parkCommand(p.Node, livestate.ParkCommand{Op: livestate.ParkOpRetrieve, Slot: slot, CallID: p.CallID,
			Offer: string(c.inv.Body()), PeerCallID: c.callID, Retriever: c.callerNum})
		answer, peerID, code, why = []byte(rep.Answer), rep.CallID, rep.Code, rep.Reason
		lk.node, lk.callID = p.Node, rep.CallID
	}
	if !ok {
		if code == 0 {
			code, why = sip.StatusRequestTimeout, "the holding node did not answer"
		}
		fail(code, why)
		return
	}
	_ = peerID
	if !c.begin(c.inv, tx) {
		s.notifyPeer(c, lk)
		return
	}
	lk.answer = answer
	c.mu.Lock()
	c.link = lk
	c.dialled = slot
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Retrieved the call parked in slot %s (parked by %s)", slot, p.ParkedBy))
	if !c.answerSelf(answer) {
		s.notifyPeer(c, lk)
		return
	}
}

// --- returning to the parker ----------------------------------------------

// parkReturn rings the parker's devices with the held party, then hands the
// call to the parker's voicemail (or ends it) when nobody answers.
func (c *call) parkReturn(ctx context.Context, pk *parkState, relay *media.Relay, partyLeg, parkerLeg string) {
	s := c.s
	snap := s.deps.Snapshots.Current()
	window := time.Duration(pk.lot.VoicemailSecs) * time.Second
	c.addTrace(fmt.Sprintf("Park slot %s timed out: ringing %s again", pk.slot, pk.parker))
	var legs []*leg
	var rc *call
	if snap != nil {
		rc, legs = c.parkReturnLegs(snap, pk, relay, parkerLeg)
	}
	if len(legs) == 0 {
		c.addTrace("The parker has no registered device")
		c.parkVoicemail(ctx, pk, relay)
		return
	}
	for _, l := range legs {
		go l.run()
	}
	s.m.ActiveCalls.Inc()
	s.bind(rc.callID, dialogRef{c: rc})
	go rc.heartbeat()
	rc.publish()
	timer := time.NewTimer(window)
	defer timer.Stop()
	left := len(legs)
	giveUp := func() {
		rc.closeSetup()
		rc.cancelForks(nil)
		rc.end(sip.StatusRequestTimeout, cdr.SideSystem, "parker did not answer the returned call", ResultNoAnswer)
		c.parkVoicemail(ctx, pk, relay)
	}
	for {
		select {
		case <-ctx.Done():
			rc.closeSetup()
			rc.cancelForks(nil)
			rc.end(sip.StatusRequestTerminated, cdr.SideSystem, "the held party hung up", ResultCancelled)
			return
		case <-timer.C:
			giveUp()
			return
		case ev := <-rc.events:
			switch ev.kind {
			case evAnswered:
				rc.cancelForks(ev.leg)
				ev.leg.ack(nil, "")
				c.parkReturnAnswered(rc, ev.leg, pk, partyLeg, parkerLeg)
				return
			case evFailed:
				left--
				if left <= 0 {
					giveUp()
					return
				}
			}
		}
	}
}

// parkReturnLegs builds the call that rings the parker: an INVITE whose
// offer is the relay's parker-side port, one leg per registered contact.
func (c *call) parkReturnLegs(snap *snapshot.Snapshot, pk *parkState, relay *media.Relay, parkerLeg string) (*call, []*leg) {
	s := c.s
	c.mu.Lock()
	host := c.anchorHost
	callerNum, callerName := c.callerNum, c.callerName
	var base media.AudioSDP
	if c.link != nil {
		base = c.link.base
	}
	c.mu.Unlock()
	if len(base.Codecs) == 0 {
		return nil, nil
	}
	from, fromName := callerNum, callerName
	if !pk.parkedIsCaller {
		from, fromName = c.dialled, ""
	}
	sdp := base.Build(host, relay.LegPort(parkerLeg), "sendrecv")
	req := sip.NewRequest(sip.INVITE, sip.Uri{Scheme: "sip", User: pk.parker, Host: s.cfg.Domain})
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(&sip.FromHeader{Address: sip.Uri{Scheme: "sip", User: from, Host: s.cfg.Domain},
		Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(16)}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: pk.parker, Host: s.cfg.Domain}})
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(sdp)
	rc := s.newCall(req)
	rc.callerNum, rc.callerName, rc.dialled = from, fromName, pk.parker
	rc.events = make(chan legEvent, 16)
	rc.addTrace(fmt.Sprintf("Returned call from park slot %s", pk.slot))
	var legs []*leg
	for _, d := range snap.DevicesForExtension(pk.parker) {
		sctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(sctx, s.aor(d.Username))
		cancel()
		if err != nil {
			continue
		}
		for _, b := range bs {
			rc.mu.Lock()
			legs = append(legs, rc.addLeg(&leg{binding: b}))
			rc.mu.Unlock()
		}
	}
	return rc, legs
}

// parkReturnAnswered bridges the parker's answer with the held party.
func (c *call) parkReturnAnswered(rc *call, l *leg, pk *parkState, partyLeg, parkerLeg string) {
	s := c.s
	if ans, err := media.ParseAudioSDP(l.session().InviteResponse.Body()); err == nil {
		c.mu.Lock()
		relay := c.relay
		c.mu.Unlock()
		if relay != nil {
			relay.SetTarget(parkerLeg, udpAddr(ans.Address, ans.Port))
		}
	}
	rc.mu.Lock()
	rc.winner, rc.connected = l, true
	rc.link = &callLink{selfSide: cdr.SideCallee, byeSelf: l.bye, relayOwner: c, relayLeg: parkerLeg, peer: c}
	rc.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, rc.expire)
	rc.mu.Unlock()
	rc.addTrace("The parker answered the returned call")
	rc.publish()
	c.mu.Lock()
	c.park = nil
	if c.link != nil {
		c.link.peer = rc
	}
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Returned to %s from slot %s", pk.parker, pk.slot))
	s.parkMetric("return", "answered")
}

// parkVoicemail hands the held party to the parker's voicemail: the party is
// re-INVITEd onto a voicemail media session and the application runs as for
// any call that reached a box. With no voicemail the call ends.
func (c *call) parkVoicemail(ctx context.Context, pk *parkState, relay *media.Relay) {
	s := c.s
	end := func(why string) {
		c.addTrace(why)
		s.parkMetric("return", "dropped")
		c.linkedHangup(cdr.SideSystem, why)
	}
	snap := s.deps.Snapshots.Current()
	var e snapshot.Extension
	if snap != nil {
		e, _ = snap.Extension(pk.parker)
	}
	if ctx.Err() != nil {
		return
	}
	if !c.voicemailConfigured(e) {
		end("The parker did not answer and has no voicemail: the parked call is ended")
		return
	}
	c.mu.Lock()
	inv, w := c.inv, c.winner
	c.mu.Unlock()
	hom := c.homed()
	var offer []byte
	switch {
	case hom != nil:
		offer = []byte(hom.leg(false).remoteSDP)
	case pk.parkedIsCaller:
		offer = inv.Body()
	default:
		if w == nil || w.session() == nil || w.session().InviteResponse == nil {
			end("The parked call lost its dialog")
			return
		}
		offer = w.session().InviteResponse.Body()
	}
	answer, sess, err := s.answerAnchored(offer)
	if err != nil || sess == nil {
		end("No voicemail media on this node: the parked call is ended")
		return
	}
	var ok bool
	switch {
	case hom != nil:
		_, ok = s.haReinvite(hom.leg(false), answer)
	case pk.parkedIsCaller:
		_, ok = reinviteA(c.dss, answer)
	default:
		_, ok = reinvite(w.session(), answer)
		if ok {
			ackLate(w.session(), nil)
		}
	}
	if !ok {
		_ = sess.Close()
		end("The parked party did not accept the voicemail media")
		return
	}
	c.mu.Lock()
	c.relay, c.anchored = nil, false
	c.dialled = pk.parker
	c.vmSession = sess
	c.mediaMode = "anchored"
	c.park = nil
	c.mu.Unlock()
	relay.Close()
	if m := s.deps.Media; m != nil {
		m.NoteEnd()
	}
	c.addTrace("The parker did not answer: the parked call goes to " + pk.parker + "'s voicemail")
	s.parkMetric("return", "voicemail")
	go c.voicemailFlow(vmLeave, "no-answer", sess)
}

// --- in-dialog requests of linked calls ------------------------------------

// linkedReinvite answers a re-INVITE or UPDATE from a linked call's party
// (a hold, or a new media address after a roam): the relay leg is re-aimed
// at the offer and the answer carries the relay port, direction mirrored.
// A change of direction (hold or resume) is then passed to the peer, whose
// party is re-INVITEd in the same direction, as an anchored bridged call
// does (handleAnchoredInDialog), so the other phone shows the hold too.
func (s *Server) linkedReinvite(c *call, req *sip.Request, tx sip.ServerTransaction) {
	c.mu.Lock()
	lk := c.link
	c.mu.Unlock()
	body := req.Body()
	dir := media.SDPDirection(body)
	var ans []byte
	if lk != nil && lk.relayOwner != nil {
		owner := lk.relayOwner
		owner.mu.Lock()
		relay, host := owner.relay, owner.anchorHost
		owner.mu.Unlock()
		if off, err := media.ParseAudioSDP(body); err == nil && relay != nil {
			relay.SetTarget(lk.relayLeg, udpAddr(off.Address, off.Port))
			ans = media.BuildAudioSDPDir(host, relay.LegPort(lk.relayLeg), off.PayloadType, off.DTMFPayloadType,
				off.DTMFRate, mirrorDirection(dir))
		}
	} else if lk != nil {
		ans = withDirection(lk.answer, mirrorDirection(dir))
	}
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", ans)
	if len(ans) > 0 {
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}
	res.AppendHeader(sip.HeaderClone(&s.contact))
	s.send(tx, res)
	if lk == nil || len(body) == 0 {
		return
	}
	held := dir == "sendonly" || dir == "inactive"
	c.mu.Lock()
	changed := c.held != held
	c.mu.Unlock()
	if !changed {
		return // a refresh or a roam: the peer has nothing to learn
	}
	c.setHeld(held)
	s.holdPeer(c, lk, dir)
}

// withDirection rewrites an SDP body's media direction. A body that does
// not parse is returned unchanged.
func withDirection(body []byte, dir string) []byte {
	sdp, err := media.ParseAudioSDP(body)
	if err != nil {
		return body
	}
	return sdp.Build(sdp.Address, sdp.Port, dir)
}

// holdPeer tells the other side of a link that this call's party held
// (dir sendonly or inactive) or resumed (sendrecv) the call: a same-node
// peer re-INVITEs its party directly, a peer on another node is sent the
// hold command.
func (s *Server) holdPeer(from *call, lk *callLink, dir string) {
	from.mu.Lock()
	peer, node, callID := lk.peer, lk.node, lk.callID
	from.mu.Unlock()
	switch {
	case peer != nil:
		peer.holdParty(dir)
	case node != "" && s.deps.Park != nil:
		ctx, cancel := s.stateCtx()
		defer cancel()
		if err := s.deps.Park.ParkSend(ctx, node, livestate.ParkCommand{
			ID: newID(), Op: livestate.ParkOpHold, From: s.cfg.NodeID, CallID: callID, Dir: dir}); err != nil {
			s.log.Warn("could not tell the linked call's node about the hold", "node", node, "error", err)
		}
	}
}

// holdParty re-INVITEs this linked call's own party with its media in dir:
// the peer held (sendonly, so this side is offered sendonly too and its
// phone shows the call as held by the other party) or resumed (sendrecv).
// The SDP is the relay leg the party already talks to.
func (c *call) holdParty(dir string) {
	s := c.s
	defer contain(s.log, "linked hold")
	c.mu.Lock()
	lk, w, dss := c.link, c.winner, c.dss
	ended := c.ended || c.hungUp
	c.mu.Unlock()
	if lk == nil || ended {
		return
	}
	var body []byte
	if owner := lk.relayOwner; owner != nil {
		owner.mu.Lock()
		relay, host := owner.relay, owner.anchorHost
		base := lk.base
		if len(base.Codecs) == 0 && owner.link != nil {
			base = owner.link.base
		}
		owner.mu.Unlock()
		if relay == nil || len(base.Codecs) == 0 {
			return
		}
		body = base.Build(host, relay.LegPort(lk.relayLeg), dir)
	} else {
		body = withDirection(lk.answer, dir)
	}
	if len(body) == 0 {
		return
	}
	ok := false
	switch hom := c.homed(); {
	case hom != nil:
		_, ok = s.haReinvite(hom.leg(false), body)
	case lk.selfSide == cdr.SideCaller:
		_, ok = reinviteA(dss, body)
	case w != nil:
		_, ok = reinvite(w.session(), body)
		if ok {
			ackLate(w.session(), nil)
		}
	}
	if !ok {
		c.addTrace("The other party's hold could not be signalled to this phone")
	}
}

// --- slot announcement -----------------------------------------------------

// The spoken slot announcement is built from announcements the console
// generates once from text (POST /api/v1/park-lots/announcement/generate):
// the lead-in, then one clip per digit. Nothing is synthesized at call time.
const parkSlotPrompt = "park.slot"

// parkDigitPrompt is the announcement name of one spoken digit.
func parkDigitPrompt(d byte) string { return "park.digit." + string(d) }

// slotSpeech returns the spoken announcement of slot: the lead-in and its
// digits, joined. ok is false (and the caller plays tones) when object
// storage is not configured, or any clip is missing, unreadable or at a
// different sample rate than the lead-in.
func (s *Server) slotSpeech(slot string) (pcm []byte, rate int, ok bool) {
	objs := s.deps.Objects
	if objs == nil || s.deps.Snapshots == nil || slot == "" {
		return nil, 0, false
	}
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return nil, 0, false
	}
	names := []string{parkSlotPrompt}
	for i := 0; i < len(slot); i++ {
		if slot[i] < '0' || slot[i] > '9' {
			return nil, 0, false
		}
		names = append(names, parkDigitPrompt(slot[i]))
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	defer cancel()
	clips := map[string][]byte{} // a slot like 707 fetches each digit once
	for i, name := range names {
		obj, found := snap.Announcement(name)
		if !found {
			return nil, 0, false
		}
		clip, seen := clips[name]
		if !seen {
			raw, err := objs.GetAnnouncement(ctx, obj)
			if err != nil {
				s.log.Warn("park: slot announcement clip unavailable, playing tones", "announcement", name, "error", err)
				return nil, 0, false
			}
			var r int
			clip, r, err = media.DecodeWAV(raw)
			if err != nil || (i > 0 && r != rate) {
				s.log.Warn("park: slot announcement clip unusable, playing tones", "announcement", name)
				return nil, 0, false
			}
			rate = r
			clips[name] = clip
		}
		pcm = append(pcm, clip...)
		pcm = append(pcm, make([]byte, rate/10*2)...) // 100 ms between words
	}
	return pcm, rate, true
}

// --- tones -----------------------------------------------------------------

// slotTones is the slot announcement: each digit as its DTMF tone pair.
func slotTones(slot string) []byte {
	freq := map[byte][2]float64{
		'1': {697, 1209}, '2': {697, 1336}, '3': {697, 1477},
		'4': {770, 1209}, '5': {770, 1336}, '6': {770, 1477},
		'7': {852, 1209}, '8': {852, 1336}, '9': {852, 1477},
		'0': {941, 1336},
	}
	const rate = 8000
	tone := int(parkAnnounceDigitTone.Seconds() * rate)
	gap := rate / 10
	var out []byte
	for i := 0; i < len(slot); i++ {
		f, ok := freq[slot[i]]
		if !ok {
			continue
		}
		for n := 0; n < tone; n++ {
			v := 6000 * (math.Sin(2*math.Pi*f[0]*float64(n)/rate) + math.Sin(2*math.Pi*f[1]*float64(n)/rate))
			s := uint16(int16(v)) //nolint:gosec // |v| <= 12000, within int16
			out = append(out, byte(s&0xff), byte(s>>8))
		}
		out = append(out, make([]byte, gap*2)...)
	}
	return out
}
