// In-call HA on the call (incall-ha spec S-1, S-2): dialog replication off
// the SIP transaction path, the owner's heartbeat and yield, and the
// replicated state's builder. The takeover itself is takeover.go.
package sip

import (
	"context"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/emiago/sipgo/sip"
)

// HAState is the dialog-replication store (incall-ha contract 1);
// *livestate.Store satisfies it. Nil disables in-call HA.
type HAState interface {
	SaveDialogState(ctx context.Context, sds livestate.DialogState, ttl time.Duration) error
	DeleteDialogState(ctx context.Context, callId string) error
	ClaimDialog(ctx context.Context, callId, newNode string) (bool, livestate.DialogState, error)
	ReleaseDialogClaim(ctx context.Context, callId string) error
	ClaimOwner(ctx context.Context, callId string) (string, error)
	OrphanedDialogs(ctx context.Context, offlineNode string) ([]livestate.DialogState, error)
	TakenOver(ctx context.Context, node string) (int, error)
	DialogOwner(ctx context.Context, callId string) (string, error)
}

// Membership reports the cluster's nodes (Phase 3 membership);
// *cluster.Store satisfies it. Nil disables takeover.
type Membership interface {
	Members(ctx context.Context) ([]cluster.Member, error)
}

// The replicated call phases beyond the dialog's own hold state (contract
// 1's State enum; "" means talking, held reports hold).
const (
	haPhaseRecording     = "recording"
	haPhaseTransferring  = "transferring"
	haPhaseAnnouncement  = "announcement"
	haPhaseVoicemail     = "voicemail"
	haRecordDetailPrefix = "announcement:"
	// haReferDetailPrefix names the dialog a REFER arrived on while a
	// transfer is in progress ("refer:caller" or "refer:callee"), so a
	// taker can tell that transferor the transfer was abandoned.
	haReferDetailPrefix = "refer:"
	// haVoicemailDetailPrefix carries the voicemail application's restart
	// data: "vm:<leave|retrieve>:<reason>:<box extension>".
	haVoicemailDetailPrefix = "vm:"
)

// noteHAState records a replicated phase change ("" returns to talking) and
// schedules a replication write off the SIP path. detail carries the phase's
// recovery information (an announcement's object name).
func (c *call) noteHAState(phase, detail string) {
	if c.s.deps.HAState == nil {
		return
	}
	c.mu.Lock()
	rec := c.rec
	c.mu.Unlock()
	if phase == "" && rec != nil && rec.isActive() {
		// Back from a hold, a transfer or an announcement while the call
		// records: the recording is what a taker must restart.
		phase = haPhaseRecording
	}
	c.mu.Lock()
	c.haPhase, c.haDetail = phase, detail
	c.mu.Unlock()
	go c.replicate()
}

// haStateName is the replicated call state (contract 1).
func (c *call) haStateName() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch {
	case c.haPhase != "":
		return c.haPhase
	case c.held:
		return "hold"
	}
	return "talking"
}

// homed returns the taken-over call's replicated dialog data (nil while the
// call lives as set up here).
func (c *call) homed() *homedCall {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.homedCall
}

// haLegSnap is one leg's dialog data snapshot, taken at quiescent points
// (the answer's 200 written, an in-dialog transaction finished) — never
// while sipgo's dialog objects are being written.
type haLegSnap struct {
	callID, localTag, remoteTag string
	localCSeq, remoteCSeq       uint32
	routes                      []string
	localID, remoteID           string
	remoteTarget, endpoint      string
	source, sdp                 string
	remoteSDP                   string // the endpoint's own SDP
}

