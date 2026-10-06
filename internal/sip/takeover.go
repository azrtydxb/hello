// Takeover (incall-ha spec S-2, S-3, S-4): surviving nodes poll for the
// orphaned dialogs of OFFLINE nodes, claim them atomically, re-create both
// legs from the replicated state and re-INVITE each endpoint, so the call
// continues here with a fresh relay. Either leg failing closes the call
// one-sidedly — never a zombie dialog.
package sip

import (
	"context"
	crand "crypto/rand"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// haReapDelay is how long after a node is first seen OFFLINE its
// unrecoverable calls are counted: past the dialog TTL, so every taker had
// its chance. A var so tests can count at once.
var haReapDelay = livestate.DialogTTL + 15*time.Second

// haMediaGap is the takeover target: audio restored within 3s of the claim
// (spec S-4). Exceeding it is logged; the CDR trace records the real gap.
const haMediaGap = 3 * time.Second

// haReinviteTimeout bounds one takeover re-INVITE. The whole takeover
// budget is 3s (spec S-4), so a leg that has not answered by then fails
// the takeover and takes the one-sided close; a var so tests shrink it.
var haReinviteTimeout = 3 * time.Second

// haLeg is one dialog of a taken-over call, carried from the replicated
// state. The endpoint's view of the dialog is unchanged: same Call-ID, same
// tags, the CSeq continuing the old owner's counter, and the same route set
// — the taker's requests go through Kamailio the way the old owner's did.
type haLeg struct {
	callID       string
	localTag     string // Hello's tag on this dialog
	remoteTag    string // the endpoint's tag
	localCSeq    uint32 // the next CSeq we send with
	remoteCSeq   uint32
	routes       []string // the dialog's service route set
	localID      string   // Hello's identity URI in the dialog
	remoteID     string   // the endpoint's identity URI
	remoteTarget string   // where requests to the endpoint go
	endpoint     string   // the endpoint's AOR
	source       string   // where the endpoint's requests come from
	sdp          string   // the last SDP on this leg (Hello's side)
	remoteSDP    string   // the endpoint's own last SDP
	uas          bool     // we are the dialog's UAS side (the caller leg)
	failed       bool     // the takeover re-INVITE got no answer

	mu sync.Mutex
}

// haLegFrom copies a replicated leg; nil when it cannot carry a dialog.
func haLegFrom(d livestate.DialogLeg, uas bool) *haLeg {
	if d.CallID == "" || d.LocalTag == "" || d.RemoteTag == "" || d.RemoteTarget == "" ||
		d.LocalIdentity == "" || d.RemoteIdentity == "" {
		return nil
	}
	return &haLeg{
		callID: d.CallID, localTag: d.LocalTag, remoteTag: d.RemoteTag,
		localCSeq: d.LocalCSeq, remoteCSeq: d.RemoteCSeq,
		routes: d.RouteSet, localID: d.LocalIdentity, remoteID: d.RemoteIdentity,
		remoteTarget: d.RemoteTarget, endpoint: d.Endpoint, source: d.Source,
		sdp: d.SDP, remoteSDP: d.RemoteSDP, uas: uas,
	}
}

// homedCall is a call's replicated dialog data after a takeover: the legs
// the taker rebuilt and where the call came from.
type homedCall struct {
	takenFrom string
	takenAt   time.Time
	legs      [2]*haLeg
}

// leg is the caller's (false) or callee's (true) homed leg.
func (h *homedCall) leg(callee bool) *haLeg {
	if callee {
		return h.legs[1]
	}
	return h.legs[0]
}

// useCSeq spends the leg's next CSeq value.
func (l *haLeg) useCSeq() {
	l.mu.Lock()
	l.localCSeq++
	l.mu.Unlock()
}

// request builds an in-dialog request on the leg.
func (l *haLeg) request(s *Server, method sip.RequestMethod, body []byte, contentType string) (*sip.Request, error) {
	l.mu.Lock()
	target, cseq, localTag, remoteTag := l.remoteTarget, l.localCSeq, l.localTag, l.remoteTag
	routes := l.routes
	l.mu.Unlock()
	var ruri sip.Uri
	if err := sip.ParseUri(strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">"), &ruri); err != nil {
		return nil, fmt.Errorf("takeover: remote target %q: %w", target, err)
	}
	req := sip.NewRequest(method, ruri)
	from := &sip.FromHeader{Address: parseOr(l.localID, ruri), Params: sip.HeaderParams{{K: "tag", V: localTag}}}
	to := &sip.ToHeader{Address: parseOr(l.remoteID, ruri), Params: sip.HeaderParams{{K: "tag", V: remoteTag}}}
	callID := sip.CallIDHeader(l.callID)
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: cseq, MethodName: method})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	for _, r := range routes {
		req.AppendHeader(sip.NewHeader("Route", r))
	}
	if len(body) > 0 {
		req.AppendHeader(sip.NewHeader("Content-Type", contentType))
		req.SetBody(body)
	}
	req.SetTransport("UDP")
	// Transport destination: the route hop on Hello's side of the dialog.
	// The route set crosses the edge proxy's two interfaces (the
	// phone-facing one is advertised and unreachable from the Hello
	// network); the hop naming a trusted proxy is the edge's Hello-facing
	// address.
	if hop := l.helloHop(s); hop != "" {
		req.SetDestination(hop)
	}
	return req, nil
}

