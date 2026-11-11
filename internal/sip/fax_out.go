package sip

// Outbound fax (pbx-gaps S-15): the API queues a PDF, a node claims it under a
// lease, places the call through the outbound routes as a call from the fax
// service, and hello-fax transmits. A fax that cannot connect (busy, no
// answer, no carrier) is retried, 3 attempts 5 minutes apart.

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/fax"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/emiago/sipgo/sip"
)

const (
	faxSendEvery = 5 * time.Second
	// faxMaxSending bounds the faxes one node sends at the same time.
	faxMaxSending = 4
	// faxRouteSource is the source outbound routing sees for a fax: a route
	// that lists source extensions does not carry faxes; one that applies to
	// every extension does.
	faxRouteSource = "fax"
)

// faxSendLoop claims queued faxes and sends them until ctx ends.
func (s *Server) faxSendLoop(ctx context.Context) {
	defer contain(s.log, "fax send loop")
	var sending atomic.Int32
	every := faxSendEvery
	if s.cfg.FaxInterval > 0 {
		every = s.cfg.FaxInterval
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-tick.C:
		}
		fd := s.faxOn()
		if fd == nil || s.deps.Snapshots == nil {
			continue
		}
		room := faxMaxSending - int(sending.Load())
		if st, _ := s.state(); room <= 0 || st != cluster.Ready {
			continue
		}
		cctx, cancel := context.WithTimeout(ctx, callbackDBWait)
		rows, err := fd.Store.ClaimOutbound(cctx, s.cfg.NodeID, fax.SendLease, room)
		cancel()
		if err != nil {
			s.log.Warn("fax: claim failed", "error", err)
			continue
		}
		for _, f := range rows {
			sending.Add(1)
			s.bg.Go(func() {
				defer sending.Add(-1)
				defer contain(s.log, "fax send")
				s.faxSendOne(fd, f)
			})
		}
	}
}

// faxRetry puts a fax whose call did not connect back in the queue, or gives
// up after the last attempt.
func (s *Server) faxRetry(fd *FaxDeps, f fax.Fax, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), faxStoreWait)
	defer cancel()
	if f.Attempts < fax.MaxAttempts {
		if err := fd.Store.Retry(ctx, f.ID, fax.RetryAfter, why); err != nil {
			s.log.Warn("fax: not requeued", "fax_id", f.ID, "error", err)
		}
		s.m.FaxTotal.WithLabelValues(fax.DirOutbound, "retry").Inc()
		return
	}
	s.faxGiveUp(fd, f, why)
}

// faxGiveUp ends a fax for good.
func (s *Server) faxGiveUp(fd *FaxDeps, f fax.Fax, why string) {
	ctx, cancel := context.WithTimeout(context.Background(), faxStoreWait)
	defer cancel()
	if err := fd.Store.Finish(ctx, f.ID, fax.Finish{Status: fax.StatusFailed, ResultText: why}); err != nil {
		s.log.Warn("fax: not closed", "fax_id", f.ID, "error", err)
	}
	s.m.FaxTotal.WithLabelValues(fax.DirOutbound, "failed").Inc()
}

// newFaxOutCall builds the call of one attempt: no INVITE, a carrier leg.
func (s *Server) newFaxOutCall(f fax.Fax) *call {
	c := &call{
		s: s, id: newID(), callerNum: f.LocalNumber, dialled: f.PeerNumber,
		start: time.Now(), direction: cdr.DirectionOutbound, kind: cdr.KindFax, mediaMode: "anchored",
		canceled: make(chan struct{}), stopHB: make(chan struct{}), setupDone: make(chan struct{}),
		aborted: make(chan struct{}), haStop: make(chan struct{}), pickupCh: make(chan *pickupReq, 1),
		events: make(chan legEvent, 16),
	}
	c.callerDialogUp = true // no caller INVITE to answer: respondA stays quiet
	return c
}

