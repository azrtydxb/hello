// In-call HA of parked calls (phone-feature-parity spec S-6, incall-ha.md).
// A call waiting in a park slot replicates like any anchored call, as a
// solo state: the held party's dialog, the relay's ports and the park
// record, so a surviving node can re-INVITE the party onto its own relay,
// put the slot back under its own name and carry on holding and returning
// the call. A call that has been retrieved, picked up or returned is a
// bridge between two calls and is not replicated: it ends with its node.
package sip

import (
	"encoding/json"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// haPhasePark is the replicated phase of a parked call.
const haPhasePark = "park"

// parkDetail is what a taker needs to carry on holding a parked call; it
// rides in the replicated state's detail.
type parkDetail struct {
	Slot           string `json:"slot"`
	Lot            string `json:"lot"`
	ReturnSecs     int    `json:"returnSecs"`
	VoicemailSecs  int    `json:"voicemailSecs"`
	Parker         string `json:"parker"`
	ParkedIsCaller bool   `json:"parkedIsCaller"`
	ReturnAtMs     int64  `json:"returnAtMs"`
	ParkedAtMs     int64  `json:"parkedAtMs"`
	Party          string `json:"party"`
	PartyName      string `json:"partyName"`
}

// haParkState is the replicated state of a call waiting in its slot; ok is
// false for any other call.
func (c *call) haParkState() (livestate.DialogState, bool) {
	c.mu.Lock()
	pk, relay, yielded, lk := c.park, c.relay, c.haYielded, c.link
	legs := c.haLegs
	contact := contactURI(c.s)
	caller, dest, host := c.callerNum, c.dialled, c.anchorHost
	handoff := c.haHandoff
	hom := c.homedCall
	phase := ""
	if pk != nil {
		phase = pk.phase
	}
	c.mu.Unlock()
	if pk == nil || relay == nil || yielded || phase != phaseParked || lk == nil {
		return livestate.DialogState{}, false
	}
	detail, err := json.Marshal(parkDetail{
		Slot: pk.slot, Lot: pk.lot.Name, ReturnSecs: pk.lot.ReturnSecs, VoicemailSecs: pk.lot.VoicemailSecs,
		Parker: pk.parker, ParkedIsCaller: pk.parkedIsCaller, ReturnAtMs: pk.returnAt.UnixMilli(),
		ParkedAtMs: pk.parkedAt.UnixMilli(), Party: pk.party, PartyName: pk.partyName,
	})
	if err != nil {
		return livestate.DialogState{}, false
	}
	var leg livestate.DialogLeg
	switch {
	case hom != nil:
		l := hom.legs[0]
		if l == nil {
			return livestate.DialogState{}, false
		}
		off, ok := haOffer(l, l)
		if !ok {
			return livestate.DialogState{}, false
		}
		l.mu.Lock()
		leg = livestate.DialogLeg{
			CallID: l.callID, LocalTag: l.localTag, RemoteTag: l.remoteTag,
			LocalCSeq: l.localCSeq, RemoteCSeq: l.remoteCSeq,
			RouteSet: l.routes, Contact: contact,
			RemoteTarget: l.remoteTarget, SDP: relaySDPDir(host, relay, lk.relayLeg, off, "sendrecv"), RemoteSDP: l.remoteSDP,
			Endpoint: l.endpoint, Source: l.source,
			LocalIdentity: l.localID, RemoteIdentity: l.remoteID,
		}
		l.mu.Unlock()
	default:
		snap := legs[1]
		if pk.parkedIsCaller {
			snap = legs[0]
		}
		if snap.callID == "" {
			return livestate.DialogState{}, false
		}
		leg = c.haSnapLeg(snap, contact)
	}
	st := livestate.DialogState{
		CallID:      c.callID,
		OwnerNode:   c.s.cfg.NodeID,
		Correlation: c.id,
		State:       haPhasePark,
		StateDetail: string(detail),
		Legs:        [2]livestate.DialogLeg{leg},
		Caller:      caller,
		Destination: dest,
		Handoff:     handoff,
		AnsweredAt:  c.maxClockStart(),
		RelayPorts:  [2]int{relay.LegPort(legCaller), relay.LegPort(legCallee)},
	}
	c.haContinuity(&st)
	return st, true
}

// takeOverPark re-homes a parked call whose node died: the held party is
// re-INVITEd onto a relay on this node, the slot is put under this node's
// name and the call goes on holding, returning and answering retrieves here.
func (s *Server) takeOverPark(st livestate.DialogState, from string) {
	start := time.Now()
	var d parkDetail
	if json.Unmarshal([]byte(st.StateDetail), &d) != nil {
		s.haAbandon(st, from, "unreadable park detail")
		return
	}
	party := haLegFrom(st.Legs[0], d.ParkedIsCaller)
	if party == nil {
		s.haAbandon(st, from, "incomplete replicated state")
		return
	}
	off, ok := haOffer(party, party)
	if !ok {
		s.haAbandon(st, from, "replicated SDP unusable")
		return
	}
	relay, err := media.NewRelay(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		s.haAbandon(st, from, "relay ports: "+err.Error())
		return
	}
	for _, leg := range []string{legCaller, legCallee} {
		if _, err := relay.AddLeg(leg); err != nil {
			relay.Close()
			s.haAbandon(st, from, "relay leg: "+err.Error())
			return
		}
		relay.SetCodecs(leg, off.Codecs, off.DTMFPayloadType)
	}
	partyLeg, parkerLeg, selfSide := legCaller, legCallee, cdr.SideCaller
	if !d.ParkedIsCaller {
		partyLeg, parkerLeg, selfSide = legCallee, legCaller, cdr.SideCallee
	}
	host := s.anchorHostOr(off.Address)
	c := s.haNewCall(st, from, party, nil, start)
	c.callID = st.CallID
	lot := d.lot()
	if snap := s.deps.Snapshots.Current(); snap != nil {
		for _, l := range snap.ParkLots() {
			if l.Name == d.Lot {
				lot = l
			}
		}
	}
	pk := &parkState{slot: d.Slot, lot: lot, parker: d.Parker, parkedIsCaller: d.ParkedIsCaller,
		phase: phaseParked, stop: make(chan struct{}), partyCallID: party.callID,
		parkedAt: time.UnixMilli(d.ParkedAtMs), returnAt: time.UnixMilli(d.ReturnAtMs), party: d.Party, partyName: d.PartyName}
	c.mu.Lock()
	c.anchorHost, c.anchored, c.anchorReason, c.relay = host, true, AnchorPolicy, relay
	c.rec = &recording{}
	c.park = pk
	c.link = &callLink{selfSide: selfSide, byeSelf: c.partyBye, relayOwner: c, relayLeg: partyLeg, base: off}
	c.mu.Unlock()
	relay.Start()
	if m := s.deps.Media; m != nil {
		m.NoteStart()
	}
	body := []byte(relaySDPDir(host, relay, partyLeg, off, "sendrecv"))
	s.log.Info("takeover re-INVITE", "call_id", st.CallID, "leg", partyLeg, "to", party.destination(s), "app", haPhasePark)
	res, ok := s.haReinvite(party, body)
	if !ok {
		s.log.Warn("takeover leg failed", "call_id", st.CallID, "leg", partyLeg, "res", resStatus(res))
		c.addTrace("Takeover from " + from + " failed: the parked party did not answer the re-INVITE")
		c.mu.Lock()
		c.park = nil
		c.mu.Unlock()
		c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "takeover failed: the parked party did not answer the re-INVITE", ResultFailed)
		s.m.ZombieCalls.Inc()
		return
	}
	haAim(relay, partyLeg, res.Body())
	party.mu.Lock()
	party.sdp = string(body)
	party.mu.Unlock()
	now := time.Now()
	gap, since := haGap(st, start, now)
	s.log.Info("takeover re-homed", "call_id", st.CallID, "from", from, "took", now.Sub(start).String(), "gap", gap.String(), "app", haPhasePark)
	c.addHATrace(from, now.Sub(start), gap, since)
	c.addTrace("The parked call stays in slot " + d.Slot + " on " + s.cfg.NodeID)
	s.m.DialogTakeovers.Inc()
	s.bind(party.callID, dialogRef{c: c})
	if st.CallID != party.callID {
		s.bind(st.CallID, dialogRef{c: c})
	}
	s.m.ActiveCalls.Inc()
	c.publish()
	go c.heartbeat()
	c.mu.Lock()
	c.haClaim = st.CallID
	c.mu.Unlock()
	c.haArmMaxDuration(st.AnsweredAt, start)
	// The slot is this node's now; a lapsed record is put back.
	rec := livestate.ParkedCall{Slot: d.Slot, Lot: d.Lot, Node: s.cfg.NodeID, CallID: party.callID, Correlation: c.id,
		ParkedBy: d.Parker, Caller: d.Party, CallerName: d.PartyName, ParkedAt: time.UnixMilli(d.ParkedAtMs),
		ReturnAt: time.UnixMilli(d.ReturnAtMs), Phase: livestate.ParkParked}
	if s.deps.Park != nil {
		pctx, cancel := s.stateCtx()
		if ok, _ := s.deps.Park.ParkUpdate(pctx, rec, parkRecordTTL); !ok {
			_, _ = s.deps.Park.ParkReserve(pctx, rec, parkRecordTTL)
		}
		cancel()
		s.slotChanged(d.Slot)
	}
	c.haSettleClaim(c.replicate())
	go c.haLoop()
	go c.parkHold(pk, relay, partyLeg, parkerLeg, rec.ReturnAt)
}

// lot rebuilds the park lot a taker holds a call for when the snapshot has
// no lot of that name any more.
func (d parkDetail) lot() snapshot.ParkLot {
	return snapshot.ParkLot{Name: d.Lot, ReturnSecs: d.ReturnSecs, VoicemailSecs: d.VoicemailSecs}
}
