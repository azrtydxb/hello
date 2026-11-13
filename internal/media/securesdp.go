package media

import (
	"errors"
	"fmt"
)

// ErrSRTPRequired is returned when an endpoint must be encrypted and the
// SDES negotiation did not deliver a key: the call is refused, never carried
// in the clear.
var ErrSRTPRequired = errors.New("media: SRTP is required but was not negotiated")

// SecurePolicy is what a media object does about SRTP: Allow says crypto may
// be accepted or offered (the endpoint's policy is not off and its signalling
// is TLS), Require says the endpoint must end up encrypted.
type SecurePolicy struct {
	Allow, Require bool
	// DTLS makes Hello, when it is the offerer, offer DTLS-SRTP
	// (UDP/TLS/RTP/SAVP, RFC 5763) instead of SDES keys. It only applies with
	// Allow.
	DTLS bool
	// ICE makes Hello, when it is the offerer, offer ICE-lite too. An
	// answer carries ICE-lite whenever the offer carried ICE.
	ICE bool
}

// AnswerSDP applies the offerer's crypto to Secure and returns ans carrying
// Hello's answering key: offer is the far end's SDP, ans the SDP Hello is
// about to answer with. A far end that offered no usable key, a policy that
// does not allow crypto, or an unusable suite leaves the endpoint plain and
// ans unchanged, unless the policy requires SRTP, which is ErrSRTPRequired.
// The answer's profile follows the offer (RTP/SAVP answers RTP/SAVP).
//
// An offer on a DTLS transport (UDP/TLS/RTP/SAVP, S-16) is answered with
// Hello's fingerprint and role and the handshake starts on the endpoint's
// socket; it cannot be answered plain, so an endpoint that does not allow
// crypto, or a node with DTLS off, is an error. An offer with ICE credentials
// is answered as ICE-lite.
func (s *Secure) AnswerSDP(offer, ans AudioSDP, p SecurePolicy) (AudioSDP, error) {
	s.peerFrom(offer)
	ans = s.answerICE(offer, ans)
	if offer.OffersDTLS() {
		if !p.Allow {
			return ans, ErrSRTPRequired
		}
		s.sec.mu.Lock()
		info, err := s.acceptDTLS(offer.DTLS)
		if err == nil {
			s.setProto(offer.Proto)
		}
		s.sec.mu.Unlock()
		if err != nil {
			return ans, err
		}
		return s.muxLike(offer, ans.WithDTLS(offer.Proto, info), true), nil
	}
	if !p.Allow || !offer.OffersSDES() {
		if p.Require {
			return ans, ErrSRTPRequired
		}
		return s.muxLike(offer, ans, false), nil
	}
	local, err := s.Accept(offer.Crypto, offer.SRTPOnly)
	if err != nil {
		if p.Require {
			return ans, ErrSRTPRequired
		}
		return s.muxLike(offer, ans, false), nil
	}
	return s.muxLike(offer, ans.WithCrypto(offer.SRTPOnly, local), false), nil
}

// muxLike answers an offer's a=rtcp-mux when the answer is on DTLS or ICE
// (where RTCP shares the media port), and records it on the endpoint.
func (s *Secure) muxLike(offer, ans AudioSDP, dtls bool) AudioSDP {
	if offer.RTCPMux && (dtls || ans.localICE != nil) {
		s.SetMux(true)
		return ans.WithRTCPMux()
	}
	return ans
}

// answerICE answers an offer's ICE credentials as ICE-lite when the endpoint
// can.
func (s *Secure) answerICE(offer, ans AudioSDP) AudioSDP {
	if !offer.ICE.usable() {
		return ans
	}
	u, pw, ok := s.iceLocal()
	if !ok {
		return ans
	}
	s.iceRemote(offer.ICE)
	return ans.WithICE(u, pw)
}

// setProto remembers the m= transport the endpoint negotiated (DTLS), so
// later SDPs keep it (the lock of Secure is held).
func (s *Secure) setProto(proto string) {
	if x := s.xp.Load(); x != nil {
		x.mu.Lock()
		if x.ds != nil {
			x.ds.proto = proto
		}
		x.mu.Unlock()
	}
}

// OfferSDP adds Hello's offered keys to off for an endpoint Hello is the
// offerer to: RTP/SAVP when the policy requires SRTP, RTP/AVP with crypto
// lines otherwise. A policy that does not allow crypto leaves off plain. With
// p.DTLS the offer is DTLS-SRTP instead (UDP/TLS/RTP/SAVP with Hello's
// fingerprint, setup:actpass and rtcp-mux); the handshake starts when the
// answer arrives (LearnAnswer). With p.ICE the offer carries ICE-lite.
func (s *Secure) OfferSDP(off AudioSDP, p SecurePolicy) (AudioSDP, error) {
	if p.ICE {
		if u, pw, ok := s.iceLocal(); ok {
			off = off.WithICE(u, pw)
		}
	}
	if !p.Allow {
		if p.Require {
			return off, ErrSRTPRequired
		}
		return off, nil
	}
	if p.DTLS {
		s.sec.mu.Lock()
		info, err := s.offerDTLS()
		if err == nil {
			s.setProto("UDP/TLS/RTP/SAVP")
		}
		s.sec.mu.Unlock()
		if err == nil {
			s.SetMux(true)
			return off.WithDTLS("UDP/TLS/RTP/SAVP", info).WithRTCPMux(), nil
		}
		// DTLS is off on this node or the endpoint has no socket: SDES.
	}
	keys, err := s.Local()
	if err != nil {
		return off, fmt.Errorf("media: SRTP keys: %w", err)
	}
	return off.WithCrypto(p.Require, keys...), nil
}

