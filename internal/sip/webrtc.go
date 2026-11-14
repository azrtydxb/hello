// Browser and app legs in the call path (clients-webrtc-mobile-video S-3 to
// S-5, S-36 to S-40). A web client is a phone that registers through the edge
// over WSS; what makes its calls different is only the media leg: UDP/TLS/RTP/
// SAVPF with ICE-lite on the node's shared WebRTC port and DTLS-SRTP keys.
// This file decides which legs are browsers', makes their relay legs, takes the
// answers and the video negotiation, and keeps the seams the next pieces plug
// into: video recording (6b) reads the video the relay forwards, conference
// video (11) and video phones and door stations (10) add legs and codecs to the
// same negotiation.
package sip

import (
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/secmedia"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// callWeb is a call's browser state (guarded by call.mu).
type callWeb struct {
	// caller: the caller's leg is a browser's (its INVITE offered the browser
	// transport). altLeg: an alternate callee leg exists for a browser fork.
	caller, altLeg bool
	// video is what the caller's offer holds for video: the codecs Hello will
	// relay, in the caller's numbering, and the section it came in. Empty when
	// the caller offered none or video is not allowed for the call.
	video      []media.SDPCodec
	videoOffer media.VideoOffer
	// plainRTP and plainRTCP are where a plain caller's video RTP and RTCP go.
	plainRTP, plainRTCP *net.UDPAddr
	// slot: the call holds one of the node's video session slots.
	slot bool
	// used: video was negotiated at some point (the CDR's video column).
	used bool
}

func (c *call) webState() *callWeb {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.web == nil {
		c.web = &callWeb{}
	}
	return c.web
}

// webCaller reports whether the caller's leg is a browser's.
func (c *call) webCaller() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.web != nil && c.web.caller
}

// webCall reports whether any leg of the call is a browser's (a call with a
// browser leg is not replicated: its ICE and DTLS state is the node's own).
func (c *call) webCall() bool {
	c.mu.Lock()
	r := c.relay
	web := c.web != nil && (c.web.caller || c.web.altLeg)
	c.mu.Unlock()
	if web {
		return true
	}
	return r != nil && (r.IsWebLeg(legCaller) || r.IsWebLeg(legCallee))
}

// isWebBinding reports whether a registered contact is a browser or app: it
// registered over a WebSocket, or its device is a web or mobile client.
func isWebBinding(snap *snapshot.Snapshot, b livestate.Binding) bool {
	if media.CurrentWebRTC() == nil {
		return false // a node without WebRTC media rings every contact as a phone
	}
	if strings.EqualFold(b.Transport, "ws") || strings.EqualFold(b.Transport, "wss") {
		return true
	}
	if snap != nil {
		if d, ok := snap.DeviceByUsername(b.Device); ok {
			return d.Kind == "web" || d.Kind == "mobile"
		}
	}
	return false
}

// legIsWeb reports whether a fork goes to a browser.
func (c *call) legIsWeb(l *leg) bool {
	if l == nil || l.trunk != nil || l.uri != nil {
		return false
	}
	return isWebBinding(c.srtpSnapshot(), l.binding)
}

// errWebRTCOff is why a browser leg cannot be taken.
var errWebRTCOff = errors.New("WebRTC media is not enabled on this node")

// webrtcGate is the part of srtpGate for a caller whose offer is the browser's:
// the node must run WebRTC media, and the hop from the edge must be TLS unless
// the operator accepted the cleartext hop (S-12), because the DTLS fingerprint
// in the SDP is only as trustworthy as the path that carries it. It reports
// whether the call goes on; when it does not, the response and the CDR are done.
func (c *call) webrtcGate(tx sip.ServerTransaction, req *sip.Request) bool {
	why := ""
	switch {
	case media.CurrentWebRTC() == nil:
		why = errWebRTCOff.Error()
	case c.s.checkWebRTCHop(req) != nil:
		// The TLS hop of S-12: counted and logged with its reason there.
		why = media.ErrCleartextHop.Error()
	case !media.DTLSEnabled():
		why = "the caller offered DTLS-SRTP, which is disabled on this node"
	}
	if why == "" {
		sess, err := media.ParseSDPSession(req.Body())
		if err == nil {
			err = media.ValidateWebRTC(sess)
		}
		if err != nil {
			why = "the offer is not an acceptable WebRTC offer: " + err.Error()
		}
	}
	if why != "" {
		c.srtpNote("refused")
		c.addTrace("Call refused: " + why)
		c.s.respond(tx, req, sip.StatusNotAcceptableHere, "Not Acceptable Here")
		c.record(sip.StatusNotAcceptableHere, cdr.SideSystem, why, ResultFailed)
		return false
	}
	c.webState().caller = true
	return true
}