// haRefresh rebuilds the replicated-dialog snapshot from the live dialogs.
// It runs only where the dialogs are quiescent.
func (c *call) haRefresh() {
	if c.s.deps.HAState == nil {
		return
	}
	c.mu.Lock()
	w, dss := c.winner, c.dss
	c.mu.Unlock()
	if dss == nil || w == nil {
		return
	}
	dcs := w.session()
	if dcs == nil || dcs.InviteRequest == nil || dcs.InviteResponse == nil {
		return
	}
	offer, _ := media.ParseAudioSDP(c.inv.Body())
	host := c.anchorHost
	dirs := c.haDirs
	s := c.s
	c.mu.Lock()
	c.haLegs = [2]haLegSnap{
		{
			callID:   c.callID,
			localTag: tagParam(dss.InviteResponse.To()), remoteTag: tagParam(c.inv.From()),
			// The next CSeq we would send with: past the dialog's INVITE,
			// past every request sipgo's session sent on it (relayed
			// re-INVITEs), and past the raw ones (contract 2).
			localCSeq:    s.nextCSeq(c.callID, c.inv.CSeq().SeqNo, dss.CSEQ()),
			remoteCSeq:   c.inv.CSeq().SeqNo,
			routes:       headerValues(c.inv, "Record-Route"),
			localID:      uriString(*c.inv.To().Address.Clone()),
			remoteID:     uriString(*c.inv.From().Address.Clone()),
			remoteTarget: uriString(c.target()),
			endpoint:     c.s.aor(c.callerNum),
			source:       c.inv.Source(),
			sdp:          relaySDPDir(host, c.relay, legCaller, offer, dirs[0]),
			remoteSDP:    string(c.inv.Body()),
		},
		{
			callID:   w.callID,
			localTag: tagParam(dcs.InviteRequest.From()), remoteTag: tagParam(dcs.InviteResponse.To()),
			localCSeq:  s.nextCSeq(w.callID, dcs.InviteRequest.CSeq().SeqNo, dcs.CSEQ()),
			remoteCSeq: dcs.InviteRequest.CSeq().SeqNo,
			// A UAC's route set is the 2xx's Record-Route reversed (RFC
			// 3261 12.1.2): the first entry is the hop next to Hello.
			routes:       reversed(headerValues(dcs.InviteResponse, "Record-Route")),
			localID:      uriString(*dcs.InviteRequest.From().Address.Clone()),
			remoteID:     uriString(*dcs.InviteRequest.To().Address.Clone()),
			remoteTarget: uriString(w.target()),
			endpoint:     w.binding.AOR,
			source:       dcs.InviteResponse.Source(),
			sdp:          relaySDPDir(host, c.relay, legCallee, offer, dirs[1]),
			remoteSDP:    string(dcs.InviteResponse.Body()),
		},
	}
	c.mu.Unlock()
}

// haSoloRefresh takes the dialog snapshot of a call answered by Hello
// itself (voicemail, an announcement destination): one leg, the caller's,
// answered through the raw transaction with vmTag as Hello's tag. sdp is
// the answer Hello gave; the caller's offer rides along, so a taker can
// bind fresh media for the same codecs.
func (c *call) haSoloRefresh(sdp string) {
	if c.s.deps.HAState == nil {
		return
	}
	c.mu.Lock()
	tag := c.vmTag
	c.mu.Unlock()
	if tag == "" || c.inv == nil {
		return
	}
	snap := haLegSnap{
		callID: c.callID, localTag: tag, remoteTag: tagParam(c.inv.From()),
		localCSeq:    c.s.nextCSeq(c.callID, c.inv.CSeq().SeqNo),
		remoteCSeq:   c.inv.CSeq().SeqNo,
		routes:       headerValues(c.inv, "Record-Route"),
		localID:      uriString(*c.inv.To().Address.Clone()),
		remoteID:     uriString(*c.inv.From().Address.Clone()),
		remoteTarget: uriString(c.target()), endpoint: c.s.aor(c.callerNum),
		source: c.inv.Source(), sdp: sdp, remoteSDP: string(c.inv.Body()),
	}
	c.mu.Lock()
	c.haLegs = [2]haLegSnap{snap}
	c.haSolo = true
	c.mu.Unlock()
}

// haSoloPhase reports whether a solo call's phase is one a taker can
// restart: the voicemail application, or an announcement destination's
// playback. Anything else is a solo call about to end on its own.
func haSoloPhase(phase string) bool {
	return phase == haPhaseVoicemail || phase == haPhaseAnnouncement
}

