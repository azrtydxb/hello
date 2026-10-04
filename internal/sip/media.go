// Anchored media on the call (plan contracts 2, 3, 5): the relay's
// lifecycle, the SDP termination that puts Hello's relay ports into both
// dialogs, recording, and the metrics wiring.
package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// The relay legs' names. Direction metrics label "a>b" means the caller
// side's audio toward the callee.
const (
	legCaller = "a"
	legCallee = "b"
)

// recordingNotice is the announcement played before a recording starts
// (contract 5); recordToggle is the DTMF sequence that toggles a call's
// recording.
const (
	recordingNotice = "recording-notice"
	recordToggle    = "*1"
)

// RecordingStore is the recordings metadata store (PostgreSQL, migration
// 00005; implemented by internal/store). InsertRecording runs in a media
// goroutine only — never on the SIP transaction path.
type RecordingStore interface {
	// InsertRecording stores one recording row and returns its id.
	InsertRecording(ctx context.Context, correlationID, object, initiatedBy string, durationMs int64) (int64, error)
}

// recording is one call's recording state (contract 5).
type recording struct {
	mu      sync.Mutex
	active  bool
	paused  bool
	rec     *media.Recorder
	by      string // dtmf, default, api
	started time.Time
}

// start activates the recording; false when it already ran (one recording
// per call, spec S-4).
func (r *recording) start(by string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active {
		return false
	}
	r.active = true
	r.rec = media.NewRecorder()
	r.by = by
	r.started = time.Now()
	return true
}

// stop deactivates and returns the recorder to take the audio from.
func (r *recording) stop() (*media.Recorder, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return nil, ""
	}
	r.active = false
	rec, by := r.rec, r.by
	r.rec, r.by = nil, ""
	return rec, by
}

// capture is the relay's packet tap: active recording, not held, G.711 or
// pass-through payload.
func (r *recording) capture(leg string, pkt *media.RTPPacket) {
	r.mu.Lock()
	rec, active := r.rec, r.active
	r.mu.Unlock()
	if !active {
		return
	}
	rec.Write(leg, pkt.PayloadType, pkt.Payload)
}

// setPaused pauses capture while the call is held (spec edge case
// "Re-INVITE hold during recording").
func (r *recording) setPaused(p bool) {
	r.mu.Lock()
	r.paused = p
	r.mu.Unlock()
}

// isPaused reports the hold-pause state.
func (r *recording) isPaused() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.paused
}

// considerAnchor decides the anchoring for this call from the snapshot,
// the caller's addressing and the callee's (contract 2); a decision already
// taken is never revisited (anchored stays anchored, spec S-1).
func (c *call) considerAnchor(snap *snapshot.Snapshot, to EndpointInfo) {
	c.mu.Lock()
	if c.anchored || c.winner != nil {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()
	reason := decideAnchor(c.inv, snap, c.callerEndpoint(snap), to, c.s.cfg.MediaForceAnchor)
	if reason == AnchorNone {
		return
	}
	c.startAnchor(snap, reason)
}

// callerEndpoint is the caller side's EndpointInfo: Contact vs packet
// source of the INVITE, plus the calling extension for record_default.
func (c *call) callerEndpoint(_ *snapshot.Snapshot) EndpointInfo {
	req := c.inv
	var chost string
	cport := 0
	if ct := req.Contact(); ct != nil {
		chost = ct.Address.Host
		cport = ct.Address.Port
	}
	shost, sport := splitHostPort(req.Source())
	return EndpointInfo{
		ContactHost: chost, ContactPort: cport,
		SourceHost: shost, SourcePort: sport,
		Extension: c.callerNum,
	}
}

// bindingEndpoint is one binding's EndpointInfo: the Contact URI's
// host:port against the source the REGISTER was received on (spec S-1a),
// plus the extension for record_default.
func bindingEndpoint(b livestate.Binding, ext string) EndpointInfo {
	var chost string
	cport := 0
	var u sip.Uri
	if err := sip.ParseUri(trimAngle(b.ContactURI), &u); err == nil {
		chost, cport = u.Host, u.Port
	}
	shost, sport := splitHostPort(b.Source)
	return EndpointInfo{
		ContactHost: chost, ContactPort: cport,
		SourceHost: shost, SourcePort: sport,
		Extension: ext,
	}
}

// splitHostPort splits ip:port; a missing port is 0.
func splitHostPort(hostport string) (string, int) {
	host, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport, 0
	}
	p, _ := strconv.Atoi(port)
	return host, p
}

