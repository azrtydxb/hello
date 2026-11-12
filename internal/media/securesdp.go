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
}

// AnswerSDP applies the offerer's crypto to Secure and returns ans carrying
// Hello's answering key: offer is the far end's SDP, ans the SDP Hello is
// about to answer with. A far end that offered no usable key, a policy that
// does not allow crypto, or an unusable suite leaves the endpoint plain and
// ans unchanged, unless the policy requires SRTP, which is ErrSRTPRequired.
// The answer's profile follows the offer (RTP/SAVP answers RTP/SAVP).
func (s *Secure) AnswerSDP(offer, ans AudioSDP, p SecurePolicy) (AudioSDP, error) {
	if !p.Allow || !offer.OffersCrypto() {
		if p.Require {
			return ans, ErrSRTPRequired
		}
		return ans, nil
	}
	local, err := s.Accept(offer.Crypto, offer.SRTPOnly)
	if err != nil {
		if p.Require {
			return ans, ErrSRTPRequired
		}
		return ans, nil
	}
	return ans.WithCrypto(offer.SRTPOnly, local), nil
}

// OfferSDP adds Hello's offered keys to off for an endpoint Hello is the
// offerer to: RTP/SAVP when the policy requires SRTP, RTP/AVP with crypto
// lines otherwise. A policy that does not allow crypto leaves off plain.
func (s *Secure) OfferSDP(off AudioSDP, p SecurePolicy) (AudioSDP, error) {
	if !p.Allow {
		if p.Require {
			return off, ErrSRTPRequired
		}
		return off, nil
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
// clear media. A plain endpoint is left alone.
func (s *Secure) KeepSDP(a AudioSDP) AudioSDP {
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

// LearnSDP applies the far end's a=crypto lines from an SDP that arrived for
// the endpoint (an answer to Hello's offer, a re-INVITE). A plain endpoint
// is never switched on by lines it did not offer a key for, except as the
// answer to a pending offer; an SDP without crypto does not switch an
// encrypted endpoint off.
func (s *Secure) LearnSDP(a AudioSDP) {
	if !a.OffersCrypto() {
		return
	}
	savp, _ := s.Profile()
	if _, on := s.Profile(); !on {
		savp = a.SRTPOnly
	}
	s.Learn(a.Crypto, savp)
}

// ReSDP rebuilds sdp, an SDP Hello wrote, with the endpoint's offered keys:
// the taker of an HA takeover re-INVITEs a phone onto fresh media and an
// endpoint that was encrypted is offered crypto again, in the profile it had.
func (s *Secure) ReSDP(sdp []byte, savp bool) ([]byte, error) {
	a, err := ParseAudioSDP(sdp)
	if err != nil {
		return nil, err
	}
	keys, err := s.Local()
	if err != nil {
		return nil, fmt.Errorf("media: SRTP keys: %w", err)
	}
	return a.WithCrypto(savp, keys...).Build(a.Address, a.Port, "sendrecv"), nil
}