// webLegSRTP is what a browser leg is: encrypted, DTLS-SRTP and ICE-lite, over
// an encrypted signalling path (the edge ends the WebSocket's TLS).
func webLegSRTP() srtpLeg {
	return srtpLeg{policy: secmedia.Required, tls: true, dtls: true, ice: true}
}

// webAnchorCaller makes the caller leg of a browser's call on the shared port
// and readies it for the offer; the relay's second leg is made by the caller.
func (c *call) webAnchorCaller(relay *media.Relay, offer []byte) error {
	node := media.CurrentWebRTC()
	if node == nil {
		return errWebRTCOff
	}
	if _, _, err := relay.AddMuxLeg(legCaller, node.Mux()); err != nil {
		return err
	}
	return relay.PrepareWebRTC(legCaller, node, offer)
}

// ---- video policy and negotiation -------------------------------------------

// videoPolicy says whether this call may carry video and why not: the switch
// (HELLO_VIDEO_ENABLED), the extensions' video_allowed, and the node's session
// limit are checked when the video is first offered to a callee.
func (c *call) videoPolicy() (ok bool, reason int) {
	if !c.s.cfg.VideoEnabled {
		return false, media.VideoDisabled
	}
	snap := c.srtpSnapshot()
	if snap != nil {
		for _, num := range []string{c.callerNum, c.dialled} {
			if e, found := snap.Extension(num); found && e.VideoDenied {
				return false, media.VideoNotAllowed
			}
		}
	}
	return true, 0
}

// webVideoKbps is the per-direction ceiling written into the SDP and enforced
// on the relay.
func (c *call) webVideoKbps() int {
	if k := c.s.cfg.VideoMaxKbps; k > 0 {
		return k
	}
	return 1500
}

// noteWebVideoOffer reads the video section of the browser caller's offer and,
// when the policy allows, keeps the codecs Hello will relay. Without a common
// codec, or against the policy, the section is answered with port 0 and the
// call goes on as audio.
func (c *call) noteWebVideoOffer(offer []byte) {
	vo, ok := media.ParseVideoOffer(offer)
	if !ok {
		return
	}
	if ok, why := c.videoPolicy(); !ok {
		media.NoteVideoRefused(why)
		c.addTrace("Video not carried: " + videoWhy(why))
		return
	}
	cands := media.SupportedVideo(vo.Codecs)
	if len(cands) == 0 {
		media.NoteVideoRefused(media.VideoNoCodec)
		c.addTrace("Video not carried: no common video codec (H.264 Constrained Baseline or VP8 needed)")
		return
	}
	w := c.webState()
	c.mu.Lock()
	w.video, w.videoOffer = cands, vo
	c.mu.Unlock()
}

func videoWhy(reason int) string {
	switch reason {
	case media.VideoDisabled:
		return "video is switched off (HELLO_VIDEO_ENABLED)"
	case media.VideoNotAllowed:
		return "an extension of the call may not use video"
	case media.VideoOverLimit:
		return "this node carries the most video calls it may (HELLO_VIDEO_MAX_SESSIONS)"
	case media.VideoNoPeer:
		return "the callee cannot do video"
	}
	return "no common video codec"
}

// takeVideoSlot reserves one of the node's video sessions for the call; false
// when the node is at its limit.
func (c *call) takeVideoSlot() bool {
	w := c.webState()
	c.mu.Lock()
	if w.slot {
		c.mu.Unlock()
		return true
	}
	c.mu.Unlock()
	if !media.AcquireVideoSession() {
		media.NoteVideoRefused(media.VideoOverLimit)
		c.addTrace("Video not carried: " + videoWhy(media.VideoOverLimit))
		return false
	}
	c.mu.Lock()
	w.slot = true
	c.mu.Unlock()
	return true
}

// releaseVideoSlot returns the call's video session slot.
func (c *call) releaseVideoSlot() {
	c.mu.Lock()
	w := c.web
	had := w != nil && w.slot
	if had {
		w.slot = false
	}
	c.mu.Unlock()
	if had {
		media.ReleaseVideoSession()
	}
}