// helloHop is the address of the route hop adjacent to Hello: the first
// entry of the dialog's route set (loose routing), "" when it has none.
// An edge proxy that double-record-routes puts two entries there, its
// Hello-facing socket first and its phone-facing one after; the latter's
// advertised address is unreachable from Hello (kw: a NodePort the edge
// answers 403 from Hello), however trusted its network may be.
func (l *haLeg) helloHop(_ *Server) string {
	l.mu.Lock()
	routes := l.routes
	l.mu.Unlock()
	if len(routes) == 0 {
		return ""
	}
	var hop sip.Uri
	if err := sip.ParseUri(strings.Trim(routes[0], "<> "), &hop); err != nil {
		return ""
	}
	return hostPort(hop)
}

// destination is where the leg's requests go: the route hop on Hello's
// side, or the remote target when no route names it.
func (l *haLeg) destination(s *Server) string {
	if hop := l.helloHop(s); hop != "" {
		return hop
	}
	l.mu.Lock()
	target := l.remoteTarget
	l.mu.Unlock()
	var u sip.Uri
	if err := sip.ParseUri(strings.TrimSuffix(strings.TrimPrefix(target, "<"), ">"), &u); err != nil {
		return ""
	}
	return hostPort(u)
}

// parseOr parses a URI string, falling back to fallback.
func parseOr(raw string, fallback sip.Uri) sip.Uri {
	var u sip.Uri
	if err := sip.ParseUri(strings.TrimSuffix(strings.TrimPrefix(raw, "<"), ">"), &u); err != nil {
		return fallback
	}
	return u
}

// haAck confirms an in-dialog INVITE's 2xx on a homed leg.
func (s *Server) haAck(l *haLeg, inv *sip.Request, res *sip.Response) {
	target := inv.Recipient
	if ct := res.Contact(); ct != nil {
		target = ct.Address
	}
	ack := sip.NewRequest(sip.ACK, *target.Clone())
	ack.AppendHeader(sip.HeaderClone(inv.From()))
	ack.AppendHeader(sip.HeaderClone(inv.To()))
	ack.AppendHeader(sip.HeaderClone(inv.CallID()))
	cseq := *inv.CSeq()
	cseq.MethodName = sip.ACK
	ack.AppendHeader(&cseq)
	ack.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	ack.SetTransport("UDP")
	if hop := l.helloHop(s); hop != "" {
		ack.SetDestination(hop)
	}
	if err := s.client.WriteRequest(ack, sipgo.ClientRequestAddVia); err != nil {
		s.log.Debug("takeover ACK failed", "error", err)
	}
}

// haReinvite re-INVITEs a leg with a new offer and re-learns the dialog
// locals from the 200: the endpoint's tags come back unchanged and its
// Contact may have moved.
func (s *Server) haReinvite(l *haLeg, body []byte) (*sip.Response, bool) {
	req, err := l.request(s, sip.INVITE, body, "application/sdp")
	if err != nil {
		s.log.Debug("takeover re-INVITE build failed", "error", err)
		return nil, false
	}
	l.useCSeq()
	ctx, cancel := context.WithTimeout(context.Background(), haReinviteTimeout)
	defer cancel()
	res, err := s.client.Do(ctx, req)
	if err != nil || res == nil {
		return nil, false
	}
	if !res.IsSuccess() {
		s.haAck(l, req, res) // a final response to an INVITE must be ACKed
		return res, false
	}
	s.haAck(l, req, res)
	l.mu.Lock()
	if ct := res.Contact(); ct != nil {
		l.remoteTarget = uriString(ct.Address)
	}
	if n := res.CSeq(); n != nil {
		l.localCSeq = n.SeqNo + 1
	}
	// The route set is fixed when the dialog forms (RFC 3261 12.2.1.2):
	// a re-INVITE refreshes only the remote target.
	l.mu.Unlock()
	return res, true
}

// haBye ends a homed leg's dialog (CSeq continuity, route set honoured).
// A solo call's missing second leg is a no-op.
func (s *Server) haBye(l *haLeg) {
	if l == nil {
		return
	}
	req, err := l.request(s, sip.BYE, nil, "")
	if err != nil {
		return
	}
	l.useCSeq()
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	if res, err := s.client.Do(ctx, req); err == nil && res != nil {
		s.m.response(res.StatusCode)
	}
}

// --- the poller ---------------------------------------------------------------

// takeoverLoop polls for orphaned dialogs of OFFLINE nodes (spec S-2). The
// poll is jittered, so survivors do not scan in lockstep.
func (s *Server) takeoverLoop(ctx context.Context) {
	for {
		s.takeoverPass(ctx)
		wait := s.cfg.HATakeoverPoll
		if s.cfg.HATakeoverJitter > 0 {
			// crypto/rand: the jitter only needs randomness, and gosec
			// rightly flags math/rand's global source.
			var b [1]byte
			_, _ = crand.Read(b[:])
			ms := s.cfg.HATakeoverJitter/time.Millisecond + 1
			wait += time.Duration(int(b[0])%int(ms)) * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-time.After(wait):
		}
	}
}

