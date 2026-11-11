// SRTP with SDES key exchange on the anchored media path (phone-feature-
// parity S-15). The anchor terminates SRTP per leg: the caller leg and the
// callee leg each have their own keys, offered or accepted only where the leg
// runs over TLS (the keys travel in the SDP, so signalling must be protected)
// and the endpoint's policy allows it. A leg that does not qualify stays
// plain RTP and the other leg is unaffected.
//
// Keys are never logged: media.Crypto redacts itself, and nothing here puts an
// SDP body, an a=crypto line or a key in a log, a trace or an error.
package sip

import (
	"errors"
	"fmt"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/secmedia"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// srtpLeg is what one endpoint of a call may do about SRTP: its policy and
// whether its signalling leg is TLS.
type srtpLeg struct {
	policy secmedia.Policy
	tls    bool
}

// allowed reports whether crypto may be offered or accepted on the leg.
func (l srtpLeg) allowed() bool { return l.policy != secmedia.Off && l.tls }

// required reports whether the leg must be encrypted.
func (l srtpLeg) required() bool { return l.policy == secmedia.Required }

// errSRTPRequired is why a fork is not placed: the device or trunk demands
// SRTP and its leg is not on TLS.
var errSRTPRequired = errors.New("SRTP is required but the leg is not on TLS")

// devicePolicy is a device's SRTP policy from the snapshot (optional when it
// is not known).
func devicePolicy(snap *snapshot.Snapshot, username string) secmedia.Policy {
	if snap != nil {
		if d, ok := snap.DeviceByUsername(username); ok {
			return secmedia.Normalize(d.SRTP, secmedia.Optional)
		}
	}
	return secmedia.Optional
}

// srtpSnapshot is the configuration the call was routed on (set at the gate,
// so a call reads the snapshot once per INVITE); a call that never passed the
// gate (a takeover) reads the current one.
func (c *call) srtpSnapshot() *snapshot.Snapshot {
	c.mu.Lock()
	snap := c.srtpSnap
	c.mu.Unlock()
	if snap != nil {
		return snap
	}
	return c.s.deps.Snapshots.Current()
}

// callerSRTP is the caller endpoint's SRTP capability: a phone's policy over
// its INVITE's transport, or an inbound trunk's policy (trunk legs are UDP).
func (c *call) callerSRTP() srtpLeg {
	if c.callerDevice != "" {
		l := srtpLeg{policy: devicePolicy(c.srtpSnapshot(), c.callerDevice)}
		if c.inv != nil {
			l.tls = c.s.clientTransport(c.inv) == transportTLS
		}
		return l
	}
	if c.trunkSRTP != "" {
		return srtpLeg{policy: secmedia.Normalize(c.trunkSRTP, secmedia.Off)}
	}
	return srtpLeg{policy: secmedia.Off}
}

// calleeSRTP is a fork's SRTP capability.
func (c *call) calleeSRTP(l *leg) srtpLeg {
	switch {
	case l == nil:
		return srtpLeg{policy: secmedia.Off}
	case l.trunk != nil:
		return srtpLeg{policy: secmedia.Normalize(l.trunk.SRTP, secmedia.Off)}
	case l.uri != nil:
		return srtpLeg{policy: secmedia.Off}
	}
	return srtpLeg{policy: devicePolicy(c.srtpSnapshot(), l.binding.Device), tls: bindingOnTLS(l.binding)}
}

// srtpNote counts a negotiation result.
func (c *call) srtpNote(result string) {
	if m := c.s.deps.Media; m != nil {
		m.SRTPNegotiations.WithLabelValues(result).Inc()
	}
}

// srtpGate refuses, with 488, a caller whose offer cannot be honoured: its
// policy requires SRTP and it did not offer usable crypto or is not on TLS,
// or it offers SRTP only on a leg Hello does not encrypt. It reports whether
// the call goes on; when it does not, the response and the CDR are done.
func (c *call) srtpGate(tx sip.ServerTransaction, snap *snapshot.Snapshot) bool {
	c.mu.Lock()
	c.srtpSnap = snap
	c.mu.Unlock()
	req := c.inv
	if req == nil || len(req.Body()) == 0 {
		return true
	}
	off, err := media.ParseAudioSDP(req.Body())
	if err != nil {
		return true // the existing paths decide what an unusable offer means
	}
	lg := c.callerSRTP()
	why := ""
	switch {
	case lg.required() && !lg.tls:
		why = "SRTP is required but the caller is not on TLS"
	case lg.required() && !off.OffersCrypto():
		why = "SRTP is required but the caller offered no usable crypto"
	case off.SRTPOnly && !lg.allowed():
		why = "the caller offered SRTP only, which this leg cannot use (SRTP needs a TLS leg and a policy other than off)"
	case off.SRTPOnly && !off.OffersCrypto():
		why = "the caller offered SRTP only without a usable a=crypto line"
	}
	if why == "" {
		return true
	}
	c.srtpNote("refused")
	c.addTrace("Call refused: " + why)
	c.s.respond(tx, req, sip.StatusNotAcceptableHere, "Not Acceptable Here")
	c.record(sip.StatusNotAcceptableHere, cdr.SideSystem, why, ResultFailed)
	return false
}

// cryptoAnswer adds the caller leg's crypto to an answer to offer when the
// leg allows it and the caller offered some: the leg becomes encrypted. The
// answer's profile follows the offer (RTP/SAVP answers RTP/SAVP; an RTP/AVP
// offer carrying crypto, SRTP optional, is answered the same way).
func (c *call) cryptoAnswer(ans, offer media.AudioSDP) media.AudioSDP {
	r := c.anchorRelay()
	if r == nil {
		return ans
	}
	lg := c.callerSRTP()
	if !offer.OffersCrypto() {
		return ans
	}
	if !lg.allowed() {
		// SRTP offered on a leg that is not TLS (or whose policy is off):
		// the crypto lines are dropped and plain RTP is answered.
		c.srtpNoteOnce(legCaller, "plain")
		return ans
	}
	local, err := r.AcceptCrypto(legCaller, offer.Crypto, offer.SRTPOnly)
	if err != nil {
		c.addTrace("The caller's SRTP offer was not usable; the leg stays plain RTP")
		c.srtpNoteOnce(legCaller, "plain")
		return ans
	}
	c.syncSRTP()
	return ans.WithCrypto(offer.SRTPOnly, local)
}

// srtpNoteOnce counts a leg's result once per call.
func (c *call) srtpNoteOnce(leg, result string) {
	c.mu.Lock()
	if c.srtpNoted == nil {
		c.srtpNoted = map[string]bool{}
	}
	done := c.srtpNoted[leg]
	c.srtpNoted[leg] = true
	c.mu.Unlock()
	if !done {
		c.srtpNote(result)
	}
}

// cryptoOffer adds the callee leg's crypto to an offer to the fork l: its
// policy allows it and the leg is TLS. RTP/SAVP when the policy requires
// SRTP, RTP/AVP with crypto lines (SRTP optional) otherwise.
func (c *call) cryptoOffer(offer media.AudioSDP, l *leg) media.AudioSDP {
	r := c.anchorRelay()
	if r == nil {
		return offer
	}
	lg := c.calleeSRTP(l)
	if !lg.allowed() {
		return offer
	}
	keys, err := r.LocalCrypto(legCallee)
	if err != nil {
		return offer
	}
	return offer.WithCrypto(lg.required(), keys...)
}

// learnCalleeCrypto applies the callee's answer to the relay's callee leg and
// reports whether the call may go on: false only when the fork's policy
// requires SRTP and the answer does not deliver it.
func (c *call) learnCalleeCrypto(l *leg, ans media.AudioSDP) bool {
	r := c.anchorRelay()
	if r == nil {
		return true
	}
	lg := c.calleeSRTP(l)
	secure := false
	if lg.allowed() {
		secure = r.ApplyAnswerCrypto(legCallee, ans.Crypto, lg.required())
	}
	if secure {
		c.syncSRTP()
		return true
	}
	if lg.required() {
		c.srtpNote("refused")
		return false
	}
	if lg.allowed() {
		c.srtpNoteOnce(legCallee, "plain") // offered, and the callee declined
	}
	return true
}

// syncSRTP records which legs are encrypted (for the CDR and the trace) and
// moves the node's SRTP leg gauge; it is cheap and idempotent.
func (c *call) syncSRTP() {
	r := c.anchorRelay()
	if r == nil {
		return
	}
	sa, a := r.Secure(legCaller)
	sb, b := r.Secure(legCallee)
	n := r.SecureLegs()
	c.mu.Lock()
	newA, newB := a && !c.srtpA, b && !c.srtpB
	c.srtpA, c.srtpB = c.srtpA || a, c.srtpB || b
	delta := 0
	if !c.srtpClosed {
		delta = n - c.srtpGauge
		c.srtpGauge = n
	}
	c.mu.Unlock()
	if m := c.s.deps.Media; m != nil {
		if delta != 0 {
			m.SRTPLegs.Add(float64(delta))
		}
		if newA {
			m.SRTPNegotiations.WithLabelValues("srtp").Inc()
		}
		if newB {
			m.SRTPNegotiations.WithLabelValues("srtp").Inc()
		}
	}
	if newA {
		c.addTrace(fmt.Sprintf("SRTP on the caller leg (%s)", sa))
	}
	if newB {
		c.addTrace(fmt.Sprintf("SRTP on the callee leg (%s)", sb))
	}
}

// srtpGaugeRelease takes the call's legs out of the node gauge, at the end of
// its media.
func (c *call) srtpGaugeRelease() {
	c.syncSRTP()
	c.mu.Lock()
	n := c.srtpGauge
	c.srtpGauge = 0
	c.srtpClosed = true
	c.mu.Unlock()
	if m := c.s.deps.Media; m != nil && n != 0 {
		m.SRTPLegs.Sub(float64(n))
	}
}

// srtpCDR is the CDR's srtp value: which legs of the call were encrypted.
func (c *call) srtpCDR() string {
	c.syncSRTP()
	c.mu.Lock()
	a, b := c.srtpA, c.srtpB
	c.mu.Unlock()
	switch {
	case a && b:
		return cdr.SRTPBoth
	case a:
		return cdr.SRTPCaller
	case b:
		return cdr.SRTPCallee
	}
	return cdr.SRTPNone
}

// keepSecurity makes an SDP for relay leg keep the leg's encryption: an
// in-dialog answer or offer built for a leg that is SRTP must carry the leg's
// key again, or the endpoint would see a plain offer and could fall back to
// clear media. A leg that is plain is left alone.
func keepSecurity(r *media.Relay, leg string, a media.AudioSDP) media.AudioSDP {
	if r == nil {
		return a
	}
	savp, on := r.SecureProfile(leg)
	if !on {
		return a
	}
	keys, err := r.LocalCrypto(leg)
	if err != nil {
		return a
	}
	return a.WithCrypto(savp, keys...)
}

// learnCrypto applies an SDP that arrived from the endpoint of relay leg (an
// answer to Hello's offer or a re-INVITE) to the leg's keys: a repeated key
// changes nothing, a new one rekeys the receive side. A leg that is plain is
// never switched on here, and an SDP without crypto does not switch an
// encrypted leg off: the endpoint's plain RTP is dropped instead of being
// accepted in the clear.
func learnCrypto(r *media.Relay, leg string, a media.AudioSDP) {
	if r == nil || !a.OffersCrypto() {
		return
	}
	savp, _ := r.SecureProfile(leg)
	r.LearnCrypto(leg, a.Crypto, savp)
}

// learnPeerAnswer applies the answer to a re-INVITE Hello sent for relay leg:
// the endpoint may have rekeyed.
func (c *call) learnPeerAnswer(r *media.Relay, leg string, body []byte) {
	if len(body) == 0 {
		return
	}
	if a, err := media.ParseAudioSDP(body); err == nil {
		learnCrypto(r, leg, a)
	}
}

// haSRTP is the replicated SRTP mode of relay leg: "" (plain), "savp" or
// "avp" (RTP/AVP with crypto lines), so a taker offers crypto again.
func haSRTP(r *media.Relay, leg string) string {
	if r == nil {
		return ""
	}
	savp, on := r.SecureProfile(leg)
	switch {
	case !on:
		return ""
	case savp:
		return "savp"
	}
	return "avp"
}

// haLegSDP is relaySDPDir for a takeover re-INVITE: a leg that was encrypted
// is offered crypto again, with fresh keys, in the profile it negotiated.
func haLegSDP(host string, r *media.Relay, leg string, off media.AudioSDP, dir, srtp string) string {
	if r == nil {
		return ""
	}
	if srtp != "" {
		if keys, err := r.LocalCrypto(leg); err == nil {
			off = off.WithCrypto(srtp == "savp", keys...)
		}
	}
	return string(off.Build(host, r.LegPort(leg), dir))
}
