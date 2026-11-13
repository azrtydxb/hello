// The webrtc legs of a relay beyond the first answer (clients-webrtc-mobile-
// video S-3 to S-5, S-36 to S-40): the browser-profile state of a mux leg, the
// alternate callee leg a call to a browser is offered on while the plain
// callee leg stays for the other forks, and the video section of the SDP.
//
// The leg's audio SDP comes from the same AudioSDP.Build every other leg uses
// (the Secure of the leg already writes DTLS, ICE-lite and rtcp-mux into it);
// what a browser needs on top is a=mid, the BUNDLE group, every node address
// as a candidate and, with video, a video section on the same transport. That
// is all this file adds.
package media

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	pionsdp "github.com/pion/sdp/v3"
)

// AltLeg names the alternate callee leg in Relay methods. After PromoteAlt it
// is the callee leg and answers to the callee's name.
const AltLeg = "callee-alt"

// secureWebPolicy is what a browser leg always is: encrypted, keyed by DTLS,
// with ICE-lite. A leg that cannot be is refused, never carried in the clear.
var secureWebPolicy = SecurePolicy{Allow: true, Require: true, DTLS: true, ICE: true}

// WebPolicy is the SecurePolicy of a webrtc leg.
func WebPolicy() SecurePolicy { return secureWebPolicy }

// sectionSpec is what decides the m= lines Hello writes for a browser leg
// beyond its audio (copied out of the leg under its lock to render an SDP).
type sectionSpec struct {
	// audioMid and videoMid are the section ids Hello writes. When answering
	// they are the remote's; when offering Hello picks 0 and 1.
	audioMid string
	videoMid string
	// remote lists the sections of the remote's offer in order, so an answer
	// mirrors it (RFC 3264 section 6); empty when Hello made the offer.
	remote  []remoteSection
	offerer bool
	// vOffer is what Hello offers for video (several codecs, the caller's
	// numbering); vSel is the negotiated section (one codec). With neither,
	// an answer rejects the remote's video section and an offer has none.
	vOffer []SDPCodec
	vSel   *SDPCodec
	vDir   string
	// kbps is written as b=AS on the video section when positive.
	kbps int
}

// legWeb is the browser-profile state of a mux leg.
type legWeb struct {
	mu   sync.Mutex
	node *WebRTCNode
	// sentVideo is true once an SDP Hello wrote for the leg carried a video
	// section.
	sentVideo bool
	sectionSpec
}

type remoteSection struct {
	kind, mid, proto, first string
	port0                   bool
}

// VideoOffer is the video section of a browser's offer.
type VideoOffer struct {
	Mid    string
	Dir    string
	Codecs []SDPCodec
}

// ParseVideoOffer returns the first active video section of an SDP, ok false
// when there is none.
func ParseVideoOffer(body []byte) (VideoOffer, bool) {
	s, err := ParseSDPSession(body)
	if err != nil {
		return VideoOffer{}, false
	}
	return videoOfferOf(s)
}

func videoOfferOf(s *SDPSession) (VideoOffer, bool) {
	for _, m := range s.Media() {
		if m.Kind() == "video" && m.Port() != 0 {
			return VideoOffer{Mid: m.Mid(), Dir: m.Direction(), Codecs: m.Codecs()}, true
		}
	}
	return VideoOffer{}, false
}

// IsWebRTCOffer reports whether the first audio section of an SDP is on the
// browser transport (UDP/TLS/RTP/SAVPF) and carries ICE.
func IsWebRTCOffer(body []byte) bool {
	s, err := ParseSDPSession(body)
	if err != nil {
		return false
	}
	for _, m := range s.Media() {
		if m.Kind() == "audio" && m.Port() != 0 {
			p, ok := ProfileOf(m.Proto())
			return ok && p == ProfileWebRTC && m.ICE(s).Ufrag != ""
		}
	}
	return false
}

// SupportedVideo returns, in Hello's order of preference, the codecs of a
// video section Hello will relay: H.264 Constrained Baseline in
// packetization mode 1, then VP8 (S-37). H.265 and AV1 are not offered, and
// neither is a retransmission or FEC payload type: those need machinery the
// relay does not have. Each keeps its own payload type and fmtp, and only the
// feedback the relay can carry (nack, nack pli, ccm fir, goog-remb);
// transport-wide congestion control is dropped because its sequence space is
// per hop.
func SupportedVideo(codecs []SDPCodec) []SDPCodec {
	var h264, vp8 []SDPCodec
	for _, c := range codecs {
		switch strings.ToUpper(c.Name) {
		case "H264":
			if h264ConstrainedBaseline(c.Fmtp) {
				h264 = append(h264, withFeedback(c))
			}
		case "VP8":
			vp8 = append(vp8, withFeedback(c))
		}
	}
	// One H.264 payload type: the first is the offerer's preference.
	out := h264
	if len(out) > 1 {
		out = out[:1]
	}
	if len(vp8) > 0 {
		out = append(out, vp8[0])
	}
	return out
}