// webOfferB is the offer to a browser fork: the alternate callee leg's DTLS-
// SRTP and ICE-lite audio, in the caller's codecs the browser may be offered,
// and the video the caller offered (in the caller's payload numbering, so
// nothing has to be mapped). Every browser fork of the call is offered the same
// leg: only the one that answers is promoted (promoteWebFork).
func (c *call) webOfferB(offer media.AudioSDP, allow []string, l *leg) []byte {
	r := c.relay
	node := media.CurrentWebRTC()
	if r == nil || node == nil {
		return nil
	}
	if _, err := r.AddAltMuxLeg(node); err != nil {
		c.addTrace("Browser fork not offered: " + err.Error())
		return nil
	}
	if err := r.PrepareWebRTCOffer(media.AltLeg, node); err != nil {
		c.addTrace("Browser fork not offered: " + err.Error())
		return nil
	}
	a, err := r.OfferSDP(media.AltLeg, filteredOffer(offer, allow).PlainOffer(), media.WebPolicy())
	if err != nil {
		c.addTrace("Browser fork not offered: " + err.Error())
		return nil
	}
	w := c.webState()
	c.mu.Lock()
	w.altLeg = true
	cands := media.SupportedVideo(w.video)
	c.mu.Unlock()
	l.web = true
	if len(cands) > 0 && c.takeVideoSlot() {
		r.SetVideoOffer(media.AltLeg, cands, "sendrecv", c.webVideoKbps())
	}
	return a.Build(c.anchorHost, r.LegPort(media.AltLeg), "sendrecv")
}

// webFork settles the alternate leg when a fork answers: a browser fork that
// won becomes the callee leg; any other winner leaves the plain leg and the
// alternate is closed. It reports whether the winner is a browser.
func (c *call) webFork(w *leg) bool {
	r := c.anchorRelay()
	if r == nil {
		return false
	}
	if w != nil && w.web {
		if !r.PromoteAlt() {
			return false
		}
		c.addTrace("The answering device is a browser: its WebRTC leg carries the call")
		return true
	}
	if r.HasAlt() {
		r.DropAlt()
	}
	return false
}

// videoAnswered negotiates the video of a call that was just answered: the
// callee's video section (a browser's answer to the offer of webOfferB, or a
// phone's or door station's on plain RTP) is matched with the codecs the caller
// offered, and both legs are configured to relay it. A callee that answered
// without video, or with a codec the caller cannot use, leaves the caller's
// video section refused and the call as audio.
func (c *call) videoAnswered(calleeBody []byte, calleeWeb bool) {
	r := c.anchorRelay()
	if r == nil {
		return
	}
	w := c.webState()
	c.mu.Lock()
	cands := append([]media.SDPCodec(nil), w.video...)
	callerRTP, callerRTCP := w.plainRTP, w.plainRTCP
	c.mu.Unlock()
	callerWeb := c.webCaller()
	reject := func(why int, say bool) {
		r.SetVideoSection(legCaller, nil, "", 0)
		r.SetVideoSection(legCallee, nil, "", 0)
		if len(cands) > 0 && say {
			media.NoteVideoRefused(why)
			c.addTrace("Video not carried: " + videoWhy(why))
		}
		c.releaseVideoSlot()
	}
	if len(cands) == 0 {
		reject(media.VideoNoCodec, false)
		return
	}
	var vb media.VideoOffer
	var calleeRTP, calleeRTCP *net.UDPAddr
	var ok bool
	if calleeWeb {
		vb, ok = media.ParseVideoOffer(calleeBody)
	} else {
		if !r.HasPlainVideo(legCallee) {
			reject(media.VideoNoPeer, true)
			return
		}
		vb, calleeRTP, calleeRTCP, ok = media.PlainVideoOffer(calleeBody)
	}
	if !ok || len(vb.Codecs) == 0 {
		reject(media.VideoNoPeer, true)
		return
	}
	if !calleeWeb && r.LegEncrypted(legCallee) {
		media.NoteVideoRefused(media.VideoNoCodec)
		c.addTrace("Video not carried: the callee's audio is encrypted and plain RTP video would not be")
		reject(media.VideoNoCodec, false)
		return
	}
	var callerCodec *media.SDPCodec
	var calleeCodec media.SDPCodec
	for _, oc := range cands {
		if sameVideo(oc, vb.Codecs[0], callerWeb || calleeWeb) {
			cc := oc
			cc.Fmtp = vb.Codecs[0].Fmtp
			cc.Feedback = sharedFeedback(oc.Feedback, vb.Codecs[0].Feedback)
			callerCodec = &cc
			calleeCodec = vb.Codecs[0]
			calleeCodec.Feedback = cc.Feedback
			break
		}
	}
	if callerCodec == nil {
		reject(media.VideoNoCodec, true)
		return
	}
	if !callerWeb && !c.ensurePlainVideo(r, legCaller, callerRTP, callerRTCP) {
		reject(media.VideoNoPeer, true)
		c.addTrace("Video not carried: no media port pair")
		return
	}
	if !calleeWeb {
		c.ensurePlainVideo(r, legCallee, calleeRTP, calleeRTCP)
	}
	// Two browsers see sendrecv; with a plain end the directions are the
	// callee's answer toward the caller and the caller's offer toward the callee.
	callerDir, calleeDir := "sendrecv", "sendrecv"
	if !callerWeb || !calleeWeb {
		callerDir = orDirection(vb.Dir)
		c.mu.Lock()
		calleeDir = orDirection(w.videoOffer.Dir)
		c.mu.Unlock()
	}
	kbps := c.webVideoKbps()
	r.SetVideoCap(kbps)
	r.SetVideoSection(legCaller, callerCodec, callerDir, kbps)
	r.SetVideoSection(legCallee, &calleeCodec, calleeDir, kbps)
	c.mu.Lock()
	w.used = true
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Video relayed (%s), at most %d kbit/s each way, not transcoded", strings.ToUpper(calleeCodec.Name), kbps))
	c.videoStarted()
}

