package sip

import (
	"github.com/azrtydxb/hello/internal/media"
)

// callerOffer is the caller's SDP offer: the INVITE's, or for a call a
// survivor took over (it has no INVITE) the SDP the old owner replicated.
func (c *call) callerOffer() []byte {
	if c.inv != nil {
		return c.inv.Body()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.takenOffer
}

// queueHomeAgent finishes the connection of a taken-over call to the agent
// that answered it. A taken-over call keeps its dialogs as replicated data
// (internal/sip/takeover.go), not as sipgo sessions; the agent's leg was set
// up here as a normal fork, so its dialog is read into the same replicated
// form and becomes the call's second homed leg. From then on the call is an
// ordinary taken-over two-leg call: a BYE from either side ends it, and it
// replicates (and can be taken over again) like any other.
func (c *call) queueHomeAgent(w *leg) {
	hom := c.homed()
	if hom == nil || w == nil {
		return
	}
	dcs := w.session()
	if dcs == nil || dcs.InviteRequest == nil || dcs.InviteResponse == nil {
		return
	}
	s := c.s
	off, err := media.ParseAudioSDP(c.callerOffer())
	c.mu.Lock()
	host, relay := c.anchorHost, c.relay
	c.mu.Unlock()
	if err != nil || relay == nil {
		return
	}
	snap := haLegSnap{
		callID:   w.callID,
		localTag: tagParam(dcs.InviteRequest.From()), remoteTag: tagParam(dcs.InviteResponse.To()),
		localCSeq:    s.nextCSeq(w.callID, dcs.InviteRequest.CSeq().SeqNo, dcs.CSEQ()),
		remoteCSeq:   dcs.InviteRequest.CSeq().SeqNo,
		routes:       reversed(headerValues(dcs.InviteResponse, "Record-Route")),
		localID:      uriString(*dcs.InviteRequest.From().Address.Clone()),
		remoteID:     uriString(*dcs.InviteRequest.To().Address.Clone()),
		remoteTarget: uriString(w.target()),
		endpoint:     w.binding.AOR,
		source:       dcs.InviteResponse.Source(),
		sdp:          relaySDPDir(host, relay, legCallee, off, ""),
		remoteSDP:    string(dcs.InviteResponse.Body()),
	}
	b := haLegFrom(c.haSnapLeg(snap, contactURI(s)), false)
	if b == nil {
		return
	}
	hom.setCallee(b)
	c.noteHAState("", "")
}