func withFeedback(c SDPCodec) SDPCodec {
	var fb []string
	for _, f := range c.Feedback {
		switch strings.ToLower(f) {
		case "nack", "nack pli", "ccm fir", "goog-remb":
			fb = append(fb, strings.ToLower(f))
		}
	}
	c.Feedback = fb
	return c
}

// h264ConstrainedBaseline reports whether an fmtp names packetization mode 1
// and a profile-level-id with profile_idc 0x42 and constraint_set1 (RFC 6184,
// RFC 7742: what every browser implements).
func h264ConstrainedBaseline(fmtp string) bool {
	mode1, profile := false, false
	for _, kv := range strings.Split(fmtp, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		switch strings.ToLower(k) {
		case "packetization-mode":
			mode1 = v == "1"
		case "profile-level-id":
			if len(v) == 6 {
				idc, err1 := strconv.ParseUint(v[0:2], 16, 8)
				cs, err2 := strconv.ParseUint(v[2:4], 16, 8)
				profile = err1 == nil && err2 == nil && idc == 0x42 && cs&0x40 != 0
			}
		}
	}
	return mode1 && profile
}

// SameVideoCodec reports whether two descriptions are the same codec for the
// relay: the same name and, for H.264, the same profile-level-id base profile
// (the two ends then agree on what they decode).
func SameVideoCodec(a, b SDPCodec) bool {
	if !strings.EqualFold(a.Name, b.Name) {
		return false
	}
	if strings.EqualFold(a.Name, "H264") {
		return h264ConstrainedBaseline(a.Fmtp) && h264ConstrainedBaseline(b.Fmtp)
	}
	return true
}

// LegFor turns a negotiated codec into the relay's per-leg configuration.
func LegFor(c SDPCodec) VideoLeg {
	v := VideoLeg{PT: c.PT, Codec: strings.ToUpper(c.Name)}
	for _, f := range c.Feedback {
		switch strings.ToLower(f) {
		case "nack":
			v.FB.NACK = true
		case "nack pli":
			v.FB.PLI = true
		case "ccm fir":
			v.FB.FIR = true
		case "goog-remb":
			v.FB.REMB = true
		}
	}
	return v
}

// ---- leg state ------------------------------------------------------------

// webLeg returns the leg's browser state, nil for a leg that is not one.
func (r *Relay) webLeg(leg string) *relayLeg {
	l := r.findLeg(leg)
	if l == nil || l.web == nil {
		return nil
	}
	return l
}

// IsWebLeg reports whether leg is a webrtc leg.
func (r *Relay) IsWebLeg(leg string) bool { return r.webLeg(leg) != nil }

// PrepareWebRTC readies a mux leg to answer a browser's offer: it validates
// the offer, routes the leg's ICE ufrag so the browser's first check finds the
// leg, and remembers the offer's section ids and order for every later SDP of
// the dialog. It is called for the first offer and again for each re-INVITE
// (a changed offer, an ICE restart). The offer's audio and video sections are
// answered through AnswerSDP and AppendVideo as for any leg.
func (r *Relay) PrepareWebRTC(leg string, node *WebRTCNode, offerBody []byte) error {
	l := r.findLeg(leg)
	if l == nil {
		return errors.New("media: unknown relay leg " + leg)
	}
	ep, ok := l.conn.(*MuxEndpoint)
	if !ok {
		return fmt.Errorf("media: leg %q is not on the WebRTC port", leg)
	}
	if node == nil {
		return errors.New("media: WebRTC is not enabled on this node")
	}
	sess, err := ParseSDPSession(offerBody)
	if err != nil {
		return err
	}
	if err := ValidateWebRTC(sess); err != nil {
		return err
	}
	ufrag, _, ok := l.secure.iceLocal()
	if !ok {
		return errors.New("media: ICE-lite is off on this node")
	}
	if err := ep.SetUfrag(ufrag); err != nil {
		return err
	}
	w := &legWeb{node: node}
	if prev := l.web; prev != nil {
		prev.mu.Lock()
		w.vSel, w.vDir, w.kbps = prev.vSel, prev.vDir, prev.kbps
		w.sentVideo, w.videoMid = prev.sentVideo, prev.videoMid
		prev.mu.Unlock()
	}
	for _, m := range sess.Media() {
		rs := remoteSection{kind: m.Kind(), mid: m.Mid(), proto: m.Proto(), port0: m.Port() == 0}
		if f := m.Formats(); len(f) > 0 {
			rs.first = f[0]
		}
		w.remote = append(w.remote, rs)
		if rs.port0 {
			continue
		}
		switch {
		case m.Kind() == "audio" && w.audioMid == "":
			w.audioMid = m.Mid()
		case m.Kind() == "video" && (w.videoMid == "" || !hadVideoMid(w, m.Mid())):
			w.videoMid = m.Mid()
		}
	}
	l.web = w
	return nil
}

