package sip

import (
	"context"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
	"github.com/emiago/sipgo/sip"
)

// AlarmCode is the number a WP8x6 dials for its alarm (Emergency Call, P22270)
// and a side key can be set to (spec phone-feature-parity S-29).
const AlarmCode = "*55"

// handleAlarm serves a phone's call to *55. The destination is the alarm
// destination of the phone group of the calling device: an extension or ring
// group, rung with the ring tag alarm whatever its DND, with a CDR of kind
// alarm. The answering party hears "panic alarm from <name>" before the
// caller. A phone whose group has no destination (or whose destination was
// deleted) is answered 404.
func (c *call) handleAlarm(tx sip.ServerTransaction, snap *snapshot.Snapshot, dev snapshot.Device) {
	s, req := c.s, c.inv
	c.mu.Lock()
	c.kind, c.ignoreDND = cdr.KindAlarm, true
	c.mu.Unlock()
	target := snap.AlarmTarget(dev.Username)
	if target == "" {
		c.addTrace("Alarm call: the phone's group has no alarm destination")
		s.m.Emergency.WithLabelValues("alarm", "no_destination").Inc()
		s.respond(tx, req, sip.StatusNotFound, "Not Found")
		c.record(sip.StatusNotFound, cdr.SideSystem, "no alarm destination", ResultNotFound)
		return
	}
	_, _, isGroup := snap.RingGroup(target)
	if !isGroup && !snap.HasExtension(target) {
		c.addTrace(fmt.Sprintf("Alarm call: the destination %s does not exist", target))
		s.m.Emergency.WithLabelValues("alarm", "no_destination").Inc()
		s.respond(tx, req, sip.StatusNotFound, "Not Found")
		c.record(sip.StatusNotFound, cdr.SideSystem, "alarm destination "+target+" does not exist", ResultNotFound)
		return
	}
	c.addTrace(fmt.Sprintf("Alarm call from %s to %s: DND ignored, ring tag alarm", dev.Extension, target))
	s.m.Emergency.WithLabelValues("alarm", "ringing").Inc()
	c.rewritten = target
	c.ringTarget(req, tx, snap, target)
}

// playAlarmNotice tells the answering party who the alarm is from, before the
// two talk. It plays to the callee leg only and never delays or fails the
// call: without speech it plays the page tone.
func (c *call) playAlarmNotice() {
	s := c.s
	defer contain(s.log, "alarm notice")
	r := c.anchorRelay()
	if r == nil {
		return
	}
	lang := voiceprompt.Parse(s.cfg.PromptLanguage)
	c.mu.Lock()
	who := c.callerName
	if who == "" {
		who = c.callerNum
	}
	c.mu.Unlock()
	pcm, ok := s.speakPCM(voiceprompt.Text(lang, voiceprompt.AlarmAnnounce, voiceprompt.Vars{"who": who}))
	if !ok {
		c.addTrace("Alarm notice: speech unavailable, played the page tone")
		pcm = media.PageTonePCM()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := r.PlayPCMTo(ctx, legCallee, pcm, 8000); err != nil {
		s.log.Debug("alarm notice playback failed", "error", err)
		return
	}
	c.addTrace("Alarm notice played to the answering party")
}

// alarmCall reports whether the call is a *55 alarm call.
func (c *call) alarmCall() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.kind == cdr.KindAlarm
}
