// Voicemail groups (spec pbx-gaps S-14). A message left for a group, or
// broadcast by a member with *63<number>, is recorded once and delivered as
// one stored object and one message row per member box (copy on delivery).
// Nothing rings. The group, its members and their box ids ride the
// configuration snapshot, so no PostgreSQL call precedes the answer.
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

// Voicemail application modes for a group (see vmLeave and vmRetrieve).
const (
	// vmGroupLeave records a message for the group's members.
	vmGroupLeave = iota + 2
	// vmGroupBroadcast is the same, started by a member with *63<number>.
	vmGroupBroadcast
)

// vmGroupReady reports whether this node can record and store a message.
func (s *Server) vmGroupReady() bool {
	return s.deps.Voicemails != nil && s.deps.Objects != nil
}

// vmGroupRefusal is why group g cannot take a message from this call (the
// SIP status and reason phrase), or 0 when it can. broadcast demands that the
// caller is a member; a message left for the group by anyone is allowed.
func (c *call) vmGroupRefusal(g snapshot.VoicemailGroup, broadcast bool) (int, string) {
	switch {
	case !g.Enabled || len(g.Members) == 0:
		return sip.StatusNotFound, "voicemail group unavailable"
	case !c.s.vmGroupReady():
		return sip.StatusTemporarilyUnavailable, "voicemail unavailable"
	case broadcast && !g.IsMember(c.callerNum):
		return sip.StatusForbidden, "caller is not a member of the voicemail group"
	}
	return 0, ""
}

// startVoicemailGroup answers the call and records a message for group g. tx
// is the INVITE's transaction (nil when the call has already begun, as for a
// time condition's destination).
func (c *call) startVoicemailGroup(tx sip.ServerTransaction, g snapshot.VoicemailGroup, broadcast bool) {
	s := c.s
	if code, why := c.vmGroupRefusal(g, broadcast); code != 0 {
		s.m.VoicemailGroupDeliveries.WithLabelValues(VMGroupRefused).Inc()
		c.addTrace("Voicemail group refused: " + why)
		if tx != nil {
			s.respond(tx, c.inv, code, statusText(code))
			c.record(code, cdr.SideSystem, why, ResultFailed)
			return
		}
		c.hangupGroup(code, why)
		return
	}
	if tx != nil && !c.begin(c.inv, tx) {
		return
	}
	mode := vmGroupLeave
	if broadcast {
		mode = vmGroupBroadcast
	}
	c.mu.Lock()
	c.vmGroup = &g
	c.mu.Unlock()
	c.startVoicemail(mode, "group")
}

// ivrVoicemailGroupExit leaves a message for the group whose number is
// target, on a call an IVR menu or queue has already answered.
func (c *call) ivrVoicemailGroupExit(ctx context.Context, snap *snapshot.Snapshot, target string) (next string, done bool) {
	g, ok := snap.VoicemailGroup(target)
	relay := c.anchorRelay()
	if !ok || relay == nil {
		c.addTrace("IVR: voicemail group " + target + " is unavailable")
		return "", false
	}
	if code, why := c.vmGroupRefusal(g, false); code != 0 {
		c.s.m.VoicemailGroupDeliveries.WithLabelValues(VMGroupRefused).Inc()
		c.addTrace("IVR: voicemail group refused: " + why)
		return "", false
	}
	c.addTrace("IVR: to voicemail group " + target)
	c.mu.Lock()
	c.vmGroup = &g
	c.haPhase, c.haDetail = "", "" // a voicemail call after the menu is not replayed by a taker
	c.mu.Unlock()
	c.vmGroupLeave(ctx, &relaySession{c: c, relay: relay}, g, false)
	c.ivrHangup()
	return "", true
}