// takeoverPass scans once: every OFFLINE SIP node's unclaimed replicated
// dialogs are claimed and taken over; the unrecoverable remainder (calls
// that were never fully replicated) is reaped into the zombie counter. The
// dialogs a DRAINING node marked for handoff are claimed at once. And the
// dialogs of a dead process of a node that is alive again under another
// incarnation - a crash restarted in place under the same node ID, which
// never goes OFFLINE - are claimed at once too, including this node's own
// previous incarnation's (the restarted node reclaims its calls).
func (s *Server) takeoverPass(ctx context.Context) {
	if s.deps.HAState == nil || s.deps.Membership == nil || !s.cfg.HATakeoverEnabled {
		return
	}
	if st, _ := s.state(); st != cluster.Ready {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
	members, err := s.deps.Membership.Members(cctx)
	cancel()
	if err != nil {
		return
	}
	self := s.cfg.NodeID
	for _, m := range members {
		if m.Kind != cluster.KindSIP || m.ID == self {
			continue
		}
		var scan haScan
		switch m.State {
		case cluster.Offline:
			s.haReap(members, m, self)
			scan.offline = true
		case cluster.Draining:
			// A draining node hands its calls over while still alive:
			// only the dialogs it marked for handoff are claimed.
			s.haOfflineForget(m.ID)
			s.haNoteActive(m.ID, m.ActiveCalls)
			scan.handoff = true
		default:
			s.haOfflineForget(m.ID)
			s.haNoteActive(m.ID, m.ActiveCalls)
		}
		if !scan.offline && s.haWatchIncarnation(m.ID, m.Incarnation) {
			scan.live = m.Incarnation
		}
		if scan.offline || scan.handoff || scan.live != "" {
			s.haScanNode(ctx, m.ID, scan)
		}
	}
	// This node's own previous incarnation: its calls are orphans here.
	if s.haWatchIncarnation(self, s.cfg.Incarnation) {
		s.haScanNode(ctx, self, haScan{live: s.cfg.Incarnation})
	}
}

// haScan is what a scan of one node's dialogs may claim: everything of an
// OFFLINE node, the handoff-marked dialogs of a DRAINING one, and those of
// any incarnation but live (the node's current one, "" when unknown).
type haScan struct {
	offline, handoff bool
	live             string
}

// claimable reports whether the scan may claim the dialog, and the dead
// incarnation to claim it under ("" for a claim on a node that is OFFLINE
// or handing off, which keeps the freshness rule).
func (sc haScan) claimable(o livestate.DialogState) (stale string, ok bool) {
	if sc.live != "" && o.OwnerIncarnation != "" && o.OwnerIncarnation != sc.live {
		return o.OwnerIncarnation, true
	}
	return "", sc.offline || (sc.handoff && o.Handoff)
}

// haScanNode claims and takes over the dialogs of node the scan allows.
func (s *Server) haScanNode(ctx context.Context, node string, scan haScan) {
	cctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
	orphans, err := s.deps.HAState.OrphanedDialogs(cctx, node)
	cancel()
	if err != nil {
		s.log.Warn("orphan scan failed", "node", node, "error", err)
		return
	}
	for _, o := range orphans {
		if o.CallID == "" || o.OwnerNode != node {
			continue
		}
		stale, ok := scan.claimable(o)
		if !ok {
			continue
		}
		if st, won := s.haClaim(ctx, o.CallID, stale); won {
			s.log.Info("taking over a call", "call_id", o.CallID, "from", node, "dead_incarnation", stale)
			go s.takeOverCall(st, node)
		}
	}
}

// haClaim claims one dialog for this node (stale: the dead owner
// incarnation, or "").
func (s *Server) haClaim(ctx context.Context, callID, stale string) (livestate.DialogState, bool) {
	cctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
	defer cancel()
	ok, st, err := s.deps.HAState.ClaimDialog(cctx, callID, s.cfg.NodeID, stale)
	if err != nil {
		s.log.Warn("claim failed", "call_id", callID, "error", err)
		return st, false
	}
	return st, ok // !ok: another survivor won, or the owner came back
}

// haIncWindow is how long after a node's incarnation is first seen (or
// changes) its dialogs are scanned for a dead incarnation's: past it, every
// record the dead process wrote has expired (it cannot refresh them).
var haIncWindow = livestate.DialogTTL + 5*time.Second

// haWatchIncarnation reports whether node's dialogs should be scanned for a
// dead incarnation's: within haIncWindow of its incarnation being first
// seen here or changing. An unknown incarnation (older node) is never
// watched.
func (s *Server) haWatchIncarnation(node, inc string) bool {
	if inc == "" {
		return false
	}
	now := time.Now()
	s.haOfflineMu.Lock()
	defer s.haOfflineMu.Unlock()
	w, ok := s.haIncSeen[node]
	if !ok || w.inc != inc {
		w = haIncWatch{inc: inc, until: now.Add(haIncWindow)}
		s.haIncSeen[node] = w
	}
	return now.Before(w.until)
}

type haIncWatch struct {
	inc   string
	until time.Time
}

// haOwnerDead reports whether a replicated dialog's owner process is gone
// (and the dead incarnation to claim it under, "" when the owner is OFFLINE
// or handing the call off): this node's own previous incarnation, a node
// alive under another incarnation, a node OFFLINE or no longer listed, or a
// DRAINING owner that marked the dialog for handoff.
func (s *Server) haOwnerDead(ctx context.Context, st livestate.DialogState) (string, bool) {
	if st.OwnerNode == s.cfg.NodeID {
		if st.OwnerIncarnation != "" && st.OwnerIncarnation != s.cfg.Incarnation {
			return st.OwnerIncarnation, true
		}
		return "", false
	}
	if s.deps.Membership == nil {
		return "", false
	}
	cctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
	members, err := s.deps.Membership.Members(cctx)
	cancel()
	if err != nil {
		return "", false
	}
	for _, m := range members {
		if m.ID != st.OwnerNode || m.Kind != cluster.KindSIP {
			continue
		}
		switch {
		case m.State == cluster.Offline:
			return "", true
		case m.Incarnation != "" && st.OwnerIncarnation != "" && m.Incarnation != st.OwnerIncarnation:
			return st.OwnerIncarnation, true
		case m.State == cluster.Draining && st.Handoff:
			return "", true
		}
		return "", false
	}
	return "", true // not even a tombstone: long gone
}

// haReap counts, once and by exactly one survivor (the smallest READY node
// ID), the calls of an OFFLINE node that no dialog record could save: its
// live-call count at death less the dialogs survivors claimed. A claimed
// call either survived or was counted by its taker when its re-INVITE
// failed, so every call is counted at most once and a taken-over call
// never.
// It runs only after the takers had their chance, haReapDelay after the
// node was first seen OFFLINE.
func (s *Server) haReap(members []cluster.Member, m cluster.Member, self string) {
	e := s.haOfflineSee(m.ID, m.ActiveCalls)
	if e.counted || time.Now().Before(e.deadline) || !s.haAmReaper(members, self) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	taken, err := s.deps.HAState.TakenOver(ctx, m.ID)
	cancel()
	if err != nil {
		return
	}
	s.haOfflineCount(m.ID)
	// OFFLINE members are listed with no load: the node's live calls are
	// the last count seen while it was still alive.
	lost := max(m.ActiveCalls, e.active) - taken
	if lost <= 0 {
		return
	}
	s.m.ZombieCalls.Add(float64(lost))
	s.log.Warn("counted unrecoverable calls of an offline node", "node", m.ID, "zombies", lost)
}

// haAmReaper reports whether this node is the one survivor that counts
// zombies: the smallest READY SIP node ID, so exactly one node counts.
func (s *Server) haAmReaper(members []cluster.Member, self string) bool {
	reaper := ""
	for _, m := range members {
		if m.Kind != cluster.KindSIP || m.State != cluster.Ready {
			continue
		}
		if reaper == "" || m.ID < reaper {
			reaper = m.ID
		}
	}
	return reaper == self
}

type haOfflineNode struct {
	active   int
	counted  bool
	deadline time.Time
}

// haOfflineSee remembers a node's OFFLINE state and when its takeovers
// should be over; haOfflineCount marks it reaped; haOfflineForget clears it
// when the node is back.
func (s *Server) haOfflineSee(id string, active int) *haOfflineNode {
	s.haOfflineMu.Lock()
	defer s.haOfflineMu.Unlock()
	if e, ok := s.haOffline[id]; ok {
		return e
	}
	active = max(active, s.haLastActive[id])
	e := &haOfflineNode{active: active, deadline: time.Now().Add(haReapDelay)}
	s.haOffline[id] = e
	return e
}

func (s *Server) haOfflineCount(id string) {
	s.haOfflineMu.Lock()
	defer s.haOfflineMu.Unlock()
	if e, ok := s.haOffline[id]; ok {
		e.counted = true
	}
}

// haNoteActive remembers a live node's last published call count, which
// the reaper needs once the node is listed OFFLINE (with no load).
func (s *Server) haNoteActive(id string, active int) {
	s.haOfflineMu.Lock()
	defer s.haOfflineMu.Unlock()
	s.haLastActive[id] = active
}

func (s *Server) haOfflineForget(id string) {
	s.haOfflineMu.Lock()
	defer s.haOfflineMu.Unlock()
	delete(s.haOffline, id)
}

// --- the takeover ---------------------------------------------------------------

// haOffer is the codec set the call negotiated: the replicated SDP of
// either leg carries it.
func haOffer(a, b *haLeg) (media.AudioSDP, bool) {
	for _, l := range []*haLeg{a, b} {
		if off, err := media.ParseAudioSDP([]byte(l.sdp)); err == nil {
			return off, true
		}
	}
	return media.AudioSDP{}, false
}

// haAim points a relay leg at an endpoint's re-INVITE answer.
func haAim(relay *media.Relay, leg string, body []byte) {
	if a, err := media.ParseAudioSDP(body); err == nil {
		relay.SetTarget(leg, udpAddr(a.Address, a.Port))
	}
}

// resStatus is a response's status for a log line ("" on a transport error).
func resStatus(res *sip.Response) string {
	if res == nil {
		return ""
	}
	return strconv.Itoa(res.StatusCode) + " " + res.Reason
}

func legName(caller bool) string {
	if caller {
		return legCaller
	}
	return legCallee
}

// takeOverCall re-homes an orphaned call onto this node (spec S-2): a fresh
// relay on this node's RTP range, both legs rebuilt from the replicated
// state, each endpoint re-INVITEd to the new relay ports. Both legs re-homed
// make the call talking here; the claim is released (the restarted owner
// cannot retake: the claim is gone and its heartbeat sees this node's
// record) and the call replicates from here on.
func (s *Server) takeOverCall(st livestate.DialogState, from string) {
	defer contain(s.log, "takeover")
	if st.Legs[1].CallID == "" {
		s.takeOverSolo(st, from)
		return
	}
	start := time.Now()
	a, b := haLegFrom(st.Legs[0], true), haLegFrom(st.Legs[1], false)
	if a == nil || b == nil {
		s.haAbandon(st, from, "incomplete replicated state")
		return
	}
	off, ok := haOffer(a, b)
	if !ok {
		s.haAbandon(st, from, "replicated SDP unusable")
		return
	}
	relay, err := media.NewRelay(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		s.haAbandon(st, from, "relay ports: "+err.Error())
		return
	}
	if _, err := relay.AddLeg(legCaller); err != nil {
		relay.Close()
		s.haAbandon(st, from, "relay leg a: "+err.Error())
		return
	}
	if _, err := relay.AddLeg(legCallee); err != nil {
		relay.Close()
		s.haAbandon(st, from, "relay leg b: "+err.Error())
		return
	}
	relay.SetPayloadTypes(legCaller, off.PayloadType, off.DTMFPayloadType)
	relay.SetPayloadTypes(legCallee, off.PayloadType, off.DTMFPayloadType)
	host := s.anchorHostOr(off.Address)
	s.log.Info("takeover claimed", "call_id", st.CallID, "from", from)
	c := s.haNewCall(st, from, a, b, start)
	c.anchorHost = host
	c.mu.Lock()
	c.anchored = true
	c.anchorReason = AnchorPolicy
	c.relay = relay
	c.rec = &recording{}
	c.mu.Unlock()
	relay.OnPacket(c.recTap)
	relay.OnDTMF(func(leg string, digit byte) { c.relayDTMF(leg, digit) })
	relay.Start()
	// Re-INVITE both endpoints to the new relay (spec S-4): the offer is
	// this node's relay port for that leg, in the dialog's replicated
	// direction (a held call stays held); the answer re-aims it.
	bodyA := []byte(relaySDPDir(host, relay, legCaller, off, media.SDPDirection([]byte(a.sdp))))
	s.log.Info("takeover re-INVITE", "call_id", st.CallID, "leg", legCaller, "to", a.destination(s))
	res, okA := s.haReinvite(a, bodyA)
	if okA {
		haAim(relay, legCaller, res.Body())
	} else {
		a.failed = true
		s.log.Warn("takeover leg failed", "call_id", st.CallID, "leg", legCaller, "res", resStatus(res))
	}
	bodyB := []byte(relaySDPDir(host, relay, legCallee, off, media.SDPDirection([]byte(b.sdp))))
	s.log.Info("takeover re-INVITE", "call_id", st.CallID, "leg", legCallee, "to", b.destination(s))
	res, okB := s.haReinvite(b, bodyB)
	if okB {
		haAim(relay, legCallee, res.Body())
	} else {
		b.failed = true
		s.log.Warn("takeover leg failed", "call_id", st.CallID, "leg", legCallee, "res", resStatus(res))
	}
	if !a.failed && !b.failed {
		restored := time.Now()
		if took := restored.Sub(start); took > haMediaGap {
			s.log.Warn("takeover media gap exceeded", "correlation_id", c.id, "took", took.String())
		}
		s.haHome(c, st, from, restored)
		return
	}
	// One-sided close (spec edge cases): the endpoint that answered gets a
	// normal BYE and the call a CDR; the claim stays until it expires, so
	// survivors do not spin on a call that cannot be saved.
	s.haOneSidedClose(c, a, b, from)
}

// haHome finishes a successful takeover (both endpoints re-homed at
// restored): bind the dialogs, start the live
// and replication heartbeats, release the claim, restart recording or the
// announcement if the call was mid-way through one.
func (s *Server) haHome(c *call, st livestate.DialogState, from string, restored time.Time) {
	hom := c.homed()
	took := restored.Sub(hom.takenAt)
	gap, since := haGap(st, hom.takenAt, restored)
	s.log.Info("takeover re-homed", "call_id", st.CallID, "from", from, "took", took.String(), "gap", gap.String())
	c.addHATrace(from, took, gap, since)
	s.m.DialogTakeovers.Inc()
	s.bind(hom.legs[0].callID, dialogRef{c: c})
	s.bind(hom.legs[1].callID, dialogRef{c: c, leg: c.winner})
	s.m.ActiveCalls.Inc()
	c.publish()
	go c.heartbeat()
	// The record names this node before the claim goes: released first, a
	// survivor's next scan would find the dialog still owned by the dead
	// node and unclaimed, and take the call over a second time. A failed
	// write keeps the claim until a heartbeat's write succeeds (haLoop).
	c.mu.Lock()
	c.haClaim = st.CallID
	c.mu.Unlock()
	c.haArmMaxDuration(st.AnsweredAt, hom.takenAt)
	c.haSettleClaim(c.replicate())
	go c.haLoop()
	switch st.State {
	case haPhaseRecording:
		c.noteHAState(haPhaseRecording, "")
		c.mu.Lock()
		rec := c.rec
		c.mu.Unlock()
		if rec != nil && rec.start("takeover") {
			c.addTrace("Recording continues after the takeover")
		}
	case haPhaseAnnouncement:
		// The detail carries the announcement's object: it replays from
		// its beginning (spec S-4).
		if obj := strings.TrimPrefix(st.StateDetail, haRecordDetailPrefix); obj != "" && obj != st.StateDetail {
			c.addTrace("Announcement restarts from its beginning after the takeover")
			go c.playAnnouncementObject(obj)
		}
	case haPhaseTransferring:
		// The transfer's origination died with its node: the call
		// continues as it was, and the transferor learns the transfer
		// failed (a final NOTIFY on the dialog its REFER came on).
		if side := strings.TrimPrefix(st.StateDetail, haReferDetailPrefix); side != st.StateDetail {
			c.addTrace("Transfer abandoned by the takeover: the call continues untransferred")
			go s.haReferFailed(hom.leg(side == "callee"))
		}
	}
	if m := s.deps.Media; m != nil {
		c.anchorRelay().Observe(m.ObserveStats)
		c.anchorRelay().OnFail(func(string) { m.AnchorFailures.Inc() })
	}
}

// haOneSidedClose ends a half-taken-over call: BYE to the leg that answered,
// media closed, CDR, the zombie counted.
func (s *Server) haOneSidedClose(c *call, a, b *haLeg, from string) {
	which := legCaller
	if a.failed {
		which = legCallee
	}
	c.addTrace(fmt.Sprintf("Takeover from %s failed: the %s leg did not answer the re-INVITE", from, which))
	if !a.failed {
		go s.haBye(a)
	}
	if !b.failed {
		go s.haBye(b)
	}
	c.mu.Lock()
	c.anchored = false
	relay := c.relay
	c.relay = nil
	c.mu.Unlock()
	if relay != nil {
		relay.Close()
		if m := c.s.deps.Media; m != nil {
			m.NoteEnd()
		}
	}
	c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "takeover failed: an endpoint did not answer the re-INVITE", ResultFailed)
	s.m.ZombieCalls.Inc()
}