// trimAngle trims surrounding angle brackets.
func trimAngle(v string) string {
	return strings.TrimSuffix(strings.TrimPrefix(v, "<"), ">")
}

// startAnchor creates the relay and terminates the caller's SDP offer with
// Hello's relay ports (spec S-2). A failure falls back to direct with a
// trace step and the anchor-failure metric (spec failure mode); the call
// itself completes.
func (c *call) startAnchor(snap *snapshot.Snapshot, reason AnchorReason) {
	offer := c.inv.Body()
	if len(offer) == 0 {
		c.addTrace("Media anchor skipped: the caller INVITE carries no SDP offer")
		return
	}
	off, err := media.ParseAudioSDP(offer)
	if err != nil {
		c.addTrace("Media anchor skipped: " + err.Error())
		return
	}
	host := c.s.anchorHostOr(off.Address)
	c.anchorHost = host
	relay, err := media.NewRelay(c.s.cfg.RTPPortMin, c.s.cfg.RTPPortMax)
	if err != nil {
		c.anchorFailed("port range", err)
		return
	}
	portA, err := relay.AddLeg(legCaller)
	if err != nil {
		relay.Close()
		c.anchorFailed("bind leg a", err)
		return
	}
	relay.SetPayloadTypes(legCaller, off.PayloadType, off.DTMFPayloadType)
	relay.SetTarget(legCaller, udpAddr(off.Address, off.Port))
	portB, err := relay.AddLeg(legCallee)
	if err != nil {
		relay.Close()
		c.anchorFailed("bind leg b", err)
		return
	}
	relay.SetPayloadTypes(legCallee, off.PayloadType, off.DTMFPayloadType)
	relay.OnPacket(c.recTap)
	relay.OnDTMF(func(leg string, digit byte) { c.relayDTMF(leg, digit) })
	c.mu.Lock()
	c.anchored = true
	c.anchorReason = reason
	c.relay = relay
	c.mediaMode = "anchored"
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Media anchored (%s)", reason))
	if m := c.s.deps.Media; m != nil {
		m.NoteStart()
		relay.Observe(m.ObserveStats)
		relay.OnFail(func(string) { m.AnchorFailures.Inc() })
	}
	c.rec = &recording{}
	c.relay.Start()
	_ = portA
	_ = portB
}

// anchorFailed traces and counts a failed anchor; the call stays direct.
func (c *call) anchorFailed(what string, err error) {
	c.addTrace(fmt.Sprintf("Media anchor failed (%s): %v; the call stays direct", what, err))
	if m := c.s.deps.Media; m != nil {
		m.AnchorFailures.Inc()
	}
}

// anchorHostOr is the node's advertised anchor host, or the offer's own
// address when the node has none (tests bind the anchor host explicitly).
func (s *Server) anchorHostOr(fallback string) string {
	if s.anchor != nil {
		return s.anchor.AdvertisedHost()
	}
	return fallback
}

// udpAddr builds a UDP address from an SDP address and port.
func udpAddr(host string, port int) *net.UDPAddr {
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	return &net.UDPAddr{IP: ip, Port: port}
}