// KeepSDP adds the endpoint's active key to a, for an in-dialog answer or
// offer: a later SDP for an encrypted endpoint must repeat its key and
// profile, or the far end would see a plain offer and could fall back to
// clear media. A DTLS endpoint repeats its fingerprint, role and transport,
// and an ICE one its credentials. A plain endpoint is left alone.
func (s *Secure) KeepSDP(a AudioSDP) AudioSDP {
	a = s.keepICE(a)
	if s.DTLSNegotiated() {
		if info, proto, ok := s.dtlsLocal(); ok {
			return a.WithDTLS(proto, info).WithRTCPMux()
		}
	}
	savp, on := s.Profile()
	if !on {
		return a
	}
	keys, err := s.Local()
	if err != nil {
		return a
	}
	return a.WithCrypto(savp, keys...)
}

// keepICE repeats the endpoint's ICE-lite credentials and rtcp-mux.
func (s *Secure) keepICE(a AudioSDP) AudioSDP {
	x := s.xp.Load()
	if x == nil {
		return a
	}
	x.mu.Lock()
	ice, mux := x.ice, x.mux
	x.mu.Unlock()
	if ice == nil {
		return a
	}
	u, pw := ice.local()
	a = a.WithICE(u, pw)
	if mux {
		a = a.WithRTCPMux()
	}
	return a
}

// LearnSDP applies the far end's a=crypto lines, or DTLS fingerprint, from an
// SDP that arrived for the endpoint (an answer to Hello's offer, a
// re-INVITE). A plain endpoint is never switched on by lines it did not
// offer a key for, except as the answer to a pending offer; an SDP without
// crypto does not switch an encrypted endpoint off. A far end that changed
// its fingerprint or ICE credentials starts a new handshake or a new address
// verification.
func (s *Secure) LearnSDP(a AudioSDP) {
	s.peerFrom(a)
	s.iceRemote(a.ICE)
	if a.OffersDTLS() {
		s.sec.mu.Lock()
		s.learnDTLS(a.DTLS)
		s.sec.mu.Unlock()
		return
	}
	if !a.OffersCrypto() {
		return
	}
	savp, _ := s.Profile()
	if _, on := s.Profile(); !on {
		savp = a.SRTPOnly
	}
	s.Learn(a.Crypto, savp)
}

// LearnAnswer applies the far end's answer to an offer Hello made, and
// reports whether the endpoint is now encrypted with that far end: SDES keys
// matching the offer, or a DTLS fingerprint and role (the handshake starts).
// An answer that is none of those leaves the endpoint plain and forgets the
// DTLS and ICE state Hello offered.
func (s *Secure) LearnAnswer(a AudioSDP, savp bool) bool {
	s.peerFrom(a)
	if a.ICE.usable() {
		s.iceRemote(a.ICE)
	} else {
		s.dropICE()
	}
	if a.OffersDTLS() {
		s.sec.mu.Lock()
		ok := s.learnDTLS(a.DTLS)
		s.sec.mu.Unlock()
		if ok && a.RTCPMux {
			s.SetMux(true)
		}
		return ok
	}
	s.dropUnansweredDTLS()
	return s.ApplyAnswer(a.Crypto, savp)
}

// dropICE forgets the ICE-lite agent (the far end did not answer ICE).
func (s *Secure) dropICE() {
	if x := s.xp.Load(); x != nil {
		x.mu.Lock()
		x.ice, x.verified = nil, false
		x.mu.Unlock()
	}
}

// dropUnansweredDTLS forgets a DTLS offer that was answered with something
// else.
func (s *Secure) dropUnansweredDTLS() {
	s.sec.mu.Lock()
	defer s.sec.mu.Unlock()
	if x := s.xp.Load(); x != nil {
		x.mu.Lock()
		if x.ds != nil && !x.ds.started {
			x.ds = nil
		}
		x.mu.Unlock()
	}
}

// ReSDP rebuilds sdp, an SDP Hello wrote, with the endpoint's offered keys:
// the taker of an HA takeover re-INVITEs a phone onto fresh media and an
// endpoint that was encrypted is offered crypto again, in the profile it had.
func (s *Secure) ReSDP(sdp []byte, savp bool) ([]byte, error) {
	mode := "avp"
	if savp {
		mode = "savp"
	}
	return s.ReSDPMode(sdp, mode)
}

// ReSDPMode is ReSDP for a replicated mode (Secure.Mode): "avp", "savp" or
// "dtls". In dtls mode the offer carries a new fingerprint of this node, so
// the far end handshakes again.
func (s *Secure) ReSDPMode(sdp []byte, mode string) ([]byte, error) {
	a, err := ParseAudioSDP(sdp)
	if err != nil {
		return nil, err
	}
	if mode == "dtls" {
		built, err := s.OfferSDP(a, SecurePolicy{Allow: true, Require: true, DTLS: true})
		if err != nil {
			return nil, err
		}
		if built.localDTLS == nil {
			return nil, ErrDTLSOff
		}
		return built.Build(a.Address, a.Port, SDPDirection(sdp)), nil
	}
	keys, err := s.Local()
	if err != nil {
		return nil, fmt.Errorf("media: SRTP keys: %w", err)
	}
	return a.WithCrypto(mode == "savp", keys...).Build(a.Address, a.Port, "sendrecv"), nil
}
