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
	haRecordDetailPrefix = "announcement:"
)

// noteHAState records a replicated phase change ("" returns to talking) and
// schedules a replication write off the SIP path. detail carries the phase's
// recovery information (an announcement's object name).
func (c *call) noteHAState(phase, detail string) {
	if c.s.deps.HAState == nil {
		return
	}
	c.mu.Lock()
	c.haPhase, c.haDetail = phase, detail
	c.mu.Unlock()
	go c.replicate(phase)
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
	c.mu.Lock()
	c.haLegs = [2]haLegSnap{
		{
			callID:   c.callID,
			localTag: tagParam(dss.InviteResponse.To()), remoteTag: tagParam(c.inv.From()),
			// The next CSeq we would send with: Hello's in-dialog requests
			// on a leg continue the dialog's INVITE CSeq (contract 2).
			localCSeq: c.inv.CSeq().SeqNo + 1, remoteCSeq: c.inv.CSeq().SeqNo,
			routes:       headerValues(c.inv, "Record-Route"),
			localID:      uriString(*c.inv.To().Address.Clone()),
			remoteID:     uriString(*c.inv.From().Address.Clone()),
			remoteTarget: uriString(c.target()),
			endpoint:     c.s.aor(c.callerNum),
			source:       c.inv.Source(),
			sdp:          relaySDPDir(host, c.relay, legCaller, offer, dirs[0]),
		},
		{
			callID:   w.callID,
			localTag: tagParam(dcs.InviteRequest.From()), remoteTag: tagParam(dcs.InviteResponse.To()),
			localCSeq: dcs.InviteRequest.CSeq().SeqNo + 1, remoteCSeq: dcs.InviteRequest.CSeq().SeqNo,
			routes:       headerValues(dcs.InviteResponse, "Record-Route"),
			localID:      uriString(*dcs.InviteRequest.From().Address.Clone()),
			remoteID:     uriString(*dcs.InviteRequest.To().Address.Clone()),
			remoteTarget: uriString(w.target()),
			endpoint:     w.binding.AOR,
			source:       dcs.InviteResponse.Source(),
			sdp:          relaySDPDir(host, c.relay, legCallee, offer, dirs[1]),
		},
	}
	c.mu.Unlock()
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
	detail := c.haDetail
	legs := c.haLegs
	contact := contactURI(c.s)
	c.mu.Unlock()
	if yielded || !anchored || relay == nil || legs[0].callID == "" || legs[1].callID == "" {
		return livestate.DialogState{}, false
	}
	return livestate.DialogState{
		CallID:      legs[0].callID,
		OwnerNode:   c.s.cfg.NodeID,
		Correlation: c.id,
		State:       c.haStateName(),
		StateDetail: detail,
		RelayPorts:  [2]int{relay.LegPort(legCaller), relay.LegPort(legCallee)},
		Legs: [2]livestate.DialogLeg{
			haSnapLeg(legs[0], contact),
			haSnapLeg(legs[1], contact),
		},
	}, true
}

// haSnapLeg renders a snapshot leg as its replicated form.
func haSnapLeg(l haLegSnap, contact string) livestate.DialogLeg {
	return livestate.DialogLeg{
		CallID: l.callID, LocalTag: l.localTag, RemoteTag: l.remoteTag,
		LocalCSeq: l.localCSeq, RemoteCSeq: l.remoteCSeq,
		RouteSet: l.routes, Contact: contact,
		RemoteTarget: l.remoteTarget, SDP: l.sdp,
		Endpoint: l.endpoint, Source: l.source,
		LocalIdentity: l.localID, RemoteIdentity: l.remoteID,
	}
}

// haHomedState builds the replicated state of a taken-over call from its
// rebuilt legs: a homed call has no sipgo sessions to read the dialogs from,
// so its own leg records and relay are the source of truth.
func (c *call) haHomedState(hom *homedCall) (livestate.DialogState, bool) {
	c.mu.Lock()
	relay, anchored, yielded := c.relay, c.anchored, c.haYielded
	detail, host := c.haDetail, c.anchorHost
	contact := contactURI(c.s)
	c.mu.Unlock()
	if yielded || !anchored || relay == nil {
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
		RelayPorts:  [2]int{relay.LegPort(legCaller), relay.LegPort(legCallee)},
	}
	for i, l := range hom.legs {
		l.mu.Lock()
		leg := livestate.DialogLeg{
			CallID: l.callID, LocalTag: l.localTag, RemoteTag: l.remoteTag,
			LocalCSeq: l.localCSeq, RemoteCSeq: l.remoteCSeq,
			RouteSet: l.routes, Contact: contact,
			RemoteTarget: l.remoteTarget,
			SDP:          relaySDPDir(host, relay, legName(i == 1), off, media.SDPDirection([]byte(l.sdp))),
			Endpoint:     l.endpoint, Source: l.source,
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
func (c *call) replicate(detail string) {
	if c.s.deps.HAState == nil {
		return
	}
	st, ok := c.haState()
	if !ok {
		return
	}
	if detail != "" {
		st.StateDetail = detail
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*c.s.cfg.StateTimeout)
	defer cancel()
	if err := c.s.deps.HAState.SaveDialogState(ctx, st, livestate.DialogTTL); err != nil {
		c.s.m.DialogReplicated.WithLabelValues("failed").Inc()
		c.s.log.Warn("dialog replication failed", "correlation_id", c.id, "error", err)
		return
	}
	c.s.m.DialogReplicated.WithLabelValues("ok").Inc()
}

// haLoop is the replication heartbeat: its writes are the owner's
// liveness (the record's TTL outlives three of them), and it yields the
// call when another node has claimed it — a slow owner reappearing after a
// membership blip must not fight its taker (spec edge case).
func (c *call) haLoop() {
	c.replicate("established")
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
			if taker := c.s.haClaimedBy(c.callID); taker != "" && taker != c.s.cfg.NodeID {
				c.haYield(taker)
				return
			}
			c.replicate("")
		}
	}
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
	c.mu.Unlock()
	c.addTrace("Call taken over by " + taker + ": this node yields")
	c.s.log.Info("yielding a taken-over call", "correlation_id", c.id, "taker", taker)
	c.end(sip.StatusOK, cdr.SideSystem, "taken over by "+taker, ResultAnswered)
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
