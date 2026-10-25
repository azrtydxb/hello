package sip

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// ivrPageGroup pages a group from an IVR option. The caller is already
// answered on the relay: the relay's callee leg is aimed at the page's
// fan-out, so the caller's audio reaches every member that answers, one way,
// exactly as for a dialled page code. It reports whether the menu is
// finished with the call; false means nobody could be paged and the menu
// repeats.
func (c *call) ivrPageGroup(ctx context.Context, snap *snapshot.Snapshot, code string) bool {
	s := c.s
	g, ok := snap.PagingGroup(code)
	relay := c.anchorRelay()
	switch {
	case !ok || relay == nil:
		c.addTrace(fmt.Sprintf("IVR: paging group %s does not exist", code))
		return false
	case !g.Enabled:
		c.addTrace("IVR: paging group " + g.Name + " is disabled")
		s.m.PagingTotal.WithLabelValues("denied").Inc()
		return false
	case len(g.Callers) > 0 && !slices.Contains(g.Callers, c.callerNum):
		c.addTrace("IVR: " + c.callerNum + " may not page " + g.Name)
		s.m.PagingTotal.WithLabelValues("denied").Inc()
		return false
	}
	off, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil {
		c.addTrace("IVR: the page needs a G.711 offer")
		return false
	}
	emergency := g.Priority == snapshot.PagingEmergency
	busy := s.busyExtensions()
	ps := &pageState{group: g, host: c.anchorHost, pt: off.PayloadType}
	var targets []pageBinding
	for _, ext := range g.Members {
		if ext == c.callerNum {
			continue
		}
		e, _ := snap.Extension(ext)
		switch {
		case e.DND && !emergency:
			ps.skipped = append(ps.skipped, pageResult{ext, PageLegDND})
			continue
		case busy[ext]:
			ps.skipped = append(ps.skipped, pageResult{ext, PageLegBusy})
			continue
		}
		bs, err := c.extensionBindings(snap, ext)
		if err != nil || len(bs) == 0 {
			ps.skipped = append(ps.skipped, pageResult{ext, PageLegNoRegistration})
			continue
		}
		for _, b := range bs {
			targets = append(targets, pageBinding{ext, b})
		}
	}
	for _, sk := range ps.skipped {
		c.addTrace(fmt.Sprintf("Page member %s skipped: %s", sk.ext, sk.result))
		s.m.PagingLegs.WithLabelValues(sk.result).Inc()
	}
	if len(targets) == 0 || len(targets) > s.cfg.PageFanout {
		c.addTrace("IVR: no member of " + g.Name + " can be paged")
		s.m.PagingTotal.WithLabelValues("no_answer").Inc()
		return false
	}
	c.mu.Lock()
	c.page = ps // before admission: another page reads the owner's group
	c.kind = cdr.KindPage
	c.mu.Unlock()
	undo := func() {
		c.pageStop()
		c.mu.Lock()
		c.page, c.kind = nil, ""
		c.mu.Unlock()
	}
	preempted, ok := s.pageAdmit(c, g)
	if !ok {
		c.addTrace("IVR: page capacity or group " + g.Name + " is busy")
		s.m.PagingTotal.WithLabelValues("capacity").Inc()
		c.mu.Lock()
		c.page, c.kind = nil, ""
		c.mu.Unlock()
		return false
	}
	if preempted != nil {
		preempted.terminate("preempted by an emergency page")
	}
	fan, err := media.NewFanout(s.cfg.RTPPortMin, s.cfg.RTPPortMax, off.PayloadType)
	if err != nil {
		c.addTrace("Page media failed: " + err.Error())
		undo()
		return false
	}
	ps.fan = fan
	ps.events = make(chan pageEvent, len(targets)+1)
	relay.SetTarget(legCallee, udpAddr("127.0.0.1", fan.CallerPort()))
	offer := media.BuildAudioSDPDir(ps.host, fan.MemberPort(), off.PayloadType, 0, 0, "sendonly")
	c.addTrace(fmt.Sprintf("IVR: paging group %s (%s): %d legs", g.Name, g.Priority, len(targets)))
	c.mu.Lock()
	for _, t := range targets {
		pl := &pageLeg{ext: t.ext, offer: offer}
		pl.l = c.addLeg(&leg{binding: t.b, pl: pl})
		ps.mu.Lock()
		ps.legs = append(ps.legs, pl)
		ps.mu.Unlock()
	}
	legs := slices.Clone(ps.legs)
	c.mu.Unlock()
	for _, pl := range legs {
		go pl.l.runPage() //nolint:gosec // the leg runs on its own context, like a dialled page
	}
	pending := len(legs)
	timer := time.NewTimer(s.cfg.PageAnswerTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return true // the call ended; end() stopped the page
		case ev := <-ps.events:
			if ev.answered {
				c.ivrPageAnswered(g)
				return true
			}
			if pending--; pending > 0 {
				continue
			}
		case <-timer.C:
		}
		c.addTrace("IVR: no member of " + g.Name + " answered")
		s.m.PagingTotal.WithLabelValues("no_answer").Inc()
		c.cancelForks(nil)
		undo()
		return false
	}
}

// ivrPageAnswered keeps the call up as a page: it ends at the page maximum
// or when the caller hangs up.
func (c *call) ivrPageAnswered(g snapshot.PagingGroup) {
	c.mu.Lock()
	c.setupClosed = true
	c.haSolo, c.haPhase, c.haDetail = false, "", ""
	if c.maxTimer != nil {
		c.maxTimer.Stop()
	}
	if !c.ended {
		c.maxTimer = time.AfterFunc(c.s.cfg.PageMaxDuration, c.expire)
	}
	st := c.ivr
	c.mu.Unlock()
	st.active.Store(false)
	c.ivrFinishSetup()
	c.addTrace("Page established")
	c.s.m.PagingTotal.WithLabelValues("answered").Inc()
	c.publish()
}