// haAbandon gives up a claim on a dialog whose state cannot carry a
// takeover: the zombie is counted and the claim left to expire, so no
// survivor retries the impossible.
func (s *Server) haAbandon(st livestate.DialogState, from, why string) {
	s.log.Warn("takeover abandoned", "call_id", st.CallID, "from", from, "why", why)
	s.m.ZombieCalls.Inc()
}

// releaseClaim drops this node's claim after a successful takeover.
func (s *Server) releaseClaim(callID string) {
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	defer cancel()
	if err := s.deps.HAState.ReleaseDialogClaim(ctx, callID); err != nil {
		s.log.Warn("could not release takeover claim", "call_id", callID, "error", err)
	}
}

// setHADirs records a hold re-INVITE's directions: the offerer's dialog
// answers mirrored, the peer's carries the direction as offered.
func (c *call) setHADirs(fromCaller bool, dir string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if fromCaller {
		c.haDirs[0], c.haDirs[1] = mirrorDirection(dir), dir
		return
	}
	c.haDirs[1], c.haDirs[0] = mirrorDirection(dir), dir
}

// haInDialog terminates a re-INVITE/UPDATE on a taken-over call: the offer
// is answered with the new relay's SDP for that side (direction mirrored)
// and the peer is re-INVITEd in the same direction, so a hold survives the
// takeover and both dialogs keep pointing at this node's relay.
func (c *call) haInDialog(hom *homedCall, req *sip.Request, tx sip.ServerTransaction, fromCaller bool) {
	s := c.s
	if c.anchorRelay() == nil {
		// A solo voicemail call: its media is the anchor session, which
		// keeps its port; the offer is answered with the SDP the
		// takeover gave the caller.
		l := hom.leg(!fromCaller)
		l.mu.Lock()
		ans := []byte(l.sdp)
		l.mu.Unlock()
		res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", ans)
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		res.AppendHeader(sip.HeaderClone(&s.contact))
		s.send(tx, res)
		return
	}
	off, err := media.ParseAudioSDP(req.Body())
	var ans, peer []byte
	if err == nil {
		dir := media.SDPDirection(req.Body())
		c.setHeld(dir == "sendonly" || dir == "inactive")
		c.setHADirs(fromCaller, dir)
		ans = media.BuildAudioSDPDir(c.anchorHost, c.legPortFor(fromCaller), off.PayloadType,
			off.DTMFPayloadType, off.DTMFRate, mirrorDirection(dir))
		peer = media.BuildAudioSDPDir(c.anchorHost, c.legPortFor(!fromCaller), off.PayloadType,
			off.DTMFPayloadType, off.DTMFRate, dir)
	}
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", ans)
	if err == nil {
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}
	res.AppendHeader(sip.HeaderClone(&s.contact))
	s.send(tx, res)
	if err != nil {
		return // no SDP to mirror
	}
	caller := hom.leg(false)
	callee := hom.leg(true)
	if caller == nil || callee == nil {
		return // a solo call has no peer to mirror to
	}
	go func() {
		defer contain(s.log, "homed re-INVITE")
		if fromCaller {
			s.haReinvite(callee, peer)
			return
		}
		s.haReinvite(caller, peer)
	}()
}

