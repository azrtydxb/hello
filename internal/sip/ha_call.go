// In-call HA on the call (incall-ha spec S-1, S-2): dialog replication off
// the SIP transaction path, the owner's heartbeat and yield, and the
// replicated state's builder. The takeover itself is takeover.go.
package sip

import (
	"context"
	"fmt"
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
	// ClaimDialog claims an orphaned dialog; staleIncarnation, when set,
	// names the dead owner process whose record may be claimed at once.
	ClaimDialog(ctx context.Context, callId, newNode, staleIncarnation string) (bool, livestate.DialogState, error)
	// DialogByLeg finds the record carrying either leg's dialog.
	DialogByLeg(ctx context.Context, callId string) (livestate.DialogState, bool, error)
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
		AnsweredAt:  c.maxClockStart(),
	}
	c.haContinuity(&st)
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
	st := livestate.DialogState{
		CallID:      hom.legs[0].callID,
		OwnerNode:   c.s.cfg.NodeID,
		Correlation: c.id,
		State:       c.haStateName(),
		StateDetail: detail,
		Caller:      caller,
		Destination: dest,
		Handoff:     c.handingOff(),
		AnsweredAt:  c.maxClockStart(),
	}
	c.haContinuity(&st)
	if relay != nil {
		st.RelayPorts = [2]int{relay.LegPort(legCaller), relay.LegPort(legCallee)}
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
		st.Legs[i] = leg
	}
	return st, true
}

// haContinuity stamps the owner process and the call's CDR continuity on
// its replicated state: the incarnation tells a restarted node's calls from
// its live ones, and the start, ring and routing data let a taker close the
// one logical call with one CDR that starts where the call started.
func (c *call) haContinuity(st *livestate.DialogState) {
	st.OwnerIncarnation = c.s.cfg.Incarnation
	c.mu.Lock()
	defer c.mu.Unlock()
	st.StartedAt, st.RingAt = c.start, c.ringTime
	st.Direction, st.Route, st.Trunk, st.Rewritten = c.direction, c.route, c.trunkName, c.rewritten
	st.Trace = make([]string, 0, len(c.trace))
	for _, step := range c.trace {
		st.Trace = append(st.Trace, step.Text)
	}
}

// replicate writes the call's recovery state (contract 1). A failure is
// logged and counted, never fatal: an unreplicated call that loses its node
// becomes a counted zombie, honestly (spec S-6).
func (c *call) replicate() bool {
	if c.s.deps.HAState == nil || !c.s.serving.Load() {
		return false // a node shutting down leaves its records to the takers
	}
	c.mu.Lock()
	written := c.haHandoff && c.haHandoffWritten
	c.mu.Unlock()
	if written {
		return false // handed off: the record is the taker's to write
	}
	st, ok := c.haState()
	if !ok {
		return false
	}
	c.haWriteMu.Lock()
	defer c.haWriteMu.Unlock()
	if c.haDeleted {
		return false // the call ended: its record is gone for good
	}
	c.mu.Lock()
	written = c.haHandoff && c.haHandoffWritten
	c.mu.Unlock()
	if written && !st.Handoff {
		// A heartbeat that built its state before the handoff mark must
		// not land after the handoff write: nothing writes the record
		// again, so survivors would never see the handoff.
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*c.s.cfg.StateTimeout)
	defer cancel()
	if err := c.s.deps.HAState.SaveDialogState(ctx, st, livestate.DialogTTL); err != nil {
		c.s.m.DialogReplicated.WithLabelValues("failed").Inc()
		c.s.log.Warn("dialog replication failed", "correlation_id", c.id, "error", err)
		return false
	}
	c.s.m.DialogReplicated.WithLabelValues("ok").Inc()
	if st.Handoff {
		c.mu.Lock()
		c.haHandoffWritten = c.haHandoff
		c.mu.Unlock()
	}
	return true
}

// haSettleClaim releases a taker's takeover claim once a replication write
// (written) names this node as the record's owner; until then the claim
// stays, so no survivor can take the call a second time.
func (c *call) haSettleClaim(written bool) {
	if !written {
		return
	}
	c.mu.Lock()
	id := c.haClaim
	c.haClaim = ""
	c.mu.Unlock()
	if id != "" {
		c.s.releaseClaim(id)
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
	c.haSettleClaim(c.replicate())
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
			c.haSettleClaim(c.replicate())
		}
	}
}