// PrepareWebRTCOffer readies a mux leg for an offer Hello makes to a browser:
// the section ids are 0 (audio) and 1 (video) and the ICE ufrag is routed.
func (r *Relay) PrepareWebRTCOffer(leg string, node *WebRTCNode) error {
	l := r.findLeg(leg)
	if l == nil {
		return errors.New("media: unknown relay leg " + leg)
	}
	ep, ok := l.conn.(*MuxEndpoint)
	if !ok {
		return fmt.Errorf("media: leg %q is not on the WebRTC port", leg)
	}
	if node == nil {
		return errors.New("media: WebRTC is not enabled on this node")
	}
	ufrag, _, ok := l.secure.iceLocal()
	if !ok {
		return errors.New("media: ICE-lite is off on this node")
	}
	if err := ep.SetUfrag(ufrag); err != nil {
		return err
	}
	if l.web == nil {
		l.web = &legWeb{node: node, sectionSpec: sectionSpec{audioMid: "0", videoMid: "1", offerer: true}}
	}
	return nil
}

// SetVideoOffer sets the video Hello offers on an offerer leg: the codecs in
// the other leg's numbering, a direction and the b=AS ceiling in kbit/s. An
// empty list offers no video.
func (r *Relay) SetVideoOffer(leg string, codecs []SDPCodec, dir string, kbps int) {
	if l := r.findLeg(leg); l != nil && l.web == nil {
		if pv := l.pvState(true); pv != nil {
			pv.setOffer(codecs, dir, kbps)
		}
		return
	}
	l := r.webLeg(leg)
	if l == nil {
		return
	}
	l.web.mu.Lock()
	l.web.vOffer = append([]SDPCodec(nil), codecs...)
	l.web.vDir, l.web.kbps = dir, kbps
	l.web.mu.Unlock()
}

// SetVideoSection fixes the video section Hello writes for leg to one codec
// (negotiated), or none (nil: an answer rejects the section, an offer omits
// it), and configures the leg's relay to match. dir is the section direction
// as Hello writes it.
func (r *Relay) SetVideoSection(leg string, c *SDPCodec, dir string, kbps int) {
	if l := r.findLeg(leg); l != nil && l.web == nil {
		pv := l.pvState(c != nil)
		if pv == nil {
			return
		}
		pv.setSection(c, dir, kbps)
		if c == nil {
			l.vid.set(nil)
			return
		}
		v := LegFor(*c)
		if !v.FB.PLI && !v.FB.FIR {
			// A phone or a door station that lists no feedback still answers a
			// picture loss indication with a keyframe (every H.264 encoder
			// built for video calls does); asking costs nothing if it does
			// not. NACK and REMB stay as negotiated.
			v.FB.PLI, v.FB.FIR = true, true
		}
		l.vid.set(&v)
		return
	}
	l := r.webLeg(leg)
	if l == nil {
		return
	}
	l.web.mu.Lock()
	l.web.vOffer = nil
	if c == nil {
		l.web.vSel = nil
	} else {
		cc := *c
		l.web.vSel = &cc
	}
	l.web.vDir, l.web.kbps = dir, kbps
	l.web.mu.Unlock()
	if c == nil {
		l.vid.set(nil)
		return
	}
	v := LegFor(*c)
	l.vid.set(&v)
}