// noteSentCSeq records a CSeq this node sent on a dialog outside sipgo's
// dialog sessions (a transfer's NOTIFYs), so the replicated LocalCSeq
// continues past it: an endpoint rejects an in-dialog request whose CSeq
// does not exceed the last one it saw from this side of the dialog.
func (s *Server) noteSentCSeq(callID string, seq uint32) {
	for {
		old, loaded := s.haSent.LoadOrStore(callID, seq)
		if !loaded {
			return
		}
		prev, _ := old.(uint32)
		if prev >= seq || s.haSent.CompareAndSwap(callID, old, seq) {
			return
		}
	}
}

// nextCSeq is the next local CSeq on a dialog: one past the highest of the
// given counters and the raw requests noteSentCSeq recorded.
func (s *Server) nextCSeq(callID string, counters ...uint32) uint32 {
	var n uint32
	if v, ok := s.haSent.Load(callID); ok {
		n, _ = v.(uint32)
	}
	for _, c := range counters {
		n = max(n, c)
	}
	return n + 1
}

// haState builds the call's replicated recovery state (contract 1); ok is
// false while the call is not a recoverable connected two-leg call: an
// announcement or voicemail call has no second endpoint to re-INVITE.
func (c *call) haState() (livestate.DialogState, bool) {
	if hom := c.homed(); hom != nil {
		return c.haHomedState(hom)
	}
	c.mu.Lock()
	relay, anchored, yielded := c.relay, c.anchored, c.haYielded
	detail, phase, solo, vm := c.haDetail, c.haPhase, c.haSolo, c.vmSession
	legs := c.haLegs
	contact := contactURI(c.s)
	c.mu.Unlock()
	if yielded || legs[0].callID == "" {
		return livestate.DialogState{}, false
	}
	if solo {
		// A one-legged call answered by Hello: recoverable while its
		// application runs (the taker restarts it from the beginning).
		if !haSoloPhase(phase) || (phase == haPhaseVoicemail && vm == nil) ||
			(phase == haPhaseAnnouncement && relay == nil) {
			return livestate.DialogState{}, false
		}
	} else if !anchored || relay == nil || legs[1].callID == "" {
		return livestate.DialogState{}, false
	}
	c.mu.Lock()
	caller, dest := c.callerNum, c.dialled
	c.mu.Unlock()
	c.mu.Lock()
	handoff := c.haHandoff
	c.mu.Unlock()
	st := livestate.DialogState{
		CallID:      legs[0].callID,
		OwnerNode:   c.s.cfg.NodeID,
		Correlation: c.id,
		State:       c.haStateName(),
		StateDetail: detail,
		Legs:        [2]livestate.DialogLeg{c.haSnapLeg(legs[0], contact)},
		Caller:      caller,
		Destination: dest,
		Handoff:     handoff,
	}
	if relay != nil {
		st.RelayPorts = [2]int{relay.LegPort(legCaller), relay.LegPort(legCallee)}
	}
	if !solo {
		st.Legs[1] = c.haSnapLeg(legs[1], contact)
	}
	return st, true
}

// haSnapLeg renders a snapshot leg as its replicated form. The CSeq is
// re-checked against the raw requests sent since the snapshot (a NOTIFY
// after the last quiescent point must not be reused by a taker).
func (c *call) haSnapLeg(l haLegSnap, contact string) livestate.DialogLeg {
	return livestate.DialogLeg{
		CallID: l.callID, LocalTag: l.localTag, RemoteTag: l.remoteTag,
		LocalCSeq: max(l.localCSeq, c.s.nextCSeq(l.callID)), RemoteCSeq: l.remoteCSeq,
		RouteSet: l.routes, Contact: contact,
		RemoteTarget: l.remoteTarget, SDP: l.sdp, RemoteSDP: l.remoteSDP,
		Endpoint: l.endpoint, Source: l.source,
		LocalIdentity: l.localID, RemoteIdentity: l.remoteID,
	}
}