// recTap adapts the relay's packet tap to the recording state: paused
// while the call is held (spec edge case), the payload pass-through is
// what lands in the recording (no transcoding).
func (c *call) recTap(leg string, pkt *media.RTPPacket) {
	c.mu.Lock()
	rec := c.rec
	c.mu.Unlock()
	if rec == nil {
		return
	}
	if !rec.isPaused() {
		rec.capture(leg, pkt)
	}
}

// anchoredAnswerA is the SDP answer for the caller: the anchor's leg-a
// port with the offer's negotiated payload types.
func (c *call) anchoredAnswerA(offer media.AudioSDP) []byte {
	r := c.relay
	if r == nil {
		return nil
	}
	port := r.LegPort(legCaller)
	return media.BuildAudioSDP(c.anchorHost, port, offer.PayloadType, offer.DTMFPayloadType, offer.DTMFRate)
}

// anchoredOfferB is the SDP offer for a B leg: the anchor's leg-b port.
func (c *call) anchoredOfferB(offer media.AudioSDP) []byte {
	r := c.relay
	if r == nil {
		return nil
	}
	port := r.LegPort(legCallee)
	return media.BuildAudioSDP(c.anchorHost, port, offer.PayloadType, offer.DTMFPayloadType, offer.DTMFRate)
}

// isAnchored reports whether the call's media anchors (locked read).
func (c *call) isAnchored() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.anchored
}

// anchorRelay returns the call's relay under mu (nil when direct).
func (c *call) anchorRelay() *media.Relay {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.relay
}

// closeMedia stops the recording (stored detached) and the relay; it runs
// once, from end (the call's single completion point).
func (c *call) closeMedia() {
	c.mu.Lock()
	relay, rec := c.relay, c.rec
	c.anchored = false
	c.mu.Unlock()
	if rec != nil {
		if r, by := rec.stop(); r != nil {
			c.storeRecording(r, by)
		}
	}
	if relay != nil {
		if m := c.s.deps.Media; m != nil {
			m.NoteEnd()
		}
		relay.Close()
	}
}

// reanchorLive anchors a connected call that was direct: new relay, both
// dialogs re-INVITEd to the anchor's ports (spec S-1: a mid-call need
// re-anchors live). It reports whether it succeeded.
func (c *call) reanchorLive(reason AnchorReason) bool {
	c.mu.Lock()
	if c.anchored || !c.connected || c.winner == nil {
		c.mu.Unlock()
		return false
	}
	c.mu.Unlock()
	s := c.s
	offer := c.inv.Body()
	if len(offer) == 0 {
		return false
	}
	off, err := media.ParseAudioSDP(offer)
	if err != nil {
		return false
	}
	relay, err := media.NewRelay(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		c.anchorFailed("re-anchor port range", err)
		return false
	}
	portA, err := relay.AddLeg(legCaller)
	if err != nil {
		relay.Close()
		c.anchorFailed("re-anchor bind leg a", err)
		return false
	}
	portB, err := relay.AddLeg(legCallee)
	if err != nil {
		relay.Close()
		c.anchorFailed("re-anchor bind leg b", err)
		return false
	}
	_ = portA
	_ = portB
	host := s.anchorHostOr(off.Address)
	c.mu.Lock()
	c.anchorHost = host
	c.anchorReason = reason
	c.mediaMode = "anchored"
	c.relay = relay
	c.anchored = true
	c.mu.Unlock()
	relay.SetPayloadTypes(legCaller, off.PayloadType, off.DTMFPayloadType)
	relay.SetPayloadTypes(legCallee, off.PayloadType, off.DTMFPayloadType)
	relay.OnPacket(c.recTap)
	relay.OnDTMF(func(leg string, digit byte) { c.relayDTMF(leg, digit) })
	relay.SetTarget(legCallee, nil)
	// Re-INVITE both dialogs: B (a UAC dialog) gets the anchored offer,
	// A (the caller's UAS dialog) the anchor's answer-side offer; each
	// side's answer aims its relay leg.
	w := c.winnerSession()
	c.mu.Lock()
	dss := c.dss
	c.mu.Unlock()
	if w == nil || dss == nil {
		relay.Close()
		return false
	}
	if ansB, ok := reinvite(w, c.anchoredOfferB(off)); ok {
		if a, err := media.ParseAudioSDP(ansB); err == nil {
			relay.SetTarget(legCallee, udpAddr(a.Address, a.Port))
		}
	} else {
		relay.Close()
		c.anchorFailed("re-anchor re-INVITE to the callee", errors.New("no answer"))
		return false
	}
	if ansA, ok := reinviteA(dss, c.anchoredAnswerA(off)); ok {
		if a, err := media.ParseAudioSDP(ansA); err == nil {
			relay.SetTarget(legCaller, udpAddr(a.Address, a.Port))
		}
	} else {
		relay.Close()
		c.anchorFailed("re-anchor re-INVITE to the caller", errors.New("no answer"))
		return false
	}
	if m := s.deps.Media; m != nil {
		m.NoteStart()
		relay.Observe(m.ObserveStats)
		relay.OnFail(func(string) { m.AnchorFailures.Inc() })
	}
	c.mu.Lock()
	c.rec = &recording{}
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Media anchored mid-call (%s)", reason))
	relay.Start()
	if reason == AnchorRecording {
		c.startRecordingFlow("dtmf")
	}
	return true
}