// VideoSection returns the negotiated video codec of leg, ok false when none.
func (r *Relay) VideoSection(leg string) (SDPCodec, bool) {
	if l := r.findLeg(leg); l != nil && l.web == nil {
		pv := l.pvState(false)
		if pv == nil {
			return SDPCodec{}, false
		}
		pv.mu.Lock()
		defer pv.mu.Unlock()
		if pv.vSel == nil {
			return SDPCodec{}, false
		}
		return *pv.vSel, true
	}
	l := r.webLeg(leg)
	if l == nil {
		return SDPCodec{}, false
	}
	l.web.mu.Lock()
	defer l.web.mu.Unlock()
	if l.web.vSel == nil {
		return SDPCodec{}, false
	}
	return *l.web.vSel, true
}

// ---- SDP ------------------------------------------------------------------

// webAudio adds a webrtc leg's extras to an audio SDP about to be built.
func (l *relayLeg) webAudio(a AudioSDP) AudioSDP {
	if l.web == nil {
		if pv := l.pvState(false); pv != nil {
			a.localVideo = pv.section
		}
		return a
	}
	l.web.mu.Lock()
	mid := l.web.audioMid
	l.web.mu.Unlock()
	if mid == "" {
		mid = "0"
	}
	if a.localDTLS != nil {
		a.localProto = ProtoDTLSFB
	}
	a = a.withWeb(mid, l.web.node.Hosts())
	a.localWeb.finish = func(sdp []byte) []byte {
		out, err := l.appendVideo(sdp)
		if err != nil {
			return sdp
		}
		return out
	}
	return a
}

// AppendVideo adds the leg's video section to an audio SDP that Build wrote
// for the leg, and rewrites the BUNDLE group to match. Build already does this
// for every SDP of a browser leg; the method is for callers that hold audio SDP
// built some other way. A leg that is not a browser's, or that has nothing to
// say about video, gets sdp back unchanged.
func (r *Relay) AppendVideo(leg string, sdp []byte) ([]byte, error) {
	l := r.webLeg(leg)
	if l == nil || sdp == nil {
		return sdp, nil
	}
	return l.appendVideo(sdp)
}

func (l *relayLeg) appendVideo(sdp []byte) ([]byte, error) {
	l.web.mu.Lock()
	w := l.web.sectionSpec
	sentVideo := l.web.sentVideo
	l.web.mu.Unlock()
	out, sent, mid, err := renderSections(w, sentVideo, sdp)
	if err == nil && (sent != sentVideo || mid != w.videoMid) {
		l.web.mu.Lock()
		l.web.sentVideo, l.web.videoMid = sent, mid
		l.web.mu.Unlock()
	}
	return out, err
}