// haReferFailed tells a transferor whose transfer died with its node that
// the transfer failed: the final NOTIFY of the REFER's implicit
// subscription, on the homed leg its REFER came on, CSeq continuing the
// dialog.
func (s *Server) haReferFailed(l *haLeg) {
	defer contain(s.log, "takeover refer notify")
	if l == nil {
		return
	}
	req, err := l.request(s, sip.NOTIFY, []byte("SIP/2.0 503 Service Unavailable\r\n"), "message/sipfrag")
	if err != nil {
		return
	}
	l.useCSeq()
	req.AppendHeader(sip.NewHeader("Event", "refer"))
	req.AppendHeader(sip.NewHeader("Subscription-State", "terminated;reason=noresource"))
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := s.client.Do(ctx, req)
	if err != nil {
		s.log.Debug("takeover refer NOTIFY failed", "error", err)
		return
	}
	s.m.response(res.StatusCode)
}

// takeOverSolo re-homes a call Hello answered itself — a voicemail call or
// an announcement destination — whose only dialog is the caller's (spec
// S-5, edge case "mid-voicemail-prompt: the prompt restarts"). The taker
// binds fresh media of its own (an anchor session for voicemail, a relay
// for an announcement), re-INVITEs the caller onto it, and restarts the
// application from its beginning: the greeting and beep before a new
// recording, or the announcement from the top. The caller failing the
// re-INVITE closes the call and counts the zombie, as for a two-legged
// call.
func (s *Server) takeOverSolo(st livestate.DialogState, from string) {
	start := time.Now()
	a := haLegFrom(st.Legs[0], true)
	if a == nil || !haSoloPhase(st.State) {
		s.haAbandon(st, from, "incomplete replicated state")
		return
	}
	c := s.haNewCall(st, from, a, nil, start)
	c.haSolo = true
	var (
		body []byte
		sess media.Session
	)
	switch st.State {
	case haPhaseVoicemail:
		anchor := s.voicemailAnchor([]byte(a.remoteSDP))
		if anchor == nil {
			s.haAbandon(st, from, "no voicemail media anchor on this node")
			return
		}
		ans, vs, err := anchor.Answer([]byte(a.remoteSDP))
		if err != nil {
			s.haAbandon(st, from, "voicemail media: "+err.Error())
			return
		}
		body, sess = ans, vs
		c.vmSession = vs
	default: // an announcement destination
		off, err := media.ParseAudioSDP([]byte(a.remoteSDP))
		if err != nil {
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
			relay.SetPayloadTypes(leg, off.PayloadType, off.DTMFPayloadType)
		}
		relay.SetTarget(legCaller, udpAddr(off.Address, off.Port))
		c.anchorHost = s.anchorHostOr(off.Address)
		c.relay, c.anchored, c.anchorReason = relay, true, AnchorPolicy
		c.rec = &recording{}
		relay.Start()
		if m := s.deps.Media; m != nil {
			m.NoteStart()
		}
		body = media.BuildAudioSDP(c.anchorHost, relay.LegPort(legCaller), off.PayloadType, off.DTMFPayloadType, off.DTMFRate)
	}
	s.log.Info("takeover re-INVITE", "call_id", st.CallID, "leg", legCaller, "to", a.destination(s), "app", st.State)
	res, ok := s.haReinvite(a, body)
	if !ok {
		s.log.Warn("takeover leg failed", "call_id", st.CallID, "leg", legCaller, "res", resStatus(res))
		if sess != nil {
			_ = sess.Close()
		}
		c.addTrace(fmt.Sprintf("Takeover from %s failed: the caller did not answer the re-INVITE", from))
		c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "takeover failed: the caller did not answer the re-INVITE", ResultFailed)
		s.m.ZombieCalls.Inc()
		return
	}
	if relay := c.anchorRelay(); relay != nil {
		haAim(relay, legCaller, res.Body())
	}
	a.mu.Lock()
	a.sdp = string(body)
	a.mu.Unlock()
	now := time.Now()
	took := now.Sub(start)
	gap, since := haGap(st, start, now)
	c.mu.Lock()
	c.haPhase, c.haDetail = st.State, st.StateDetail
	c.mu.Unlock()
	s.log.Info("takeover re-homed", "call_id", st.CallID, "from", from, "took", took.String(), "gap", gap.String(), "app", st.State)
	c.addHATrace(from, took, gap, since)
	s.m.DialogTakeovers.Inc()
	s.bind(a.callID, dialogRef{c: c})
	s.m.ActiveCalls.Inc()
	c.publish()
	go c.heartbeat()
	c.mu.Lock()
	c.haClaim = st.CallID
	c.mu.Unlock()
	c.haArmMaxDuration(st.AnsweredAt, start)
	c.haSettleClaim(c.replicate())
	go c.haLoop()
	if st.State == haPhaseVoicemail {
		mode, reason, box := parseVoicemailDetail(st.StateDetail)
		if box != "" {
			c.mu.Lock()
			c.dialled = box
			c.mu.Unlock()
		}
		c.addTrace("Voicemail restarts from its greeting after the takeover")
		go c.voicemailFlow(mode, reason, sess)
		return
	}
	obj := strings.TrimPrefix(st.StateDetail, haRecordDetailPrefix)
	c.addTrace("Announcement restarts from its beginning after the takeover")
	go func() {
		defer contain(s.log, "announcement destination after takeover")
		if obj != "" && obj != st.StateDetail {
			c.playAnnouncementObject(obj)
		}
		c.announcementEnd()
	}()
}

