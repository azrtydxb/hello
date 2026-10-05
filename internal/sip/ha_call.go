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

// haState builds the call's replicated recovery state (contract 1); ok is
// false while the call is not a recoverable connected two-leg call: an
// announcement or voicemail call has no second endpoint to re-INVITE.
func (c *call) haState() (livestate.DialogState, bool) {
	c.mu.Lock()
	w, dss, relay, anchored, yielded := c.winner, c.dss, c.relay, c.anchored, c.haYielded
	detail := c.haDetail
	c.mu.Unlock()
	if yielded || !anchored || relay == nil || dss == nil || w == nil {
		return livestate.DialogState{}, false
	}
	dcs := w.session()
	if dcs == nil || dcs.InviteRequest == nil || dcs.InviteResponse == nil {
		return livestate.DialogState{}, false
	}
	offer, _ := media.ParseAudioSDP(c.inv.Body())
	host := c.anchorHost
	state := livestate.DialogState{
		CallID:      c.callID,
		OwnerNode:   c.s.cfg.NodeID,
		Correlation: c.id,
		State:       c.haStateName(),
		StateDetail: detail,
		RelayPorts:  [2]int{relay.LegPort(legCaller), relay.LegPort(legCallee)},
		Legs: [2]livestate.DialogLeg{
			{
				CallID:    c.callID,
				LocalTag:  tagParam(dss.InviteResponse.To()),
				RemoteTag: tagParam(c.inv.From()),
				// The next CSeq we would send with: Hello's in-dialog
				// requests on a leg continue the dialog's INVITE CSeq
				// (contract 2's continuity; the taker re-INVITEs with it).
				LocalCSeq:    c.inv.CSeq().SeqNo + 1,
				RemoteCSeq:   c.inv.CSeq().SeqNo,
				RouteSet:     headerValues(c.inv, "Record-Route"),
				Contact:      contactURI(c.s),
				RemoteTarget: uriString(c.target()),
				SDP:          relaySDP(host, relay, legCaller, offer),
				Endpoint:     c.s.aor(c.callerNum),
				Source:       c.inv.Source(),
			},
			{
				CallID:       w.callID,
				LocalTag:     tagParam(dcs.InviteRequest.From()),
				RemoteTag:    tagParam(dcs.InviteResponse.To()),
				LocalCSeq:    dcs.InviteRequest.CSeq().SeqNo + 1,
				RemoteCSeq:   dcs.InviteRequest.CSeq().SeqNo,
				RouteSet:     headerValues(dcs.InviteResponse, "Record-Route"),
				Contact:      contactURI(c.s),
				RemoteTarget: uriString(w.target()),
				SDP:          relaySDP(host, relay, legCallee, offer),
				Endpoint:     w.binding.AOR,
				Source:       dcs.InviteResponse.Source(),
			},
		},
	}
	a := state.Legs[0]
	a.LocalIdentity = uriString(*c.inv.To().Address.Clone())
	a.RemoteIdentity = uriString(*c.inv.From().Address.Clone())
	state.Legs[0] = a
	b := state.Legs[1]
	b.LocalIdentity = uriString(*dcs.InviteRequest.From().Address.Clone())
	b.RemoteIdentity = uriString(*dcs.InviteRequest.To().Address.Clone())
	state.Legs[1] = b
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

// relaySDP is the relay leg's SDP body for the offer's negotiated codecs.
func relaySDP(host string, r *media.Relay, leg string, off media.AudioSDP) string {
	if r == nil {
		return ""
	}
	return string(media.BuildAudioSDP(host, r.LegPort(leg), off.PayloadType, off.DTMFPayloadType, off.DTMFRate))
}

// userOf is the user part of a URI string ("" when absent).
func userOf(raw string) string {
	var u sip.Uri
	if err := sip.ParseUri(raw, &u); err != nil {
		return ""
	}
	return u.User
}