// haHomedState builds the replicated state of a taken-over call from its
// rebuilt legs: a homed call has no sipgo sessions to read the dialogs from,
// so its own leg records and relay are the source of truth.
func (c *call) haHomedState(hom *homedCall) (livestate.DialogState, bool) {
	c.mu.Lock()
	relay, yielded := c.relay, c.haYielded
	detail, host, phase := c.haDetail, c.anchorHost, c.haPhase
	caller, dest := c.callerNum, c.dialled
	contact := contactURI(c.s)
	c.mu.Unlock()
	solo := hom.legs[1] == nil
	// A voicemail call's media is its anchor session, not a relay.
	voicemail := solo && phase == haPhaseVoicemail
	if yielded || (relay == nil && !voicemail) || (solo && !haSoloPhase(phase)) {
		return livestate.DialogState{}, false
	}
	off, ok := haOffer(hom.legs[0], hom.legs[1])
	if !ok {
		return livestate.DialogState{}, false
	}
	state := livestate.DialogState{
		CallID:      hom.legs[0].callID,
		OwnerNode:   c.s.cfg.NodeID,
		Correlation: c.id,
		State:       c.haStateName(),
		StateDetail: detail,
		Caller:      caller,
		Destination: dest,
		Handoff:     c.handingOff(),
	}
	if relay != nil {
		state.RelayPorts = [2]int{relay.LegPort(legCaller), relay.LegPort(legCallee)}
	}
	for i, l := range hom.legs {
		if l == nil {
			continue
		}
		l.mu.Lock()
		sdp := l.sdp
		if relay != nil {
			sdp = relaySDPDir(host, relay, legName(i == 0), off, media.SDPDirection([]byte(l.sdp)))
		}
		leg := livestate.DialogLeg{
			CallID: l.callID, LocalTag: l.localTag, RemoteTag: l.remoteTag,
			LocalCSeq: l.localCSeq, RemoteCSeq: l.remoteCSeq,
			RouteSet: l.routes, Contact: contact,
			RemoteTarget: l.remoteTarget, SDP: sdp, RemoteSDP: l.remoteSDP,
			Endpoint: l.endpoint, Source: l.source,
			LocalIdentity: l.localID, RemoteIdentity: l.remoteID,
		}
		l.mu.Unlock()
		state.Legs[i] = leg
	}
	return state, true
}

// replicate writes the call's recovery state (contract 1). A failure is
// logged and counted, never fatal: an unreplicated call that loses its node
// becomes a counted zombie, honestly (spec S-6).
func (c *call) replicate() {
	if c.s.deps.HAState == nil || !c.s.serving.Load() {
		return // a node shutting down leaves its records to the takers
	}
	c.mu.Lock()
	written := c.haHandoff && c.haHandoffWritten
	c.mu.Unlock()
	if written {
		return // handed off: the record is the taker's to write
	}
	st, ok := c.haState()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*c.s.cfg.StateTimeout)
	defer cancel()
	if err := c.s.deps.HAState.SaveDialogState(ctx, st, livestate.DialogTTL); err != nil {
		c.s.m.DialogReplicated.WithLabelValues("failed").Inc()
		c.s.log.Warn("dialog replication failed", "correlation_id", c.id, "error", err)
		return
	}
	c.s.m.DialogReplicated.WithLabelValues("ok").Inc()
	if st.Handoff {
		c.mu.Lock()
		c.haHandoffWritten = c.haHandoff
		c.mu.Unlock()
	}
}

// handingOff reports whether the call is being handed to a survivor.
func (c *call) handingOff() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.haHandoff
}