// haNewCall builds the call a taker carries from a claimed dialog's
// replicated state: one logical call continuing, so its CDR starts when the
// call started on its first node, rang and was answered when it was there,
// keeps its routing and the trace so far, and its live record (same
// correlation id) answers when it really did. b is nil for a solo call.
func (s *Server) haNewCall(st livestate.DialogState, from string, a, b *haLeg, takenAt time.Time) *call {
	c := &call{
		s: s, id: st.Correlation, callID: a.callID,
		callerNum: orDefault(st.Caller, userOf(a.remoteID)), dialled: orDefault(st.Destination, userOf(a.localID)),
		start: orTime(st.StartedAt, orTime(st.AnsweredAt, takenAt)), ringTime: st.RingAt,
		direction: orDefault(st.Direction, cdr.DirectionInternal), mediaMode: "anchored",
		route: st.Route, trunkName: st.Trunk, rewritten: st.Rewritten,
		canceled: make(chan struct{}), stopHB: make(chan struct{}),
		setupDone: make(chan struct{}), aborted: make(chan struct{}),
		haStop: make(chan struct{}),
	}
	for _, text := range st.Trace {
		c.trace.Add(text)
	}
	if st.OwnerNode == s.cfg.NodeID {
		c.trace.Add(fmt.Sprintf("ha: %s restarted in place: its new process reclaims the call", from))
	}
	c.connected = true
	c.answerTime = orTime(st.AnsweredAt, takenAt)
	c.homedCall = &homedCall{takenFrom: from, takenAt: takenAt, legs: [2]*haLeg{a, b}}
	if b != nil {
		c.winner = &leg{c: c, callID: b.callID, binding: livestate.Binding{AOR: b.endpoint}}
	}
	return c
}

