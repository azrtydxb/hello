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
	sdp          string   // the last SDP on this leg
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
		sdp: d.SDP, uas: uas,
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
	// The dialog's route set crosses the edge proxy's two interfaces: on
	// the caller's leg (Hello is the UAS) it runs phone-edge-first, so
	// Hello's hop is the last; on Hello's own forks it is the first.
	if hop, ok := l.helloHop(); ok {
		req.SetDestination(hop)
	}
	return req, nil
}

// helloHop is the address of the route hop adjacent to Hello, for the
// transport destination of the leg's requests.
func (l *haLeg) helloHop() (string, bool) {
	l.mu.Lock()
	routes, uas := l.routes, l.uas
	l.mu.Unlock()
	var raw string
	switch {
	case len(routes) == 0:
		return "", false
	case uas:
		raw = routes[len(routes)-1]
	default:
		raw = routes[0]
	}
	var hop sip.Uri
	if err := sip.ParseUri(strings.Trim(raw, "<> "), &hop); err != nil {
		return "", false
	}
	return hostPort(hop), true
}

// destination is where the leg's requests go: the route hop on Hello's
// side, or the remote target when the dialog has no route set.
func (l *haLeg) destination() string {
	if hop, ok := l.helloHop(); ok {
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
	if hop, ok := l.helloHop(); ok {
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
	if rrs := headerValues(res, "Record-Route"); len(rrs) > 0 {
		l.routes = rrs
	}
	l.mu.Unlock()
	return res, true
}

// haBye ends a homed leg's dialog (CSeq continuity, route set honoured).
func (s *Server) haBye(l *haLeg) {
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
// that were never fully replicated) is reaped into the zombie counter.
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
		if m.State != cluster.Offline {
			s.haOfflineForget(m.ID)
			continue
		}
		s.haReap(members, m, self)
		cctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
		orphans, err := s.deps.HAState.OrphanedDialogs(cctx, m.ID)
		cancel()
		if err != nil {
			s.log.Warn("orphan scan failed", "node", m.ID, "error", err)
			continue
		}
		for _, o := range orphans {
			if o.CallID == "" || o.OwnerNode != m.ID {
				continue
			}
			cctx, cancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
			ok, st, err := s.deps.HAState.ClaimDialog(cctx, o.CallID, self)
			cancel()
			if err != nil {
				s.log.Warn("claim failed", "call_id", o.CallID, "error", err)
				continue
			}
			if !ok {
				continue // another survivor won, or the owner came back
			}
			s.log.Info("taking over a call", "call_id", o.CallID, "from", m.ID)
			go s.takeOverCall(st, m.ID)
		}
	}
}

// haReap counts, once and by exactly one survivor (the smallest READY node
// ID), the calls of an OFFLINE node that no dialog record can save: its
// live-call count at death less the dialogs still visible to take over.
// It runs only after the takers had their chance, past the dialog TTL.
func (s *Server) haReap(members []cluster.Member, m cluster.Member, self string) {
	e := s.haOfflineSee(m.ID, m.ActiveCalls)
	if e.counted || time.Now().Before(e.deadline) || !s.haAmReaper(members, self) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	orphans, err := s.deps.HAState.OrphanedDialogs(ctx, m.ID)
	cancel()
	if err != nil {
		return
	}
	s.haOfflineCount(m.ID)
	lost := m.ActiveCalls - len(orphans)
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
	e := &haOfflineNode{active: active, deadline: time.Now().Add(livestate.DialogTTL + 15*time.Second)}
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
	c := &call{
		s: s, id: st.Correlation, callID: a.callID,
		callerNum: userOf(a.remoteID), dialled: userOf(a.localID),
		start: start, direction: cdr.DirectionInternal, mediaMode: "anchored",
		canceled: make(chan struct{}), stopHB: make(chan struct{}),
		setupDone: make(chan struct{}), aborted: make(chan struct{}),
		anchorHost: host,
	}
	c.haStop = make(chan struct{})
	w := &leg{c: c, callID: b.callID, binding: livestate.Binding{AOR: b.endpoint}}
	c.mu.Lock()
	c.winner = w
	c.homedCall = &homedCall{takenFrom: from, takenAt: start, legs: [2]*haLeg{a, b}}
	c.anchored = true
	c.anchorReason = AnchorPolicy
	c.relay = relay
	c.rec = &recording{}
	c.connected = true
	c.answerTime = start
	c.mu.Unlock()
	relay.OnPacket(c.recTap)
	relay.OnDTMF(func(leg string, digit byte) { c.relayDTMF(leg, digit) })
	relay.Start()
	// Re-INVITE both endpoints to the new relay (spec S-4): the offer is
	// this node's relay port for that leg, in the dialog's replicated
	// direction (a held call stays held); the answer re-aims it.
	bodyA := []byte(relaySDPDir(host, relay, legCaller, off, media.SDPDirection([]byte(a.sdp))))
	if res, okA := s.haReinvite(a, bodyA); okA {
		haAim(relay, legCaller, res.Body())
	} else {
		a.failed = true
	}
	bodyB := []byte(relaySDPDir(host, relay, legCallee, off, media.SDPDirection([]byte(b.sdp))))
	if res, okB := s.haReinvite(b, bodyB); okB {
		haAim(relay, legCallee, res.Body())
	} else {
		b.failed = true
	}
	gap := time.Since(start)
	if !a.failed && !b.failed {
		if gap > haMediaGap {
			s.log.Warn("takeover media gap exceeded", "correlation_id", c.id, "gap", gap.String())
		}
		s.haHome(c, st, from, gap)
		return
	}
	// One-sided close (spec edge cases): the endpoint that answered gets a
	// normal BYE and the call a CDR; the claim stays until it expires, so
	// survivors do not spin on a call that cannot be saved.
	s.haOneSidedClose(c, a, b, from)
}

// haHome finishes a successful takeover: bind the dialogs, start the live
// and replication heartbeats, release the claim, restart recording or the
// announcement if the call was mid-way through one.
func (s *Server) haHome(c *call, st livestate.DialogState, from string, gap time.Duration) {
	s.log.Info("takeover re-homed", "call_id", st.CallID, "from", from, "gap", gap.String())
	hom := c.homed()
	c.addTrace(fmt.Sprintf("ha: taken over from %s in %s (media gap %s)", from,
		gap.Round(time.Millisecond), gap.Round(time.Millisecond)))
	s.m.DialogTakeovers.Inc()
	s.bind(hom.legs[0].callID, dialogRef{c: c})
	s.bind(hom.legs[1].callID, dialogRef{c: c, leg: c.winner})
	s.m.ActiveCalls.Inc()
	c.publish()
	go c.heartbeat()
	go c.haLoop()
	s.releaseClaim(st.CallID)
	switch st.State {
	case haPhaseRecording:
		if c.rec.start("takeover") {
			c.addTrace("Recording continues after the takeover")
		}
	case haPhaseAnnouncement:
		if name := strings.TrimPrefix(st.StateDetail, haRecordDetailPrefix); name != "" {
			if snap := s.deps.Snapshots.Current(); snap != nil {
				if obj, ok := snap.Announcement(name); ok {
					go c.playAnnouncementObject(obj)
				}
			}
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
	go func() {
		defer contain(s.log, "homed re-INVITE")
		if fromCaller {
			s.haReinvite(callee, peer)
			return
		}
		s.haReinvite(caller, peer)
	}()
}