// sharedFeedback is the RTCP feedback both ends listed: only what both can use
// is answered.
func sharedFeedback(a, b []string) []string {
	var out []string
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				out = append(out, x)
				break
			}
		}
	}
	return out
}

// ---- in-dialog changes (hold, resume, video up and down) -------------------

// webInDialog applies a browser's re-INVITE to the relay before the answer and
// the peer's offer are written: it refreshes what the leg knows of the offer
// (section ids, ICE restart) and settles the video:
//
//   - video on a call that had none is accepted when the policy allows, both
//     legs are browsers' and the offer holds a codec Hello relays: the codec
//     the offerer listed first goes to the peer as the only one, and a peer
//     that refuses it ends the video again (webVideoPeerAnswer);
//   - video that is gone from the offer (a removed or rejected section, a
//     video-off by the browser) is taken off both legs;
//   - the direction of the video follows the audio's: a hold stops both, and a
//     resume asks both senders for a keyframe.
func (c *call) webInDialog(fromCaller bool, body []byte, dir string) {
	r := c.anchorRelay()
	if r == nil {
		return
	}
	leg, peer := legName(fromCaller), legName(!fromCaller)
	var vo media.VideoOffer
	var offered bool
	var rtp, rtcp *net.UDPAddr
	if r.IsWebLeg(leg) {
		node := media.CurrentWebRTC()
		if err := r.PrepareWebRTC(leg, node, body); err != nil {
			c.addTrace("Browser re-INVITE not usable for media: " + err.Error())
			return
		}
		vo, offered = media.ParseVideoOffer(body)
	} else {
		first, has := media.PlainVideoLine(body)
		r.NotePlainVideoOffer(leg, has, first)
		if !has && !r.HasPlainVideo(leg) {
			return
		}
		vo, rtp, rtcp, offered = media.PlainVideoOffer(body)
		if offered && r.HasPlainVideo(leg) {
			r.SetPlainVideoTarget(leg, rtp, rtcp)
		}
	}
	kbps := c.webVideoKbps()
	cur, have := r.VideoSection(leg)
	if !offered || strings.EqualFold(vo.Dir, "inactive") && !have {
		if have {
			c.webVideoOff(leg, peer, "the sender stopped its video")
		}
		return
	}
	if !have {
		c.webUpgrade(r, leg, peer, vo, rtp, rtcp, dir, kbps)
		return
	}
	// Video goes on: its direction follows the offer's.
	r.SetVideoSection(leg, &cur, mirrorDirection(vo.Dir), kbps)
	if pc, ok := r.VideoSection(peer); ok {
		r.SetVideoSection(peer, &pc, vo.Dir, kbps)
	}
	if vo.Dir == "" || strings.EqualFold(vo.Dir, "sendrecv") {
		// A stream resumes (after a hold): the picture it needs is a keyframe.
		r.RequestKeyframe(leg)
		r.RequestKeyframe(peer)
	}
}

// videoPeerOK reports whether the other end of a mid-call video offer can be
// offered video: a browser, or a plain leg that is not a trunk and not SRTP.
func (c *call) videoPeerOK(r *media.Relay, peer string) bool {
	if r.IsWebLeg(peer) {
		return true
	}
	return c.videoLegAllowed(peer) && !r.LegEncrypted(peer)
}