// haGap is a takeover's media gap, measured honestly: from the owner's
// last sign of life - its last replication write, which a crashed owner
// made at most one heartbeat (5s) before it died - to both endpoints
// re-homed (restored). It is an upper bound of the audio the call lost,
// at most a heartbeat over it, and includes the time it took to notice
// the owner was gone (membership's OFFLINE, or the restarted process).
// A handoff has no gap before its claim: the drainer relays until the
// endpoints move, so the gap runs from the claim (claimed). since names
// where the gap was measured from.
func haGap(st livestate.DialogState, claimed, restored time.Time) (gap time.Duration, since string) {
	if st.Handoff || st.UpdatedAt.IsZero() || st.UpdatedAt.After(claimed) {
		return restored.Sub(claimed), "the claim"
	}
	return restored.Sub(st.UpdatedAt), "the owner's last heartbeat at " + st.UpdatedAt.UTC().Format("15:04:05.000")
}

// addHATrace records a takeover on the call's trace: who it was taken
// from, how long the takeover took from the claim, and the media gap.
func (c *call) addHATrace(from string, took, gap time.Duration, since string) {
	c.addTrace(fmt.Sprintf("ha: taken over from %s in %s (media gap %s)", from,
		took.Round(time.Millisecond), gap.Round(time.Millisecond)))
	c.addTrace("ha: the media gap runs from " + since + " to both endpoints re-homed")
}

// orTime is t, or def when t is zero.
func orTime(t, def time.Time) time.Time {
	if t.IsZero() {
		return def
	}
	return t
}

// voicemailDetail encodes the voicemail application's restart data into a
// replicated state detail; parseVoicemailDetail decodes it.
func voicemailDetail(mode int, reason, box string) string {
	m := "leave"
	if mode == vmRetrieve {
		m = "retrieve"
	}
	return haVoicemailDetailPrefix + m + ":" + reason + ":" + box
}

func parseVoicemailDetail(detail string) (mode int, reason, box string) {
	parts := strings.SplitN(strings.TrimPrefix(detail, haVoicemailDetailPrefix), ":", 3)
	if len(parts) > 0 && parts[0] == "retrieve" {
		mode = vmRetrieve
	}
	if len(parts) > 1 {
		reason = parts[1]
	}
	if len(parts) > 2 {
		box = parts[2]
	}
	return mode, reason, box
}

// orDefault is v, or def when v is empty.
func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