// renderSections adds to an audio SDP the sections a browser's dialog needs
// beyond it (see appendVideo) and returns the new "a video section was written"
// and video mid for the leg to remember.
func renderSections(w sectionSpec, sentVideo bool, sdp []byte) (out []byte, sent bool, videoMid string, err error) {
	sent, videoMid = sentVideo, w.videoMid
	hasVideo := w.vSel != nil || len(w.vOffer) > 0
	rejects := !w.offerer && hasRemoteVideo(w.remote)
	// A video section the remote's offer did not have (an upgrade Hello offers
	// in a re-INVITE, or the plain audio-only call a browser began) is added
	// at the end; once written it stays, rejected when the video is gone, so
	// the dialog's m= lines never shrink (RFC 3264 section 8.3).
	extra := !w.offerer && !hasRemoteVideo(w.remote) && (w.vSel != nil || sentVideo)
	keeps := (w.offerer || extra) && sentVideo
	// Whatever else the remote's offer listed (a rejected video line, a data
	// channel) is answered in place, so the answer has the offer's m= lines.
	mirrors := !w.offerer && len(w.remote) > 1
	if !hasVideo && !rejects && !keeps && !extra && !mirrors {
		return sdp, sent, videoMid, nil
	}
	sess, err := ParseSDPSession(sdp)
	if err != nil {
		return sdp, sent, videoMid, err
	}
	ms := sess.Media()
	if len(ms) == 0 {
		return sdp, sent, videoMid, nil
	}
	audio := ms[0]
	mids := []string{audio.Mid()}
	add := func(kind, mid string, c []SDPCodec, dir string) {
		m := sess.AddMedia(kind, audioPort(audio), ProtoDTLSFB, ptList(c)...)
		m.Add("mid", mid)
		m.Add(dir, "")
		m.Add("rtcp-mux", "")
		for _, cc := range c {
			m.Add("rtpmap", fmt.Sprintf("%d %s/%d", cc.PT, cc.Name, cc.Rate))
			if cc.Fmtp != "" {
				m.Add("fmtp", fmt.Sprintf("%d %s", cc.PT, cc.Fmtp))
			}
			for _, f := range cc.Feedback {
				m.Add("rtcp-fb", fmt.Sprintf("%d %s", cc.PT, f))
			}
		}
		copyTransport(m, audio)
		if w.kbps > 0 {
			m.md.Bandwidth = append(m.md.Bandwidth, pionsdp.Bandwidth{Type: "AS", Bandwidth: uint64(w.kbps)}) //nolint:gosec // positive
		}
	}
	reject := func(kind, mid, proto, first string) {
		if first == "" {
			first = "0"
		}
		m := sess.AddMedia(kind, 0, proto, first)
		if mid != "" {
			m.Add("mid", mid)
		}
	}
	switch {
	case w.offerer:
		switch {
		case w.vSel != nil:
			add("video", w.videoMid, []SDPCodec{*w.vSel}, orDir(w.vDir))
			mids = append(mids, w.videoMid)
		case len(w.vOffer) > 0:
			add("video", w.videoMid, w.vOffer, orDir(w.vDir))
			mids = append(mids, w.videoMid)
		case keeps:
			reject("video", w.videoMid, ProtoDTLSFB, "0")
		}
		if hasVideo || keeps {
			sent = true
		}
	default:
		// Mirror the remote's offer: its audio section is our first (Build
		// wrote it); every other section answers in the offer's order.
		seenAudio := false
		for _, rs := range w.remote {
			switch {
			case rs.kind == "audio" && !seenAudio && !rs.port0:
				seenAudio = true
			case rs.kind == "video" && !rs.port0 && rs.mid == w.videoMid && w.vSel != nil:
				add("video", rs.mid, []SDPCodec{*w.vSel}, orDir(w.vDir))
				mids = append(mids, rs.mid)
			default:
				reject(rs.kind, rs.mid, rs.proto, rs.first)
			}
		}
		if !hasRemoteSection(w.remote, "audio") {
			return sdp, sent, videoMid, nil
		}
		if extra {
			mid := w.videoMid
			if mid == "" {
				mid = "v" + w.audioMid
			}
			if w.vSel != nil {
				add("video", mid, []SDPCodec{*w.vSel}, orDir(w.vDir))
				mids = append(mids, mid)
			} else {
				reject("video", mid, ProtoDTLSFB, "0")
			}
			sent, videoMid = true, mid
		}
		// The answer lists its sections in the offer's order, whichever
		// kind the offer began with.
		sess.orderLike(w.remote)
	}
	sess.SetBundle(mids...)
	out, err = sess.Build()
	return out, sent, videoMid, err
}

func orDir(d string) string {
	switch d {
	case "sendonly", "recvonly", "inactive":
		return d
	}
	return "sendrecv"
}

func hasRemoteVideo(r []remoteSection) bool {
	for _, s := range r {
		if s.kind == "video" && !s.port0 {
			return true
		}
	}
	return false
}

func hasRemoteSection(r []remoteSection, kind string) bool {
	for _, s := range r {
		if s.kind == kind && !s.port0 {
			return true
		}
	}
	return false
}

// hasOtherSection reports whether the remote's offer had a section that is
// neither audio nor video (a data channel): the answer rejects it.
func hasOtherSection(r []remoteSection) bool {
	for _, s := range r {
		if s.kind != "audio" && s.kind != "video" {
			return true
		}
	}
	return false
}

func ptList(c []SDPCodec) []string {
	out := make([]string, len(c))
	for i, cc := range c {
		out[i] = strconv.Itoa(int(cc.PT))
	}
	return out
}

func audioPort(m *SDPMedia) int { return m.Port() }

// copyTransport repeats the audio section's ICE, DTLS and candidate
// attributes on a bundled section.
func copyTransport(dst, src *SDPMedia) {
	for _, k := range []string{"ice-ufrag", "ice-pwd", "fingerprint", "setup"} {
		if v, ok := src.Attr(k); ok {
			dst.Add(k, v)
		}
	}
	for _, c := range src.Attrs("candidate") {
		dst.Add("candidate", c)
	}
	if src.Has("end-of-candidates") {
		dst.Add("end-of-candidates", "")
	}
}

