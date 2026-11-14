package media

import (
	"errors"
	"fmt"
)

// SignalingHop says how the offer reached this node from the edge (Kamailio).
// The offer carries the browser's DTLS fingerprint, which is only as
// trustworthy as the signalling path that carried it, so an offer that came
// over a cleartext hop is refused unless the operator allowed it
// (HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP, default false).
type SignalingHop struct {
	// Encrypted is true when the offer arrived over the TLS hop between the
	// edge and this node (S-12).
	Encrypted bool
	// AllowCleartext is the operator's permission to answer anyway.
	AllowCleartext bool
}

// ErrCleartextHop is why an offer is refused: it arrived over a cleartext
// edge-to-node hop and HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP is false.
var ErrCleartextHop = errors.New("media: the WebRTC offer arrived over a cleartext signalling hop, so its DTLS fingerprint cannot be trusted: enable the TLS hop (HELLO_SIP_TLS_ADDR on the node, KAMAILIO_HELLO_TLS_PORT on the edge) or set HELLO_MEDIA_WEBRTC_ALLOW_CLEARTEXT_HOP=true")

// Check reports ErrCleartextHop when the offer may not be answered.
func (h SignalingHop) Check() error {
	if h.Encrypted || h.AllowCleartext {
		return nil
	}
	return ErrCleartextHop
}

// AnswerWebRTC answers a browser or app offer on a leg made by AddMuxLeg: the
// join of the multi-section SDP model, the leg's Secure (ICE-lite agent and
// DTLS-SRTP handshake) and the shared port. hop is how the offer reached this
// node; over a cleartext hop it is refused with ErrCleartextHop before
// anything is routed or started, unless the hop allows it.
//
// The offer must pass ValidateWebRTC. choose receives the offer as AudioSDP and
// returns the codec choice in the offer's payload numbering (AnswerFor of the
// offer and the other leg's answer). The leg's local ufrag is routed to its
// endpoint before the answer exists, so the browser's first check finds the
// leg; the handshake starts as Secure.AnswerSDP says (Hello is the DTLS client
// to an actpass offer). The leg is encrypted or the call is refused: a leg
// that cannot be keyed is never answered in the clear.
func (r *Relay) AnswerWebRTC(leg string, hop SignalingHop, node *WebRTCNode, offerBody []byte, choose func(offer AudioSDP) AudioSDP) ([]byte, error) {
	if err := hop.Check(); err != nil {
		return nil, err
	}
	l := r.findLeg(leg)
	if l == nil {
		return nil, errors.New("media: unknown relay leg " + leg)
	}
	ep, ok := l.conn.(*MuxEndpoint)
	if !ok {
		return nil, fmt.Errorf("media: leg %q is not on the WebRTC port", leg)
	}
	if node == nil {
		return nil, errors.New("media: WebRTC is not enabled on this node")
	}
	sess, err := ParseSDPSession(offerBody)
	if err != nil {
		return nil, err
	}
	if err := ValidateWebRTC(sess); err != nil {
		return nil, err
	}
	offer, err := ParseAudioSDP(offerBody)
	if err != nil {
		return nil, err
	}
	if !offer.OffersDTLS() || !offer.ICE.usable() {
		return nil, fmt.Errorf("%w: the audio section carries no DTLS-SRTP or ICE", ErrWebRTCSDP)
	}
	choice := choose(offer)
	if len(choice.Codecs) == 0 {
		return nil, errors.New("media: no codec to answer the WebRTC offer with")
	}
	ufrag, pwd, ok := l.secure.iceLocal()
	if !ok {
		return nil, errors.New("media: ICE-lite is off on this node")
	}
	if err := ep.SetUfrag(ufrag); err != nil {
		return nil, err
	}
	if _, err := l.secure.AnswerSDP(offer, choice, SecurePolicy{Allow: true, Require: true}); err != nil {
		return nil, err
	}
	info, _, ok := l.secure.dtlsLocal()
	if !ok || len(info.Fingerprints) == 0 {
		return nil, errors.New("media: the DTLS session did not start")
	}
	ans, err := AnswerWebRTC(sess, choice, node.Local(ufrag, pwd, info.Fingerprints[0].String(), info.Setup))
	if err != nil {
		return nil, err
	}
	return ans.Build()
}