// haLoop is the replication heartbeat: its writes are the owner's
// liveness (the record's TTL outlives three of them), and it yields the
// call when another node has claimed it — a slow owner reappearing after a
// membership blip must not fight its taker (spec edge case).
func (c *call) haLoop() {
	c.replicate()
	t := time.NewTicker(c.s.cfg.HADialogHeartbeat)
	defer t.Stop()
	for {
		select {
		case <-c.haStop:
			return
		case <-c.s.done:
			return
		case <-t.C:
			if !c.s.serving.Load() {
				return // the node is shutting down; its calls die with it
			}
			if taker := c.s.haTakenBy(c.callID); taker != "" {
				c.haYield(taker)
				return
			}
			c.replicate()
		}
	}
}

// haTakenBy names the other node that took the call over: the holder of
// its claim, or, once the taker released the claim, the node its record
// names. "" while the call is still this node's (or the store is down).
func (s *Server) haTakenBy(callID string) string {
	if taker := s.haClaimedBy(callID); taker != "" && taker != s.cfg.NodeID {
		return taker
	}
	if s.deps.HAState == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	defer cancel()
	owner, err := s.deps.HAState.DialogOwner(ctx, callID)
	if err != nil || owner == s.cfg.NodeID {
		return ""
	}
	return owner
}

// haClaimedBy names the node holding the dialog's takeover claim ("" when
// none or when the store is unavailable).
func (s *Server) haClaimedBy(callID string) string {
	if s.deps.HAState == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	defer cancel()
	owner, err := s.deps.HAState.ClaimOwner(ctx, callID)
	if err != nil {
		return ""
	}
	return owner
}

// haYield gives a taken-over call up: the taker owns the endpoints' dialogs
// and the replicated record. This node closes its copy — media, metrics,
// CDR — without touching the endpoints or the record, and never writes the
// record again (haDelete honours the yield).
func (c *call) haYield(taker string) {
	c.mu.Lock()
	if c.haYielded || c.ended {
		c.mu.Unlock()
		return
	}
	c.haYielded = true
	legs := []string{c.callID}
	if w := c.winner; w != nil && w.callID != "" {
		legs = append(legs, w.callID)
	}
	if hom := c.homedCall; hom != nil {
		for _, l := range hom.legs {
			if l != nil {
				legs = append(legs, l.callID)
			}
		}
	}
	c.mu.Unlock()
	// In-dialog requests that still reach this node for the call (the
	// edge routes by Record-Route) are answered 503, so the edge retries
	// them on the taker.
	for _, id := range legs {
		c.s.haGone.Store(id, taker)
	}
	c.addTrace("Call taken over by " + taker + ": this node yields")
	c.s.log.Info("yielding a taken-over call", "correlation_id", c.id, "taker", taker)
	c.end(sip.StatusOK, cdr.SideSystem, "taken over by "+taker, ResultAnswered)
	// The dialogs are the taker's now: nothing here may answer for them
	// (or BYE the endpoints when a stray request arrives).
	c.release()
}

// haDelete removes the replicated record of an ended call, so its dialog is
// never offered to takers. A yielded call's record belongs to its taker.
func (c *call) haDelete() {
	c.mu.Lock()
	yielded := c.haYielded
	c.mu.Unlock()
	if yielded || c.s.deps.HAState == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*c.s.cfg.StateTimeout)
	defer cancel()
	if err := c.s.deps.HAState.DeleteDialogState(ctx, c.callID); err != nil {
		c.s.log.Warn("could not remove replicated dialog", "correlation_id", c.id, "error", err)
	}
}

// --- replicated-state plumbing ----------------------------------------------

// headerValues collects one header's values in order.
func headerValues(m sip.Message, name string) []string {
	var out []string
	for _, h := range m.GetHeaders(name) {
		out = append(out, h.Value())
	}
	return out
}