// webUpgrade adds video to a running call. It reports whether video was taken.
func (c *call) webUpgrade(r *media.Relay, leg, peer string, vo media.VideoOffer, rtp, rtcp *net.UDPAddr, dir string, kbps int) bool {
	if strings.EqualFold(vo.Dir, "inactive") {
		return false
	}
	if ok, why := c.videoPolicy(); !ok {
		media.NoteVideoRefused(why)
		c.addTrace("Video not added: " + videoWhy(why))
		return false
	}
	if !c.videoLegAllowed(leg) || (!r.IsWebLeg(leg) && r.LegEncrypted(leg)) || !c.videoPeerOK(r, peer) {
		media.NoteVideoRefused(media.VideoNoPeer)
		c.addTrace("Video not added: " + videoWhy(media.VideoNoPeer))
		return false
	}
	anyWeb := r.IsWebLeg(leg) || r.IsWebLeg(peer)
	cands := vo.Codecs
	if anyWeb {
		cands = media.SupportedVideo(vo.Codecs)
	}
	if !r.IsWebLeg(peer) || !r.IsWebLeg(leg) {
		cands = h264Only(cands)
	}
	if len(cands) == 0 {
		media.NoteVideoRefused(media.VideoNoCodec)
		c.addTrace("Video not added: " + videoWhy(media.VideoNoCodec))
		return false
	}
	if !c.takeVideoSlot() {
		return false
	}
	if !c.ensurePlainVideo(r, leg, rtp, rtcp) || !c.ensurePlainVideo(r, peer, nil, nil) {
		c.releaseVideoSlot()
		c.addTrace("Video not added: no media port pair")
		return false
	}
	chosen := cands[0]
	r.SetVideoCap(kbps)
	r.SetVideoSection(leg, &chosen, mirrorDirection(vo.Dir), kbps)
	// The peer is offered the one codec, in the sender's numbering; its answer
	// fixes the numbering it actually uses.
	r.SetVideoSection(peer, &chosen, vo.Dir, kbps)
	w := c.webState()
	c.mu.Lock()
	w.used = true
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Video added mid-call (%s)", strings.ToUpper(chosen.Name)))
	c.videoStarted()
	return true
}

// webVideoOff takes video off both legs.
func (c *call) webVideoOff(leg, peer, why string) {
	r := c.anchorRelay()
	if r == nil {
		return
	}
	r.SetVideoSection(leg, nil, "", 0)
	r.SetVideoSection(peer, nil, "", 0)
	c.releaseVideoSlot()
	c.addTrace("Video ended: " + why)
}

// webVideoPeerAnswer reads the peer's answer to a re-INVITE that Hello sent for
// relay leg: video the peer accepted is fixed to the numbering and feedback of
// its answer, video it refused is taken off both legs.
func (c *call) webVideoPeerAnswer(leg string, body []byte) {
	r := c.anchorRelay()
	if r == nil {
		return
	}
	other := legName(leg != legCaller)
	cur, have := r.VideoSection(leg)
	if !have {
		return
	}
	var vb media.VideoOffer
	var ok bool
	if r.IsWebLeg(leg) {
		vb, ok = media.ParseVideoOffer(body)
	} else {
		var rtp, rtcp *net.UDPAddr
		vb, rtp, rtcp, ok = media.PlainVideoOffer(body)
		if ok {
			r.SetPlainVideoTarget(leg, rtp, rtcp)
		}
	}
	if !ok || len(vb.Codecs) == 0 || !sameVideo(cur, vb.Codecs[0], r.IsWebLeg(leg) || r.IsWebLeg(other)) {
		media.NoteVideoRefused(media.VideoNoPeer)
		c.webVideoOff(leg, other, "the other side did not accept it")
		return
	}
	got := vb.Codecs[0]
	got.Feedback = sharedFeedback(cur.Feedback, got.Feedback)
	r.SetVideoSection(leg, &got, mirrorDirection(vb.Dir), c.webVideoKbps())
}

// webVideoCarried reports whether the call carries video now.
func (c *call) webVideoCarried() bool {
	r := c.anchorRelay()
	return r != nil && r.VideoOn()
}

// videoStarted is called when a call begins to carry video: a running
// recording starts its video files (videorec.go).
func (c *call) videoStarted() {
	if c.videoRecordingActive() {
		return
	}
	if c.startVideoRecording() && c.s.cfg.MediaRecordingNotice {
		// The recording began as audio only; both sides are told it now
		// includes video.
		go c.playRecordingNotice(true)
	}
}
