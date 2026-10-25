package sip

import (
	"context"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// queuedIn names the queue the call is waiting in, "" when it is not queued.
func (c *call) queuedIn() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ended || c.ivr == nil || !c.ivr.active.Load() || c.qName == "" || c.qAgent != "" {
		return ""
	}
	return c.qName
}

// transferIntoQueue carries out a blind transfer whose target is a queue: the
// transferee's leg drops and the caller, who stays on the relay, joins the
// queue as a call of its own (a second CDR, like any transferred call). notify
// reports the outcome to the transferor.
func (c *call) transferIntoQueue(snap *snapshot.Snapshot, q snapshot.Queue, w *leg, kind string, notify func(string, bool)) {
	s := c.s
	fail := func(code int, why string) {
		c.addTrace(fmt.Sprintf("Transfer to queue %s failed: %s", q.Name, why))
		s.m.Transfers.WithLabelValues(kind, TransferFailed).Inc()
		notify(fmt.Sprintf("SIP/2.0 %d %s", code, statusText(code)), true)
		c.noteHAState("", "")
	}
	c.mu.Lock()
	relay, host, anchored, dss := c.relay, c.anchorHost, c.anchored, c.dss
	var vmTag string
	if dss != nil && dss.InviteResponse != nil {
		vmTag = tagParam(dss.InviteResponse.To())
	}
	sdp := c.haLegs[0].sdp
	c.mu.Unlock()
	switch {
	case !q.Enabled || s.queueStore() == nil:
		fail(sip.StatusServiceUnavailable, "the queue is unavailable")
		return
	case relay == nil || !anchored || dss == nil:
		fail(sip.StatusNotAcceptableHere, "the call is not anchored")
		return
	}
	off, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil {
		fail(sip.StatusNotAcceptableHere, "the caller's offer is not G.711")
		return
	}
	// The transferor's hold, if any, is over: the caller sends to the relay
	// again, and hears the queue's music from here.
	aOffer := media.BuildAudioSDP(host, relay.LegPort(legCaller), off.PayloadType, off.DTMFPayloadType, off.DTMFRate)
	if aAns, ok := reinviteA(dss, aOffer); ok {
		haAim(relay, legCaller, aAns)
	}
	c.mu.Lock()
	c2 := s.newCall(c.inv)
	c2.callID = c.callID
	c2.dialled = orDefault(q.Number, q.Name)
	c2.callerNum, c2.callerName, c2.callerDevice = c.callerNum, c.callerName, c.callerDevice
	c2.direction, c2.dss = c.direction, c.dss
	c2.callerDialogUp, c2.haShared = true, true
	c2.events = make(chan legEvent, 16)
	c2.setupDone = make(chan struct{})
	c2.ringTime = c.ringTime
	c2.anchorHost = host
	c.mu.Unlock()
	c2.adoptRelay(c, relay)
	st := newIVRState()
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now()
	c2.mu.Lock()
	c2.ivr, c2.ivrCancel = st, cancel
	c2.connected, c2.answerTime, c2.ivrAnswered = true, now, now
	c2.setupClosed = true
	c2.vmTag = vmTag
	c2.mu.Unlock()
	s.m.ActiveCalls.Inc()
	s.bind(c2.callID, dialogRef{c: c2})
	go c2.heartbeat()
	c2.publish()
	w.bye() // the transferee's leg drops once the caller is queued
	c.end(sip.StatusOK, cdr.SideCallee, kind+" transfer", ResultAnswered)
	s.noteCallEnd(c.callerNum, c2.dialled)
	if s.deps.HAState != nil && sdp != "" {
		// Replicated like any queued caller: a taker re-INVITEs the caller
		// and puts it back in the queue. After c.end, whose record deletion
		// shares the caller dialog's key.
		c2.haSoloRefresh(sdp)
		c2.mu.Lock()
		c2.haShared = false
		c2.haPhase, c2.haDetail = haPhaseQueue, haQueueDetailPrefix+q.Name+":0"
		c2.mu.Unlock()
		go c2.haLoop()
	}
	s.m.Transfers.WithLabelValues(kind, TransferAnswered).Inc()
	c2.addTrace(fmt.Sprintf("Transferred into queue %s by %s", q.Name, c.dialled))
	notify("SIP/2.0 200 OK", true)
	go func() {
		defer contain(s.log, "queue flow after transfer")
		c2.queueSession(ctx, snap, q, 0)
	}()
}