// haTakenBy names the other node that took the call over: the holder of
// its claim, or, once the taker released the claim, the node its record
// names. "" while the call is still this node's (or the store is down).
func (s *Server) haTakenBy(callID string) string {
	switch taker := s.haClaimedBy(callID); taker {
	case "":
	case s.cfg.NodeID:
		// This node's own claim: it took the call over and has not yet
		// written a record naming itself, which still names the dead
		// owner. The call is this node's; it must not yield to the dead.
		return ""
	default:
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
	yielded, shared := c.haYielded, c.haShared
	c.mu.Unlock()
	if yielded || shared || c.s.deps.HAState == nil {
		return
	}
	c.haWriteMu.Lock()
	defer c.haWriteMu.Unlock()
	c.haDeleted = true
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

// HandOffCalls hands every recoverable live call to a surviving node,
// for as long as the node drains: the call's record is marked for handoff
// (survivors claim such dialogs of a DRAINING node at once), and once a
// survivor has re-homed a call this node yields its copy without touching
// the endpoints. A call that becomes recoverable during the drain (a
// ringing call answered, a caller reaching voicemail) is handed off when
// it does. The drain then ends as soon as the calls are handed over
// instead of at the drain timeout; calls nobody takes stay here and the
// drain timeout still applies.
func (s *Server) HandOffCalls() {
	if s.deps.HAState == nil || !s.cfg.HATakeoverEnabled {
		return
	}
	if s.handoff.Swap(true) {
		return // already handing off
	}
	s.handoffMark()
	s.goBG(s.handoffLoop)
}

// handoffMark marks every recoverable call not yet marked.
func (s *Server) handoffMark() {
	for _, c := range s.liveCalls() {
		c.mu.Lock()
		skip := c.haHandoff || c.haYielded || c.ended
		c.mu.Unlock()
		if skip {
			continue
		}
		if _, ok := c.haState(); !ok {
			continue // not recoverable (yet): ringing, unreplicated
		}
		c.mu.Lock()
		c.haHandoff, c.haHandoffWritten = true, false
		c.mu.Unlock()
		c.addTrace("Node draining: call handed off for takeover")
		c.replicate()
	}
}

// liveCalls snapshots the node's calls.
func (s *Server) liveCalls() []*call {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := make([]*call, 0, len(s.calls))
	for c := range s.calls {
		calls = append(calls, c)
	}
	return calls
}

// handoffLoop yields each handed-off call once a survivor took it over and
// marks calls that became recoverable, until the node stops or the drain
// is cancelled.
func (s *Server) handoffLoop() {
	t := time.NewTicker(handoffPoll)
	defer t.Stop()
	for s.handoff.Load() {
		select {
		case <-s.done:
			return
		case <-t.C:
		}
		if !s.handoff.Load() {
			return
		}
		s.handoffMark()
		for _, c := range s.liveCalls() {
			c.mu.Lock()
			pending := c.haHandoff && c.haHandoffWritten && !c.ended && !c.haYielded
			c.mu.Unlock()
			if !pending {
				continue
			}
			if taker := s.haTakenBy(c.callID); taker != "" {
				c.haYield(taker)
			}
		}
	}
}

// CancelHandOff takes back the calls not yet taken over when a drain is
// cancelled: their records lose the handoff mark and replicate as before.
func (s *Server) CancelHandOff() {
	s.handoff.Store(false)
	for _, c := range s.liveCalls() {
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

// haOnDemand takes a call over when an in-dialog request for it reaches
// this node and the node holds no such dialog, but a replicated record
// does, and its owner process is gone (incall-ha: an in-dialog request is
// never 481-ed while the call is recoverable). The decisive case is a node
// restarted in place under the same ID: Kamailio keeps routing the call's
// requests to it, and its new process must answer them from the record
// instead of 481 (the old process's calls never go OFFLINE). A BYE ends
// the call right here - 200, the other leg's BYE, the CDR - without
// re-INVITEs; a re-INVITE or UPDATE starts the full takeover and is
// answered 491 (the endpoint retries once the takeover re-INVITEd it);
// anything else starts it and gets 503. With the owner alive (or another
// node's claim in flight) the request gets 503 + Retry-After, never 481,
// so it is retried once the call is re-homed. true when it answered.
func (s *Server) haOnDemand(req *sip.Request, tx sip.ServerTransaction) bool {
	if s.deps.HAState == nil || !s.cfg.HATakeoverEnabled || req.IsAck() || tx == nil {
		return false
	}
	switch req.Method {
	case sip.BYE, sip.INVITE, sip.UPDATE, sip.INFO, sip.REFER:
	default:
		return false
	}
	if _, ok := req.To().Params.Get("tag"); !ok {
		return false // not in-dialog
	}
	id := req.CallID().Value()
	if _, bound := s.lookup(id); bound {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	st, ok, err := s.deps.HAState.DialogByLeg(ctx, id)
	cancel()
	if err != nil || !ok {
		return false
	}
	// The request must belong to the replicated dialog, from the
	// endpoint's side: its tags, from its source or a trusted edge.
	fromCallee := id != st.CallID
	l := st.Legs[0]
	if fromCallee {
		l = st.Legs[1]
	}
	if l.CallID != id || tagParam(req.From()) != l.RemoteTag || tagParam(req.To()) != l.LocalTag ||
		(req.Source() != l.Source && !s.fromTrustedProxy(req)) {
		return false
	}
	ctx, cancel = context.WithTimeout(context.Background(), 2*s.cfg.StateTimeout)
	stale, dead := s.haOwnerDead(ctx, st)
	cancel()
	if !dead {
		if st.OwnerNode == s.cfg.NodeID && st.OwnerIncarnation == s.cfg.Incarnation {
			return false // this process's own record of a call it no longer holds
		}
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable", sip.NewHeader("Retry-After", "1"))
		return true
	}
	claimed, won := s.haClaim(context.Background(), st.CallID, stale)
	if !won {
		// Another node's takeover is in flight: retried, it lands there.
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable", sip.NewHeader("Retry-After", "1"))
		return true
	}
	s.log.Info("taking over a call on demand", "call_id", st.CallID, "from", st.OwnerNode,
		"method", req.Method.String(), "dead_incarnation", stale)
	switch req.Method {
	case sip.BYE:
		s.haByeOnDemand(claimed, req, tx, fromCallee)
	case sip.INVITE, sip.UPDATE:
		go s.takeOverCall(claimed, claimed.OwnerNode)
		s.respond(tx, req, sip.StatusRequestPending, "Request Pending")
	default:
		go s.takeOverCall(claimed, claimed.OwnerNode)
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable", sip.NewHeader("Retry-After", "1"))
	}
	return true
}

// haByeOnDemand ends a claimed call whose endpoint hung up before any
// takeover re-homed it: the call is rebuilt from the record without media
// (nothing is left to relay), the BYE is answered 200, the other leg gets
// its BYE, and the one CDR of the logical call closes answered with the
// side that hung up.
func (s *Server) haByeOnDemand(st livestate.DialogState, req *sip.Request, tx sip.ServerTransaction, fromCallee bool) {
	defer s.releaseClaim(st.CallID)
	a := haLegFrom(st.Legs[0], true)
	var b *haLeg
	if st.Legs[1].CallID != "" {
		b = haLegFrom(st.Legs[1], false)
	}
	if a == nil || (st.Legs[1].CallID != "" && b == nil) {
		s.haAbandon(st, st.OwnerNode, "incomplete replicated state")
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	now := time.Now()
	c := s.haNewCall(st, st.OwnerNode, a, b, now)
	c.haSolo = b == nil
	c.mediaMode = "anchored"
	c.addTrace(fmt.Sprintf("ha: taken over from %s on demand: the %s hung up before any re-INVITE", st.OwnerNode,
		map[bool]string{false: "caller", true: "callee"}[fromCallee]))
	s.m.DialogTakeovers.Inc()
	s.m.ActiveCalls.Inc()
	hom := c.homed()
	s.bind(a.callID, dialogRef{c: c})
	if b != nil {
		s.bind(b.callID, dialogRef{c: c, leg: c.winner})
	}
	s.haByeRequest(c, hom, fromCallee, req, tx)
}

// maxClockStart is when the call's maximum-duration clock started: the
// original answer of a taken-over call, else this node's answer.
func (c *call) maxClockStart() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.maxFrom.IsZero() {
		return c.maxFrom
	}
	return c.answerTime
}

// haArmMaxDuration arms a taken-over call's maximum-duration timer for
// what remains of it since the call was first answered (answeredAt; zero
// from an older owner, then from start): a taken-over call still clears
// at MaxCallDuration, and a second takeover does not extend it.
func (c *call) haArmMaxDuration(answeredAt, start time.Time) {
	if answeredAt.IsZero() || answeredAt.After(start) {
		answeredAt = start
	}
	left := max(c.s.cfg.MaxCallDuration-start.Sub(answeredAt), 0)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.maxFrom = answeredAt
	if !c.ended && c.maxTimer == nil {
		c.maxTimer = time.AfterFunc(left, c.expire)
	}
}