// faxSendOne places the call of one claimed fax and hands it to the engine.
func (s *Server) faxSendOne(fd *FaxDeps, f fax.Fax) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		s.faxRetry(fd, f, "the node has no configuration yet")
		return
	}
	gctx, cancel := context.WithTimeout(context.Background(), faxStoreWait)
	pdf, err := fd.Objects.Get(gctx, f.ObjectKey)
	cancel()
	if err != nil {
		s.log.Warn("fax: document not read", "fax_id", f.ID, "error", err)
		s.faxRetry(fd, f, "the document could not be read")
		return
	}
	c := s.newFaxOutCall(f)
	s.m.ActiveCalls.Inc()
	_ = fd.Store.Link(context.Background(), f.ID, c.id)
	c.addTrace(fmt.Sprintf("Fax %d to %s, attempt %d", f.ID, f.PeerNumber, f.Attempts))
	fail := func(status int, why string, retry bool) {
		c.addTrace("Fax attempt failed: " + why)
		c.mu.Lock()
		c.faxNote = "fax failed: " + why
		c.mu.Unlock()
		c.end(status, cdr.SideCallee, why, ResultFailed)
		if retry {
			s.faxRetry(fd, f, why)
		} else {
			s.faxGiveUp(fd, f, why)
		}
	}

	dec := s.decide(snap.Routing(), routing.Call{FromExtension: faxRouteSource, Number: f.PeerNumber, CallerID: f.LocalNumber, At: time.Now()})
	c.mu.Lock()
	c.trace = append(c.trace, dec.Trace...)
	c.route, c.rewritten = dec.Route, dec.Number
	c.mu.Unlock()
	if dec.Kind != routing.KindOutbound {
		fail(sip.StatusNotFound, "no outbound route carries "+f.PeerNumber+": "+orDefault(dec.Reason, "no route"), false)
		return
	}
	relay, err := media.NewRelay(s.cfg.RTPPortMin, s.cfg.RTPPortMax)
	if err != nil {
		fail(sip.StatusServiceUnavailable, "media ports: "+err.Error(), true)
		return
	}
	portEngine, err1 := relay.AddLeg(legCaller)
	_, err2 := relay.AddLeg(legCallee)
	if err1 != nil || err2 != nil {
		relay.Close()
		fail(sip.StatusServiceUnavailable, "media ports unavailable", true)
		return
	}
	_ = portEngine
	host := s.anchorHostOr(s.advHost)
	if len(dec.Candidates) > 0 && dec.Candidates[0].Trunk != nil && len(dec.Candidates[0].Destinations) > 0 {
		host = s.anchorHostOr(dec.Candidates[0].Destinations[0].Host)
	}
	g711 := []media.Codec{}
	for _, pt := range []uint8{0, 8} {
		if cd, ok := media.StaticCodec(pt); ok {
			g711 = append(g711, cd)
		}
	}
	body := media.BuildSDP(host, relay.LegPort(legCaller), g711, nil, "sendrecv")
	off, _ := media.ParseAudioSDP(body)
	relay.SetCodecs(legCaller, off.Codecs, 0)
	relay.SetCodecs(legCallee, off.Codecs, 0)
	if m := s.deps.Media; m != nil {
		m.NoteStart()
	}
	fx := &faxCall{outbound: true, rowID: f.ID, peer: f.PeerNumber, local: f.LocalNumber, host: host, relay: relay,
		carrier: legCallee, engine: legCaller, pdf: pdf}
	// The trunk the route picks decides whether T.38 may be asked for.
	for _, cand := range dec.Candidates {
		if cand.Trunk != nil && cand.Trunk.FaxMode == fax.ModeT38 {
			fx.t38OK = true
		}
	}
	c.mu.Lock()
	c.relay, c.anchored, c.anchorHost, c.cbOffer, c.fx = relay, true, host, body, fx
	c.callID = newID()
	c.mu.Unlock()
	c.observeRelay(relay)
	c.publish()
	go c.heartbeat()

	plan := callbackPlan{dec: dec, host: host, external: true}
	w, status, result, reason := c.callbackDial(plan)
	if w == nil {
		relay.Close()
		retry := result != "cancelled"
		switch result {
		case "busy", "no_answer", "silent", "failed", "draining":
			retry = true
		}
		fail(status, orDefault(reason, result), retry)
		return
	}
	if !c.faxHomeOut(fd, fx, w, off) {
		w.bye()
		relay.Close()
		fail(sip.StatusServiceUnavailable, "the carrier answered with media Hello cannot use", true)
		return
	}
	c.addTrace(fmt.Sprintf("Fax %d: %s answered", f.ID, f.PeerNumber))
	if err := c.faxOpenSession(fd, fx, fax.ModePassthrough, fx.pt); err != nil {
		w.bye()
		c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "the fax engine is unavailable", ResultFailed)
		s.faxRetry(fd, f, "the fax engine is unavailable: "+err.Error())
		return
	}
	c.faxRun(fx)
}

// faxHomeOut makes the carrier's answer the call's one dialog: the relay
// aims at it, and the call counts as connected.
func (c *call) faxHomeOut(fd *FaxDeps, fx *faxCall, w *leg, off media.AudioSDP) bool {
	s := c.s
	dcs := w.session()
	if dcs == nil || dcs.InviteResponse == nil {
		return false
	}
	res := dcs.InviteResponse
	ans, err := media.ParseAudioSDP(res.Body())
	if err != nil || len(ans.Codecs) == 0 {
		return false
	}
	// Only G.711 carries a T.30 fax over audio.
	pt := ans.Codecs[0].PT
	if pt != 0 && pt != 8 {
		return false
	}
	c.cancelForks(w)
	w.ack(nil, "")
	target := udpAddr(ans.Address, ans.Port)
	if target == nil {
		return false
	}
	fx.relay.SetTarget(legCallee, target)
	allowSignalSource(fx.relay, legCallee, res.Source())
	fx.relay.Start()
	fx.w, fx.pt = w, pt
	now := time.Now()
	c.mu.Lock()
	c.winner, c.setupClosed = w, true
	c.connected, c.answerTime = true, now
	c.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, c.expire)
	c.mu.Unlock()
	c.publish()
	return true
}
