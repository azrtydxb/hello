// The SDP of a webrtc leg (clients-webrtc-mobile-video S-3 to S-5): Hello's
// answer to a browser offer, written on the multi-section model. The answer is
// ICE-lite with host candidates only (the node address and, for remote
// clients, the public one), DTLS-SRTP with Hello's fingerprint, rtcp-mux and
// one BUNDLE group, and it lists exactly the codecs the other leg chose: no
// codec is added, none is transcoded.
//
// The DTLS and ICE machinery itself (certificate, handshake, the ICE-lite
// agent) belongs to the leg's Secure; this file only writes its values into an
// SDP, so the fingerprint and the credentials come in as plain strings.
package media

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// WebRTCLocal is what Hello's side of a webrtc SDP says.
type WebRTCLocal struct {
	// Ufrag and Pwd are the leg's ICE-lite credentials.
	Ufrag, Pwd string
	// Hosts are the candidate addresses: the node address first, then the
	// public one for remote clients. They are the only candidates written.
	Hosts []string
	// Port is the shared WebRTC port (Mux.Port).
	Port int
	// Fingerprint is the a=fingerprint value of the node certificate,
	// "sha-256 AB:CD:...".
	Fingerprint string
	// Setup is the DTLS role Hello announces: active or passive in an answer
	// (RFC 5763 section 5), actpass in an offer.
	Setup string
}

func (l WebRTCLocal) validate() error {
	switch {
	case len(l.Hosts) == 0:
		return errors.New("media: WebRTC SDP needs at least one candidate address")
	case l.Port <= 0 || l.Port > 65535:
		return fmt.Errorf("media: WebRTC SDP port %d is out of range", l.Port)
	case len(l.Ufrag) < 4 || len(l.Pwd) < 22:
		return errors.New("media: WebRTC SDP needs ICE credentials")
	case !fingerprintUsable(l.Fingerprint):
		return errors.New("media: WebRTC SDP needs a certificate fingerprint")
	case l.Setup != "active" && l.Setup != "passive" && l.Setup != "actpass":
		return fmt.Errorf("media: WebRTC SDP setup %q is not a DTLS role", l.Setup)
	}
	for _, h := range l.Hosts {
		if ip := net.ParseIP(h); ip == nil || ip.To4() == nil {
			return fmt.Errorf("media: candidate address %q is not an IPv4 address", h)
		}
	}
	return nil
}

// candidates are the a=candidate values for the hosts on the shared port:
// foundation per address, component 1, descending local preference so the
// node address is tried before the public one.
func (l WebRTCLocal) candidates() []string {
	out := make([]string, 0, len(l.Hosts))
	for i, h := range l.Hosts {
		prio := 126<<24 | (65535-i)<<8 | 255
		out = append(out, fmt.Sprintf("%d 1 udp %d %s %d typ host", i+1, prio, h, l.Port))
	}
	return out
}

// applyTransport writes the ICE, DTLS and mux attributes of a section.
func (m *SDPMedia) applyTransport(l WebRTCLocal) {
	m.Add("ice-ufrag", l.Ufrag)
	m.Add("ice-pwd", l.Pwd)
	for _, c := range l.candidates() {
		m.Add("candidate", c)
	}
	m.Add("end-of-candidates", "")
	m.Add("fingerprint", l.Fingerprint)
	m.Add("setup", l.Setup)
	m.Add("rtcp-mux", "")
}

// mirrorDirection is the answer direction to an offered one (RFC 3264 section
// 6.1).
func mirrorDirection(d string) string {
	switch d {
	case "sendonly":
		return "recvonly"
	case "recvonly":
		return "sendonly"
	case "inactive":
		return "inactive"
	}
	return "sendrecv"
}

