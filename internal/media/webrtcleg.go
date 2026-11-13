package media

import (
	"errors"
	"fmt"
)

// AnswerWebRTC answers a browser or app offer on a leg made by AddMuxLeg: the
// join of the multi-section SDP model, the leg's Secure (ICE-lite agent and
// DTLS-SRTP handshake) and the shared port.
//
// The offer must pass ValidateWebRTC. choose receives the offer as AudioSDP and
// returns the codec choice in the offer's payload numbering (AnswerFor of the
// offer and the other leg's answer). The leg's local ufrag is routed to its
// endpoint before the answer exists, so the browser's first check finds the
// leg; the handshake starts as Secure.AnswerSDP says (Hello is the DTLS client
// to an actpass offer). The leg is encrypted or the call is refused: a leg
// that cannot be keyed is never answered in the clear.
func (r *Relay) AnswerWebRTC(leg string, node *WebRTCNode, offerBody []byte, choose func(offer AudioSDP) AudioSDP) ([]byte, error) {
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
