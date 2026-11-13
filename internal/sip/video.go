package sip

import (
	"bytes"
	"net"
	"strings"

	"github.com/azrtydxb/hello/internal/media"
)

// Video on plain RTP legs (clients-webrtc-mobile-video S-44, phone-feature-
// parity S-49). A desk phone or a door station that offers H.264 on a video
// m-line gets the same treatment as a browser: its video runs through the
// relay's translator (constant offsets, one SSRC per leg, RTCP ended per leg,
// PLI, FIR, NACK and REMB carried across, the rate ceiling) on a port pair of
// the leg of its own, so a phone and a browser, two phones and a door station
// and a phone are each one pairing of VideoLeg configurations. The decision is
// the one webrtc.go makes for browsers: policy, a node slot, a common codec; a
// callee that cannot answers m=video 0 and the call goes on as audio. A call
// whose audio leg is SRTP never carries plain video beside it, and a trunk
// never carries video at all. A takeover, a transfer or a re-anchor carries
// audio only.

// h264Only keeps the H.264 payload types of a candidate list.
func h264Only(in []media.SDPCodec) []media.SDPCodec {
	var out []media.SDPCodec
	for _, c := range in {
		if strings.EqualFold(c.Name, "H264") {
			out = append(out, c)
		}
	}
	return out
}

// sameH264Plain is the codec match between two plain endpoints: H.264 with the
// same packetization mode and, when both name one, the same profile_idc. The
// relay does not look inside, so what matters is that the two decoders agree.
func sameH264Plain(a, b media.SDPCodec) bool {
	if !strings.EqualFold(a.Name, "H264") || !strings.EqualFold(b.Name, "H264") {
		return false
	}
	return fmtpParam(a.Fmtp, "packetization-mode", "0") == fmtpParam(b.Fmtp, "packetization-mode", "0") &&
		profileIDC(a.Fmtp) == profileIDC(b.Fmtp)
}

func fmtpParam(fmtp, key, def string) string {
	for _, kv := range strings.Split(fmtp, ";") {
		k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
		if strings.EqualFold(k, key) {
			return v
		}
	}
	return def
}

func profileIDC(fmtp string) string {
	v := fmtpParam(fmtp, "profile-level-id", "")
	if len(v) >= 2 {
		return strings.ToLower(v[:2])
	}
	return ""
}

// sameVideo decides whether two descriptions are one codec for the relay.
func sameVideo(a, b media.SDPCodec, anyWeb bool) bool {
	if anyWeb {
		return media.SameVideoCodec(a, b)
	}
	return sameH264Plain(a, b)
}

// videoLegAllowed reports whether leg of the call may carry video at all: not a
// trunk or a URI destination, and its audio is not encrypted when it is a plain
// leg.
func (c *call) videoLegAllowed(leg string) bool {
	if leg == legCaller {
		return c.inTrunk == nil
	}
	w := c.winnerLeg()
	return w == nil || (w.trunk == nil && w.uri == nil)
}

// noteVideoOffer reads the video of the caller's first offer: a browser's
// section through the browser's negotiation, a phone's or door station's H.264
// section on plain RTP. Whatever is decided, an offer that had a video section
// is answered with one (port 0 unless the video is carried).
func (c *call) noteVideoOffer(r *media.Relay, offer []byte) {
	if c.webCaller() {
		c.noteWebVideoOffer(offer)
		return
	}
	first, has := media.PlainVideoFirstPT(offer)
	r.NotePlainVideoOffer(legCaller, has, first)
	if !has {
		return
	}
	if !c.videoLegAllowed(legCaller) {
		c.addTrace("Video not carried: trunks carry audio only")
		return
	}
	vo, rtp, rtcp, ok := media.PlainVideoOffer(offer)
	if !ok {
		if media.IsWebRTCOffer(offer) {
			return
		}
		media.NoteVideoRefused(media.VideoNoCodec)
		c.addTrace("Video not carried: no H.264 over plain RTP in the video offer")
		return
	}
	if ok, why := c.videoPolicy(); !ok {
		media.NoteVideoRefused(why)
		c.addTrace("Video not carried: " + videoWhy(why))
		return
	}
	if r.LegEncrypted(legCaller) {
		media.NoteVideoRefused(media.VideoNoCodec)
		c.addTrace("Video not carried: the audio is encrypted and plain RTP video would not be")
		return
	}
	w := c.webState()
	c.mu.Lock()
	w.video, w.videoOffer = vo.Codecs, vo
	w.plainRTP, w.plainRTCP = rtp, rtcp
	c.mu.Unlock()
}

// offerPlainVideo adds the video the caller offered to the offer for a plain
// callee (a phone or a door station): the H.264 types it listed, in its own
// numbering and with its direction. Every fork shares the callee leg's video
// ports, as it shares its audio port.
func (c *call) offerPlainVideo(l *leg) {
	r := c.anchorRelay()
	if r == nil || l == nil {
		return
	}
	w := c.webState()
	c.mu.Lock()
	cands := h264Only(w.video)
	dir := w.videoOffer.Dir
	c.mu.Unlock()
	if len(cands) == 0 {
		return
	}
	if l.trunk != nil || l.uri != nil {
		c.addTrace("Video not carried: trunks carry audio only")
		return
	}
	if !c.takeVideoSlot() {
		return
	}
	if _, err := r.BindPlainVideo(legCallee); err != nil {
		c.releaseVideoSlot()
		c.addTrace("Video not carried: no media port pair")
		return
	}
	r.SetVideoOffer(legCallee, cands, orDirection(dir), c.webVideoKbps())
}

func orDirection(d string) string {
	switch d {
	case "sendonly", "recvonly", "inactive":
		return d
	}
	return "sendrecv"
}

// withVideo appends the video section of a plain leg to an audio SDP built for
// side when the build did not already write one.
func (c *call) withVideo(sdp []byte, side string) []byte {
	r := c.anchorRelay()
	if sdp == nil || r == nil || r.IsWebLeg(side) || bytes.Contains(sdp, []byte("\nm=video")) {
		return sdp
	}
	if s := r.PlainVideoSection(side, c.anchorHost); s != "" {
		return append(sdp[:len(sdp):len(sdp)], s...)
	}
	return sdp
}

// ensurePlainVideo gives a plain leg its video sockets and aims them at the
// addresses its SDP names.
func (c *call) ensurePlainVideo(r *media.Relay, leg string, rtp, rtcp *net.UDPAddr) bool {
	if r.IsWebLeg(leg) {
		return true
	}
	if _, err := r.BindPlainVideo(leg); err != nil {
		return false
	}
	if rtp != nil {
		r.SetPlainVideoTarget(leg, rtp, rtcp)
	}
	return true
}
