// Leg profiles (clients-webrtc-mobile-video S-3): every relay leg is plain
// RTP, SDES SRTP or WebRTC, chosen from the m= transport of the endpoint's
// SDP. The anchor builds the other leg's SDP in that leg's profile; the
// rewrite between the legs does not change.
package media

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Profile is the transport profile of one leg.
type Profile string

const (
	// ProfilePlain is RTP/AVP.
	ProfilePlain Profile = "plain"
	// ProfileSDES is RTP/SAVP keyed by a=crypto (phone-feature-parity S-15).
	ProfileSDES Profile = "sdes"
	// ProfileDTLS is UDP/TLS/RTP/SAVP: DTLS-SRTP without the browser rules
	// (a desk phone doing DTLS, phone-feature-parity S-16).
	ProfileDTLS Profile = "dtls"
	// ProfileWebRTC is UDP/TLS/RTP/SAVPF: DTLS-SRTP with ICE, rtcp-mux and
	// BUNDLE, what a browser or an app speaks.
	ProfileWebRTC Profile = "webrtc"
)

// Proto values of the m= line.
const (
	ProtoAVP    = "RTP/AVP"
	ProtoSAVP   = "RTP/SAVP"
	ProtoDTLS   = "UDP/TLS/RTP/SAVP"
	ProtoDTLSFB = "UDP/TLS/RTP/SAVPF"
)

// ProfileOf maps an m= transport to the leg profile; ok is false for a
// transport Hello does not relay (RTP/AVPF without SRTP, TCP/..., SCTP).
func ProfileOf(proto string) (Profile, bool) {
	switch strings.ToUpper(proto) {
	case ProtoAVP:
		return ProfilePlain, true
	case ProtoSAVP:
		return ProfileSDES, true
	case ProtoDTLS:
		return ProfileDTLS, true
	case ProtoDTLSFB:
		return ProfileWebRTC, true
	}
	return "", false
}

// Proto is the m= transport Hello writes for the profile.
func (p Profile) Proto() string {
	switch p {
	case ProfileSDES:
		return ProtoSAVP
	case ProfileDTLS:
		return ProtoDTLS
	case ProfileWebRTC:
		return ProtoDTLSFB
	}
	return ProtoAVP
}

// Encrypted reports whether the profile carries SRTP.
func (p Profile) Encrypted() bool { return p != ProfilePlain && p != "" }

// ErrWebRTCSDP is wrapped by every ValidateWebRTC failure.
var ErrWebRTCSDP = errors.New("media: not an acceptable WebRTC SDP")

// ValidateWebRTC checks an offer or answer meant for a webrtc leg against
// what S-4 requires of one: every active section is DTLS-SRTP (SAVP or SAVPF),
// has rtcp-mux (RFC 8835), carries ICE credentials and a usable fingerprint
// with a setup role, and the sections that are active sit in one BUNDLE group
// when there is more than one. A rejected section (port 0) is not checked.
// It reports the first problem, naming the section.
func ValidateWebRTC(s *SDPSession) error {
	active := 0
	var mids []string
	for i, m := range s.Media() {
		if m.Port() == 0 {
			continue
		}
		active++
		where := fmt.Sprintf("m=%s section %d", m.Kind(), i)
		p, ok := ProfileOf(m.Proto())
		if !ok || (p != ProfileWebRTC && p != ProfileDTLS) {
			return fmt.Errorf("%w: %s uses transport %q, want DTLS-SRTP", ErrWebRTCSDP, where, m.Proto())
		}
		if !m.RTCPMux() {
			return fmt.Errorf("%w: %s has no a=rtcp-mux", ErrWebRTCSDP, where)
		}
		ice := m.ICE(s)
		if ice.Ufrag == "" || ice.Pwd == "" {
			return fmt.Errorf("%w: %s has no ICE credentials", ErrWebRTCSDP, where)
		}
		if len(ice.Ufrag) < 4 || len(ice.Ufrag) > 256 || len(ice.Pwd) < 22 || len(ice.Pwd) > 256 {
			return fmt.Errorf("%w: %s has ICE credentials of an illegal length", ErrWebRTCSDP, where)
		}
		d := m.DTLS(s)
		usable := false
		for _, f := range d.Fingerprints {
			if fingerprintUsable(f) {
				usable = true
			}
		}
		if !usable {
			return fmt.Errorf("%w: %s has no usable a=fingerprint", ErrWebRTCSDP, where)
		}
		switch d.Setup {
		case "active", "passive", "actpass":
		default:
			return fmt.Errorf("%w: %s has no a=setup role", ErrWebRTCSDP, where)
		}
		if m.Mid() == "" {
			return fmt.Errorf("%w: %s has no a=mid", ErrWebRTCSDP, where)
		}
		mids = append(mids, m.Mid())
	}
	if active == 0 {
		return fmt.Errorf("%w: no active media section", ErrWebRTCSDP)
	}
	if active > 1 {
		in := map[string]bool{}
		for _, id := range s.Bundle() {
			in[id] = true
		}
		for _, id := range mids {
			if !in[id] {
				return fmt.Errorf("%w: mid %q is not in the BUNDLE group", ErrWebRTCSDP, id)
			}
		}
	}
	return nil
}

// fingerprintUsable reports whether an a=fingerprint value names a hash
// function Hello accepts (sha-1, sha-256, sha-384, sha-512; md5 and md2 are
// refused) with a digest of that function's length (RFC 8122). The check that
// the peer's certificate matches it belongs to the DTLS handshake.
func fingerprintUsable(v string) bool {
	f := strings.Fields(v)
	if len(f) != 2 {
		return false
	}
	size := map[string]int{"sha-1": 20, "sha-256": 32, "sha-384": 48, "sha-512": 64}[strings.ToLower(f[0])]
	sum, err := hex.DecodeString(strings.ReplaceAll(f[1], ":", ""))
	return size > 0 && err == nil && len(sum) == size
}