// AnswerWebRTC writes Hello's answer to a browser or app offer. audio is the
// codec choice for the offer's audio section, in the offer's payload numbering
// (AnswerFor of the offer and the other leg's answer), so the answer names
// what the callee chose and nothing else; its Ptime and fmtp lines carry
// over. The first audio section is answered on the shared port, every other
// section is rejected (port 0): video is not accepted until the video phases.
// The offer must pass ValidateWebRTC, and its fingerprint and setup role are
// the caller's to read before the call (Hello answers active to actpass and
// passive, passive to active: answerSetup of the DTLS code), so local.Setup
// is whatever that decided.
func AnswerWebRTC(offer *SDPSession, audio AudioSDP, local WebRTCLocal) (*SDPSession, error) {
	if err := ValidateWebRTC(offer); err != nil {
		return nil, err
	}
	if err := local.validate(); err != nil {
		return nil, err
	}
	if len(audio.Codecs) == 0 {
		return nil, errors.New("media: no codec to answer the WebRTC offer with")
	}
	ans := NewSDPSession(local.Hosts[0], uint64(local.Port)) //nolint:gosec // validate() made the port positive
	ans.SetSessionAttr("ice-lite", "")
	done := false
	var bundle []string
	for _, om := range offer.Media() {
		proto := om.Proto()
		if om.Kind() != "audio" || done || om.Port() == 0 {
			// A rejected section keeps the offer's first format (RFC 3264
			// section 6) and its transport.
			f := om.Formats()
			if len(f) == 0 {
				f = []string{"0"}
			}
			rm := ans.AddMedia(om.Kind(), 0, proto, f[0])
			if mid := om.Mid(); mid != "" {
				rm.Add("mid", mid)
			}
			continue
		}
		done = true
		var pts []string
		for _, c := range audio.Codecs {
			pts = append(pts, strconv.Itoa(int(c.PT)))
		}
		for _, e := range audio.Events {
			pts = append(pts, strconv.Itoa(int(e.PT)))
		}
		am := ans.AddMedia("audio", local.Port, ProtoDTLSFB, pts...)
		am.SetAddress(local.Hosts[0])
		am.Add("mid", om.Mid())
		am.Add(mirrorDirection(om.Direction()), "")
		for _, c := range append(append([]Codec(nil), audio.Codecs...), audio.Events...) {
			am.Add("rtpmap", rtpmapValue(c))
			if c.Fmtp != "" {
				am.Add("fmtp", fmt.Sprintf("%d %s", c.PT, c.Fmtp))
			}
		}
		if audio.Ptime > 0 {
			am.Add("ptime", strconv.Itoa(audio.Ptime))
		}
		am.applyTransport(local)
		bundle = append(bundle, om.Mid())
	}
	if !done {
		return nil, errors.New("media: the WebRTC offer has no audio section")
	}
	ans.SetBundle(bundle...)
	return ans, nil
}

func rtpmapValue(c Codec) string {
	v := fmt.Sprintf("%d %s/%d", c.PT, c.Name, c.ClockRate)
	if c.Channels > 1 {
		v += "/" + strconv.Itoa(c.Channels)
	}
	return v
}

// SessionHasCleartext reports whether any active section of the session
// would carry media outside SRTP: a plain RTP/AVP transport or one without
// DTLS or SDES keys. A webrtc leg's SDP must never.
func (s *SDPSession) SessionHasCleartext() bool {
	for _, m := range s.Media() {
		if m.Port() == 0 {
			continue
		}
		p, ok := ProfileOf(m.Proto())
		if !ok || p == ProfilePlain {
			return true
		}
		if p == ProfileSDES {
			if keys, _ := m.Crypto(); len(keys) == 0 {
				return true
			}
		}
		if (p == ProfileDTLS || p == ProfileWebRTC) && len(m.DTLS(s).Fingerprints) == 0 {
			return true
		}
	}
	return false
}

// CandidateAddresses returns the addresses of every a=candidate of the
// session, for tests and for the call trace.
func (s *SDPSession) CandidateAddresses() []string {
	var out []string
	for _, m := range s.Media() {
		for _, c := range m.Attrs("candidate") {
			f := strings.Fields(c)
			if len(f) >= 5 {
				out = append(out, f[4])
			}
		}
	}
	return out
}