// winnerSession is the winning fork's dialog session (nil without one).
func (c *call) winnerSession() *sipgo.DialogClientSession {
	c.mu.Lock()
	w := c.winner
	c.mu.Unlock()
	if w == nil {
		return nil
	}
	return w.session()
}

// startRecordingFlow begins a recording: re-anchoring first when the call
// is direct (spec S-4: a direct call with record_default anchors at
// setup, or here, mid-call), the notice announcement (config-gated), then
// capture. Runs off the transaction path.
func (c *call) startRecordingFlow(by string) {
	c.mu.Lock()
	anchored := c.anchored
	c.mu.Unlock()
	if !anchored {
		if !c.reanchorLive(AnchorRecording) {
			c.addTrace("Recording skipped: the call could not be anchored")
			return
		}
		return // reanchorLive starts the recording itself
	}
	c.mu.Lock()
	rec := c.rec
	c.mu.Unlock()
	if rec == nil {
		return
	}
	if !rec.start(by) {
		return // already recording (spec: one recording per call)
	}
	c.addTrace("Recording started")
	// The notice announcement plays before the capture becomes a kept
	// recording, matching the voicemail beep discipline.
	if c.s.cfg.MediaRecordingNotice {
		go c.playRecordingNotice()
	}
}

// toggleRecording is the *1 handler: start or stop the recording (spec
// S-4). The stop stores the audio detached from the call.
func (c *call) toggleRecording() {
	c.mu.Lock()
	rec := c.rec
	c.mu.Unlock()
	if rec == nil {
		// A direct call never anchored: *1 is the mid-call need that
		// re-anchors (spec S-1, S-4).
		c.startRecordingFlow("dtmf")
		return
	}
	if r, by := rec.stop(); r != nil {
		c.addTrace("Recording stopped")
		go c.storeRecording(r, by)
		return
	}
	c.startRecordingFlow("dtmf")
}

// playRecordingNotice plays the recording-notice announcement to both
// parties when the announcement set has it (contract 5). A missing WAV is
// skipped (spec failure mode).
func (c *call) playRecordingNotice() {
	defer contain(c.s.log, "recording notice")
	snap := c.s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	obj, ok := snap.Announcement(recordingNotice)
	if !ok {
		return
	}
	c.playAnnouncementObject(obj)
}