// vmGroupLeave runs the application for a group: the prompt, the tone, the
// recording and the delivery. broadcast changes only the prompt and adds a
// confirmation. The store work runs detached from the call context, so a
// caller who hangs up mid-recording still leaves the message recorded so far
// (a take under one second is discarded, as for a box).
func (c *call) vmGroupLeave(ctx context.Context, sess media.Session, g snapshot.VoicemailGroup, broadcast bool) {
	s := c.s
	c.playPreRoll(ctx, sess)
	lang := s.promptLang()
	if sess != nil && s.deps.Speech != nil {
		text := voiceprompt.Text(lang, voiceprompt.GroupLeave, voiceprompt.Vars{"group": g.Name})
		if broadcast {
			text = voiceprompt.Text(lang, voiceprompt.GroupBroadcast, nil)
		}
		c.speakOnce(ctx, sess, text)
	}
	if sess != nil {
		_ = sess.Play(ctx, media.BeepPCMRate(sess.SampleRate())) // the tone that says "now"
	}
	vm, objs := s.deps.Voicemails, s.deps.Objects
	if sess == nil || vm == nil || objs == nil {
		s.m.VoicemailGroupDeliveries.WithLabelValues(VMGroupFailed).Inc()
		return
	}
	max := time.Duration(g.MaxSeconds) * time.Second
	if max <= 0 {
		max = 180 * time.Second
	}
	audio, dur, ok := c.recordMessageMax(ctx, sess, max)
	live := ctx
	ctx = context.WithoutCancel(ctx)
	if !ok {
		s.m.VoicemailGroupDeliveries.WithLabelValues(VMGroupDiscarded).Inc()
		c.addTrace("Voicemail group message discarded (hangup or shorter than 1s)")
		return
	}
	boxes := make([]int64, 0, len(g.Members))
	for _, m := range g.Members {
		boxes = append(boxes, m.BoxID)
	}
	wav := media.EncodeWAV(audio, sess.SampleRate())
	key, err := media.ObjectKey(boxes[0], c.callID, time.Now())
	if err != nil {
		s.log.Warn("voicemail group object key refused", "error", err)
		s.m.VoicemailGroupDeliveries.WithLabelValues(VMGroupFailed).Inc()
		return
	}
	if !c.putAudio(ctx, key, wav) {
		s.m.VoicemailGroupDeliveries.WithLabelValues(VMGroupFailed).Inc()
		c.addTrace("Voicemail group storage failed after 3 attempts")
		return
	}
	idctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	defer cancel()
	delivered, err := vm.InsertGroupMessage(idctx, boxes, key, c.callerNum, c.callerName, dur.Milliseconds())
	if err != nil || len(delivered) == 0 {
		if err != nil {
			s.log.Warn("voicemail group message rows failed", "group_id", g.ID, "error", err)
		}
		c.removeObject(ctx, key) // no row references the audio
		s.m.VoicemailGroupDeliveries.WithLabelValues(VMGroupFailed).Inc()
		c.addTrace("Voicemail group metadata store failed")
		return
	}
	got := make(map[int64]bool, len(delivered))
	for _, b := range delivered {
		got[b] = true
	}
	for _, m := range g.Members {
		if got[m.BoxID] {
			c.sendMWI(m.Extension)
		}
	}
	if skipped := len(boxes) - len(delivered); skipped > 0 {
		// A member box was removed after the snapshot: the others keep theirs.
		s.log.Warn("voicemail group: some member boxes no longer exist", "group_id", g.ID, "skipped", skipped)
	}
	n := s.vmBytes.Add(int64(len(wav)))
	s.m.VoicemailStorage.Set(float64(n))
	s.m.VoicemailGroupDeliveries.WithLabelValues(VMGroupDelivered).Inc()
	c.addTrace(fmt.Sprintf("Voicemail group message delivered to %d of %d boxes (%d ms)", len(delivered), len(boxes), dur.Milliseconds()))
	if broadcast && live.Err() == nil && s.deps.Speech != nil {
		c.speakOnce(live, sess, voiceprompt.Text(lang, voiceprompt.GroupSent, nil))
	}
}