// reversed returns a reversed copy of a list.
func reversed(in []string) []string {
	out := make([]string, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}

// contactURI is the node's Contact as a URI string.
func contactURI(s *Server) string { return uriString(s.contact.Address) }

// uriString renders a URI without display name.
func uriString(u sip.Uri) string {
	if u.String() == "" {
		return ""
	}
	return (&u).String()
}

// relaySDPDir is relaySDP with an explicit direction attribute (a hold's
// sendonly/recvonly pair), so a taker's re-INVITEs keep the hold.
func relaySDPDir(host string, r *media.Relay, leg string, off media.AudioSDP, dir string) string {
	if r == nil {
		return ""
	}
	if dir == "" || dir == "sendrecv" {
		return string(media.BuildAudioSDP(host, r.LegPort(leg), off.PayloadType, off.DTMFPayloadType, off.DTMFRate))
	}
	return string(media.BuildAudioSDPDir(host, r.LegPort(leg), off.PayloadType, off.DTMFPayloadType, off.DTMFRate, dir))
}

// userOf is the user part of a URI string ("" when absent).
func userOf(raw string) string {
	var u sip.Uri
	if err := sip.ParseUri(raw, &u); err != nil {
		return ""
	}
	return u.User
}

// --- handoff on drain (incall-ha addition) -----------------------------------

// handoffPoll is how often a draining node checks whether a survivor has
// taken over the calls it handed off; a var so tests can shrink it.
var handoffPoll = 100 * time.Millisecond

// HandOffCalls hands every recoverable live call to a surviving node: the
// call's record is marked for handoff (survivors claim such dialogs of a
// DRAINING node at once), and once a survivor has re-homed a call this
// node yields its copy without touching the endpoints. A drain then ends
// as soon as the calls are handed over instead of at the drain timeout;
// calls nobody takes stay here and the drain timeout still applies.
func (s *Server) HandOffCalls() {
	if s.deps.HAState == nil || !s.cfg.HATakeoverEnabled {
		return
	}
	s.mu.Lock()
	calls := make([]*call, 0, len(s.calls))
	for c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	var handed []*call
	for _, c := range calls {
		if _, ok := c.haState(); !ok {
			continue // not recoverable (ringing, unreplicated): it stays
		}
		c.mu.Lock()
		c.haHandoff, c.haHandoffWritten = true, false
		c.mu.Unlock()
		c.addTrace("Node draining: call handed off for takeover")
		c.replicate()
		handed = append(handed, c)
	}
	if len(handed) > 0 {
		s.goBG(func() { s.handoffLoop(handed) })
	}
}

// handoffLoop yields each handed-off call once a survivor took it over,
// until none is left, the node stops, or the drain is cancelled.
func (s *Server) handoffLoop(calls []*call) {
	t := time.NewTicker(handoffPoll)
	defer t.Stop()
	for len(calls) > 0 {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		left := calls[:0]
		for _, c := range calls {
			c.mu.Lock()
			done := c.ended || c.haYielded || !c.haHandoff
			c.mu.Unlock()
			if done {
				continue
			}
			if taker := s.haTakenBy(c.callID); taker != "" {
				c.haYield(taker)
				continue
			}
			left = append(left, c)
		}
		calls = left
	}
}

// CancelHandOff takes back the calls not yet taken over when a drain is
// cancelled: their records lose the handoff mark and replicate as before.
func (s *Server) CancelHandOff() {
	s.mu.Lock()
	calls := make([]*call, 0, len(s.calls))
	for c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	for _, c := range calls {
		c.mu.Lock()
		was := c.haHandoff && !c.haYielded
		c.haHandoff, c.haHandoffWritten = false, false
		c.mu.Unlock()
		if was {
			c.addTrace("Drain cancelled: the call stays on this node")
			go c.replicate()
		}
	}
}

// handedOff answers an in-dialog request for a call this node handed to
// another node with 503, so the edge retries it on a surviving node (its
// in-dialog failure route); true when it answered.
func (s *Server) handedOff(req *sip.Request, tx sip.ServerTransaction) bool {
	if req.IsAck() || tx == nil {
		return false
	}
	if _, ok := req.To().Params.Get("tag"); !ok {
		return false
	}
	id := req.CallID().Value()
	if _, gone := s.haGone.Load(id); !gone {
		return false
	}
	if _, bound := s.lookup(id); bound {
		return false
	}
	s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable", sip.NewHeader("Retry-After", "0"))
	return true
}