// playAnnouncementObject fetches one announcement WAV from MinIO and
// plays it to both anchored legs. A fetch failure is traced and skipped;
// the call continues (spec failure mode).
func (c *call) playAnnouncementObject(obj string) {
	objs := c.s.deps.Objects
	r := c.anchorRelay()
	if objs == nil || r == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), c.s.cfg.StateTimeout)
	raw, err := objs.Get(ctx, obj)
	cancel()
	if err != nil {
		c.addTrace(fmt.Sprintf("Announcement %q unavailable: skipped", obj))
		return
	}
	pcm, _, err := media.DecodeWAV(raw)
	if err != nil {
		c.addTrace(fmt.Sprintf("Announcement %q is not a WAV: skipped", obj))
		return
	}
	pctx, pcancel := context.WithTimeout(context.Background(), time.Minute)
	defer pcancel()
	for _, legName := range []string{legCaller, legCallee} {
		if err := r.PlayTo(pctx, legName, pcm); err != nil {
			c.s.log.Debug("announcement playback failed", "leg", legName, "error", err)
		}
	}
}

// storeRecording takes the recorder's audio and stores it: the WAV (or
// pass-through payload) in MinIO with three attempts, then the recordings
// row, then the storage gauge. It runs detached so a caller who hangs up
// mid-recording still leaves the file (spec failure modes).
func (c *call) storeRecording(rec *media.Recorder, by string) {
	audio, codec, durMs := rec.Result()
	if len(audio) == 0 {
		return
	}
	if rec.Over() {
		c.addTrace("Recording truncated at the 10 MB in-memory bound")
	}
	s := c.s
	ctx, cancel := context.WithTimeout(context.Background(), 3*s.cfg.StateTimeout)
	defer cancel()
	key, err := media.RecordingKey(c.callID, time.Now())
	if err != nil {
		s.log.Warn("recording key refused", "error", err)
		return
	}
	obj := key
	if codec != "WAV" {
		obj = strings.TrimSuffix(key, ".wav") + "." + strings.ToLower(codec)
	}
	stored := false
	for i := 0; i < 3; i++ {
		pctx, pcancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
		err := s.deps.Objects.Put(pctx, obj, audio)
		pcancel()
		if err == nil {
			stored = true
			break
		}
		s.log.Warn("recording upload failed", "attempt", i+1, "error", err)
		select {
		case <-ctx.Done():
			c.addTrace("Recording storage failed after 3 attempts")
			return
		case <-time.After(time.Duration(100*(i+1)) * time.Millisecond):
		}
	}
	if !stored {
		c.addTrace("Recording storage failed after 3 attempts")
		return
	}
	if s.deps.Recordings != nil {
		rctx, rcancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
		_, err := s.deps.Recordings.InsertRecording(rctx, c.id, obj, by, durMs)
		rcancel()
		if err != nil {
			c.addTrace("Recording metadata store failed")
			s.log.Warn("recording row failed", "error", err)
			return
		}
	}
	if m := s.deps.Media; m != nil {
		n := s.vmBytes.Add(int64(len(audio)))
		m.RecordingStorage.Set(float64(n))
	}
	c.addTrace(fmt.Sprintf("Recording stored (%s, %d ms)", codec, durMs))
}

// relayDTMF is the relay's RFC 2833 DTMF hook: *1 toggles the recording
// (spec S-4). Digits also flow into the call's feature-code buffer, so an
// INFO dtmf-relay *1 and an in-band *1 behave the same.
func (c *call) relayDTMF(_ string, digit byte) {
	buf := c.s.bufferFor(c)
	all := buf.push([]byte{digit})
	if digit == '*' {
		return // the 1 follows
	}
	if len(all) >= 2 && all[len(all)-2] == '*' && digit == '1' {
		buf.take()
		c.toggleRecording()
	}
}

