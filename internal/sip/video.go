package sip

import (
	"github.com/azrtydxb/hello/internal/media"
)

// Door station video (spec phone-feature-parity S-49). When a door station
// offers H.264 video (as the caller, or as the callee a user dialled), the
// anchor relays the video's RTP and RTCP port pairs unchanged next to the
// audio relay, and the SDP on each side carries a video m-line with Hello's
// own ports. The callee's answer decides: a callee that accepts the video
// gets it, one that does not gets none and the door's offer is answered with
// a refused (port 0) video line. A call with no door station never carries
// video, whatever its SDP offers, and nothing here is added to an audio-only
// offer. A takeover, transfer or re-anchor carries audio only.

// callVideo is a call's video state (guarded by call.mu).
type callVideo struct {
	// relay carries the RTP and RTCP; nil when no video port could be bound
	// (the offer is then refused).
	relay *media.VideoRelay
	// offer is what the caller offered, H.264 only; answer is what the callee
	// accepted, nil until it answers or when it refused.
	offer  media.VideoSDP
	answer *media.VideoSDP
}

// involvesDoor reports whether a door station is a party of the call: it
// called, or a user dialled its number.
func (c *call) involvesDoor() bool {
	c.mu.Lock()
	caller := c.doorCaller
	c.mu.Unlock()
	if caller {
		return true
	}
	snap := c.s.deps.Snapshots.Current()
	return snap != nil && snap.IsDoor(c.dialled)
}

// startVideo sets up the video relay for a door call whose caller offered
// H.264 video. It runs when the audio anchor is created.
func (c *call) startVideo(offerBody []byte) {
	if !c.involvesDoor() {
		return
	}
	v, ok := media.ParseVideoSDP(offerBody)
	if !ok {
		return
	}
	cv := &callVideo{offer: v}
	relay, err := media.NewVideoRelay(c.s.cfg.RTPPortMin, c.s.cfg.RTPPortMax)
	if err != nil {
		c.addTrace("Video not carried: no media port pair")
	} else {
		relay.SetTarget(legCaller, v.RTPAddr())
		relay.SetRTCPTarget(legCaller, v.RTCPAddr())
		allowSignalSource(relay.Relay, legCaller, c.inv.Source())
		relay.Start()
		cv.relay = relay
		c.addTrace("Video offered by the caller (H.264), relayed through the anchor")
	}
	c.mu.Lock()
	c.video = cv
	c.mu.Unlock()
}

// noteVideoAnswer reads the callee's answer for video: an accepted H.264
// section aims the callee leg of the video relay at it.
func (c *call) noteVideoAnswer(calleeBody []byte, source string) {
	c.mu.Lock()
	cv := c.video
	c.mu.Unlock()
	if cv == nil || cv.relay == nil {
		return
	}
	v, ok := media.ParseVideoSDP(calleeBody)
	if !ok {
		c.addTrace("Video refused by the callee")
		return
	}
	cv.relay.SetTarget(legCallee, v.RTPAddr())
	cv.relay.SetRTCPTarget(legCallee, v.RTCPAddr())
	allowSignalSource(cv.relay.Relay, legCallee, source)
	c.mu.Lock()
	cv.answer = &v
	c.mu.Unlock()
	c.addTrace("Video accepted by the callee (H.264)")
}

// videoFacing is the video section of an SDP that faces side: the callee
// gets the caller's offer on the callee leg's ports, the caller gets the
// callee's answer on the caller leg's ports, or a refused line when the
// callee did not take the video. Empty when the call carries no video.
func (c *call) videoFacing(side string) string {
	c.mu.Lock()
	cv, host := c.video, c.anchorHost
	c.mu.Unlock()
	if cv == nil {
		return ""
	}
	switch {
	case side == legCallee && cv.relay != nil:
		return cv.offer.Section(host, cv.relay.LegPort(legCallee), "")
	case side == legCallee:
		return ""
	}
	c.mu.Lock()
	ans := cv.answer
	c.mu.Unlock()
	if ans != nil && cv.relay != nil {
		return ans.Section(host, cv.relay.LegPort(legCaller), "")
	}
	return media.RejectedVideoSection(cv.offer)
}

// withVideo appends the video section facing side to an audio SDP.
func (c *call) withVideo(sdp []byte, side string) []byte {
	if sdp == nil {
		return sdp
	}
	if s := c.videoFacing(side); s != "" {
		return append(sdp[:len(sdp):len(sdp)], s...)
	}
	return sdp
}

// closeVideo stops the video relay with the call.
func (c *call) closeVideo() {
	c.mu.Lock()
	cv := c.video
	c.mu.Unlock()
	if cv != nil && cv.relay != nil {
		cv.relay.Close()
	}
}