// orderLike reorders the session's sections to follow the remote's offer when
// it listed them in another order than audio first: the section for each of
// its entries, matched by mid.
func (s *SDPSession) orderLike(remote []remoteSection) {
	byMid := map[string]*pionsdp.MediaDescription{}
	var rest []*pionsdp.MediaDescription
	for _, md := range s.sd.MediaDescriptions {
		if v, ok := md.Attribute("mid"); ok {
			byMid[v] = md
		} else {
			rest = append(rest, md)
		}
	}
	var out []*pionsdp.MediaDescription
	for _, rs := range remote {
		if md, ok := byMid[rs.mid]; ok {
			out = append(out, md)
			delete(byMid, rs.mid)
		}
	}
	for _, md := range s.sd.MediaDescriptions {
		for _, v := range byMid {
			if v == md {
				out = append(out, md)
			}
		}
	}
	s.sd.MediaDescriptions = append(out, rest...)
}

// ---- alternate callee leg ----------------------------------------------------

// AddAltMuxLeg adds the alternate callee leg on the shared WebRTC port: the
// leg a browser fork of the call is offered. Its media is parked (dropped)
// until PromoteAlt; its ICE and DTLS run from the first check. A relay has at
// most one. It reuses an existing alternate leg.
func (r *Relay) AddAltMuxLeg(node *WebRTCNode) (port int, err error) {
	if node == nil {
		return 0, errors.New("media: WebRTC is not enabled on this node")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || len(r.legs) != 2 {
		return 0, errors.New("media: the alternate leg needs a relay with two legs")
	}
	if r.alt != nil {
		return r.alt.port, nil
	}
	ep, err := node.Mux().NewEndpoint()
	if err != nil {
		return 0, fmt.Errorf("media: alternate leg on the WebRTC port: %w", err)
	}
	l := newRelayLeg(r, r.legs[1].name, ep, node.Mux().Port())
	l.parked.Store(true)
	l.secure.Bind(ep.LocalAddr(), func(b []byte, to *net.UDPAddr) { _, _ = ep.WriteToUDP(b, to) },
		func(from *net.UDPAddr, nominated bool) { l.iceVerified(from, nominated) })
	l.secure.OnFail(func(reason string) { r.secureFailed(l.name, reason) })
	l.secure.OnReady(func() { r.secureUpCall(l.name) })
	r.alt = l
	if r.started {
		r.wg.Add(1)
		go r.runLeg(l)
	}
	return l.port, nil
}

// PromoteAlt makes the alternate leg the callee leg: the browser that answered
// is the callee, the plain callee leg is closed. The codecs the call negotiated
// carry over. It reports false when there is no alternate leg.
func (r *Relay) PromoteAlt() bool {
	r.mu.Lock()
	alt := r.alt
	if alt == nil || r.closed || len(r.legs) != 2 {
		r.mu.Unlock()
		return false
	}
	old := r.legs[1]
	r.alt = nil
	r.legs[1] = alt
	r.mu.Unlock()
	old.copyCodecsTo(alt)
	alt.muted.Store(old.muted.Load())
	alt.parked.Store(false)
	close(old.retired)
	old.secure.Close()
	_ = old.conn.Close()
	if pv := old.pvState(false); pv != nil {
		pv.close()
	}
	return true
}

// DropAlt closes the alternate leg: the call was answered by a callee that is
// not a browser, or by none.
func (r *Relay) DropAlt() {
	r.mu.Lock()
	alt := r.alt
	r.alt = nil
	r.mu.Unlock()
	if alt == nil {
		return
	}
	close(alt.retired)
	alt.secure.Close()
	_ = alt.conn.Close()
}

// HasAlt reports whether an alternate callee leg exists.
func (r *Relay) HasAlt() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.alt != nil
}

// copyCodecsTo gives dst the audio codec state of l.
func (l *relayLeg) copyCodecsTo(dst *relayLeg) {
	l.codecMu.RLock()
	codecs := append([]Codec(nil), l.codecs...)
	accept, anyPT := l.accept, l.anyPT
	l.codecMu.RUnlock()
	dst.codecMu.Lock()
	dst.codecs, dst.accept, dst.anyPT = codecs, accept, anyPT
	dst.codecMu.Unlock()
	dst.dtmfPT, dst.audioPT = l.dtmfPT, l.audioPT
}

// hadVideoMid reports whether the leg already uses mid for video (so a prior
// value carried over from an earlier offer is replaced by the remote's own).
func hadVideoMid(w *legWeb, mid string) bool { return w.videoMid == mid }