// handleAnchoredInDialog terminates a re-INVITE/UPDATE on an anchored
// call: the offer is answered with the anchor's SDP for that side
// (direction mirrored), and the peer is re-INVITEd with the anchor's SDP
// in the same direction, so both dialogs keep pointing at the relay and a
// hold reaches both phones (spec S-2, S-1 hold edge case).
func (c *call) handleAnchoredInDialog(req *sip.Request, tx sip.ServerTransaction, fromCaller bool) {
	s := c.s
	body := req.Body()
	off, err := media.ParseAudioSDP(body)
	if err != nil {
		// No usable SDP: mirror the request to the peer unchanged, the
		// direct path's behaviour.
		c.mirrorInDialog(req, tx, fromCaller)
		return
	}
	dir := media.SDPDirection(body)
	c.setHeld(dir == "sendonly" || dir == "inactive")
	// The answer to the offer's side: our SDP with the mirrored direction.
	ansDir := mirrorDirection(dir)
	ans := media.BuildAudioSDPDir(c.anchorHost, c.legPortFor(fromCaller), off.PayloadType,
		off.DTMFPayloadType, off.DTMFRate, ansDir)
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", ans)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res.AppendHeader(sip.HeaderClone(&s.contact))
	s.send(tx, res)
	// The peer gets our SDP in the same direction (a held side hears
	// silence because the held side stopped sending).
	peerDir := dir
	peer := media.BuildAudioSDPDir(c.anchorHost, c.legPortFor(!fromCaller), off.PayloadType,
		off.DTMFPayloadType, off.DTMFRate, peerDir)
	c.mu.Lock()
	w := c.winner
	c.mu.Unlock()
	if fromCaller {
		if w != nil {
			_, _ = reinvite(w.session(), peer)
		}
		return
	}
	c.mu.Lock()
	dss := c.dss
	c.mu.Unlock()
	if dss != nil {
		_, _ = reinviteA(dss, peer)
	}
}

// mirrorDirection flips a direction for the answer: the answerer receives
// what the offerer sends.
func mirrorDirection(dir string) string {
	switch dir {
	case "sendonly":
		return "recvonly"
	case "recvonly":
		return "sendonly"
	case "inactive":
		return "inactive"
	}
	return "sendrecv"
}

// legPortFor is the relay port of the caller's (true) or callee's (false)
// leg.
func (c *call) legPortFor(caller bool) int {
	r := c.anchorRelay()
	if r == nil {
		return 0
	}
	if caller {
		return r.LegPort(legCaller)
	}
	return r.LegPort(legCallee)
}

// mirrorInDialog relays a bodyless in-dialog request to the peer, the
// direct path's behaviour, for anchored calls without SDP.
func (c *call) mirrorInDialog(req *sip.Request, tx sip.ServerTransaction, fromCaller bool) {
	s := c.s
	var (
		out *sip.Request
		do  func(context.Context, *sip.Request) (*sip.Response, error)
	)
	if fromCaller {
		c.mu.Lock()
		w := c.winner
		c.mu.Unlock()
		if w == nil {
			s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
			return
		}
		out, do = sip.NewRequest(req.Method, w.target()), w.session().Do
	} else {
		out, do = sip.NewRequest(req.Method, c.target()), c.dss.Do
	}
	if body := req.Body(); len(body) > 0 {
		if ct := req.ContentType(); ct != nil {
			out.AppendHeader(sip.HeaderClone(ct))
		}
		out.SetBody(body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := do(ctx, out)
	if err != nil {
		s.respond(tx, req, sip.StatusRequestTimeout, "Request Timeout")
		return
	}
	relayed := sip.NewResponseFromRequest(req, res.StatusCode, res.Reason, res.Body())
	if ct := res.ContentType(); ct != nil {
		relayed.AppendHeader(sip.HeaderClone(ct))
	}
	s.send(tx, relayed)
}

// announcementDestination answers the caller through the anchor, plays the
// named announcement, then hangs the caller up (spec S-5: an announcement
// destination answers, plays, then continues). A missing anchor or WAV
// still ends the call with a trace step.
func (c *call) announcementDestination(name string) {
	s := c.s
	snap := s.deps.Snapshots.Current()
	obj, ok := "", false
	if snap != nil {
		obj, ok = snap.Announcement(name)
	}
	if !c.isAnchored() {
		// Pre-answer (a group failure destination): anchor at setup like
		// voicemail does, so the answer SDP is the anchor's.
		c.startAnchor(snap, AnchorAnnouncement)
		if !c.isAnchored() {
			c.addTrace("Announcement destination skipped: the call could not be anchored")
			c.hangupGroup(sip.StatusRequestTimeout, "group timeout")
			return
		}
	}
	if !c.answerSelf(c.anchoredAnswerA(parsedOffer(c))) {
		return
	}
	go func() {
		defer contain(s.log, "announcement destination")
		if ok {
			c.playAnnouncementObject(obj)
		} else {
			c.addTrace(fmt.Sprintf("Announcement %q missing: step skipped", name))
		}
		// The announcement ends the call here: a group's failure
		// destination has no next step (spec S-5 "hangup or next step").
		c.announcementEnd()
	}()
}

// parsedOffer parses the caller's offer once for the SDP builders.
func parsedOffer(c *call) media.AudioSDP {
	off, _ := media.ParseAudioSDP(c.inv.Body())
	return off
}

// playAnnouncementToCall plays a named announcement on an established
// call, anchoring it first when the call was direct (the pre-transfer
// prompt, contract 7). A missing anchor or WAV is traced and skipped.
func (c *call) playAnnouncementToCall(name string) {
	snap := c.s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	obj, ok := snap.Announcement(name)
	if !ok {
		c.addTrace(fmt.Sprintf("Announcement %q missing: skipped", name))
		return
	}
	if !c.isAnchored() && !c.reanchorLive(AnchorAnnouncement) {
		c.addTrace("Announcement skipped: the call could not be anchored")
		return
	}
	c.playAnnouncementObject(obj)
}

// anchorReasonIs reports the call's anchoring reason (locked read).
func (c *call) anchorReasonIs(r AnchorReason) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.anchored && c.anchorReason == r
}

// announcementEnd ends an answered callee-less call (announcement
// destination or feature code): the CDR, then a raw BYE on the caller's
// dialog, whose sipgo session Bye refuses (its 200 was written through the
// transaction, so InviteResponse was never set).
func (c *call) announcementEnd() {
	c.mu.Lock()
	if c.hungUp {
		c.mu.Unlock()
		return
	}
	c.hungUp = true
	c.mu.Unlock()
	c.end(sip.StatusOK, cdr.SideCallee, "", ResultAnswered)
	go func() {
		defer contain(c.s.log, "announcement bye")
		c.byeCallerRaw()
	}()
}

// byeCallerRaw sends the BYE on the caller dialog we answered through the
// transaction (vmTag identifies it).
func (c *call) byeCallerRaw() {
	c.mu.Lock()
	inv, tag := c.inv, c.vmTag
	c.mu.Unlock()
	if inv == nil {
		return
	}
	target := inv.Recipient
	if ct := inv.Contact(); ct != nil {
		target = ct.Address
	}
	req := sip.NewRequest(sip.BYE, *target.Clone())
	req.AppendHeader(&sip.FromHeader{Address: *inv.To().Address.Clone(),
		Params: sip.HeaderParams{{K: "tag", V: tag}}})
	req.AppendHeader(&sip.ToHeader{Address: *inv.From().Address.Clone(),
		Params: sip.HeaderParams{{K: "tag", V: tagParam(inv.From())}}})
	callID := sip.CallIDHeader(inv.CallID().Value())
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: inv.CSeq().SeqNo + 1, MethodName: sip.BYE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&c.s.contact))
	req.SetTransport("UDP")
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := c.s.client.Do(ctx, req)
	if err != nil {
		c.s.log.Debug("announcement BYE failed", "error", err)
		return
	}
	c.s.m.response(res.StatusCode)
}
