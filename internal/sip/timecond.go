// Time conditions (migration 00038). A time condition is a destination that
// picks one of three destinations from a schedule (open, closed, holiday)
// or a manual override, then hands the call to it. The decision and the
// reason are written to the call's trace (the CDR) and the INFO log, and
// counted in hello_time_condition_total{result}.
package sip

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/timecond"
	"github.com/emiago/sipgo/sip"
)

// ActionTimeCondition routes a dialled feature code to a time condition
// (the argument names it); ActionTCOverride is the day/night toggle *28<n>:
// the argument is "toggle", "open", "closed" or "auto" and the digits
// dialled after the code select the time condition by its toggle number.
const (
	ActionTimeCondition = "time_condition"
	ActionTCOverride    = "tc_override"
)

// Results counted in hello_time_condition_total.
const (
	TCOpen         = "open"
	TCClosed       = "closed"
	TCHoliday      = "holiday"
	TCForcedOpen   = "forced_open"
	TCForcedClosed = "forced_closed"
	TCLoop         = "loop"
	TCMissing      = "missing"
	TCToggled      = "toggled"
)

// TimeConditionSettings changes a time condition's manual override from a
// feature code. The implementation (cmd/hello-sip) writes PostgreSQL through
// the store, so the override is audited, bumps the configuration revision
// and reaches every node as a snapshot.
type TimeConditionSettings interface {
	// Toggle applies mode ("toggle", "auto", "open" or "closed") to the
	// condition with that toggle number and returns its name and the
	// override now in force.
	Toggle(ctx context.Context, actor, number, mode string, now time.Time) (name, override string, err error)
}

// statusDecline is 603: the time condition hangs the call up on purpose.
const statusDecline = 603

// tcResult maps a step to its metric label.
func tcResult(st timecond.Step) string {
	switch {
	case st.Forced && st.Branch == timecond.BranchOpen:
		return TCForcedOpen
	case st.Forced:
		return TCForcedClosed
	}
	return st.Branch
}

// routeTimeCondition decides the time condition name and carries out the
// destination it picks. tx is the INVITE's server transaction while the call
// has not begun; nil when it already began (a ring group's failure
// destination, the call is still ringing for the caller).
func (c *call) routeTimeCondition(tx sip.ServerTransaction, snap *snapshot.Snapshot, name string) {
	c.mu.Lock()
	visited := slices.Clone(c.tcVisited)
	c.mu.Unlock()
	res, err := c.tcDecide(snap, name, visited)
	c.mu.Lock()
	for _, st := range res.Steps {
		c.tcVisited = append(c.tcVisited, st.Condition)
	}
	c.mu.Unlock()
	if err != nil {
		status, reason := sip.StatusLoopDetected, "time conditions lead back to each other"
		if errors.Is(err, timecond.ErrMissing) {
			status, reason = sip.StatusNotFound, "time condition does not exist"
		}
		c.tcReject(tx, status, reason)
		return
	}
	c.applyTCDest(tx, snap, res.Dest)
}

// tcDecide follows the time condition name (and the ones it names) to a
// destination at the current time, leaving the decision of each step in the
// call's trace, the INFO log and the metric.
func (c *call) tcDecide(snap *snapshot.Snapshot, name string, visited []string) (timecond.Resolution, error) {
	res, err := timecond.Resolve(snap.TimeConditionLookup, name, time.Now(), visited...)
	for _, st := range res.Steps {
		r := tcResult(st)
		c.s.m.TimeConditionTotal.WithLabelValues(r).Inc()
		c.addTrace(fmt.Sprintf("Time condition %s: %s (%s) -> %s", st.Condition, st.Branch, st.Why, st.Dest))
		c.s.log.Info("time condition decided", "component", "sip", "time_condition", st.Condition, "branch", st.Branch,
			"result", r, "why", st.Why, "destination", st.Dest.String(), "forced", st.Forced,
			"correlation_id", c.id, "call_id", c.callID, "caller", c.callerNum)
	}
	if err != nil {
		result := TCLoop
		if errors.Is(err, timecond.ErrMissing) {
			result = TCMissing
		}
		c.s.m.TimeConditionTotal.WithLabelValues(result).Inc()
		c.addTrace("Time condition " + name + ": " + err.Error())
		c.s.log.Warn("time condition failed", "component", "sip", "time_condition", name, "result", result,
			"error", err, "correlation_id", c.id, "call_id", c.callID)
	}
	return res, err
}

// tcReject answers the caller with a final failure.
func (c *call) tcReject(tx sip.ServerTransaction, code int, why string) {
	if tx != nil {
		c.s.respond(tx, c.inv, code, statusText(code))
		c.record(code, cdr.SideSystem, why, ResultFailed)
		return
	}
	c.hangupGroup(code, why)
}

// applyTCDest hands the call to the destination a time condition picked.
func (c *call) applyTCDest(tx sip.ServerTransaction, snap *snapshot.Snapshot, d timecond.Dest) {
	s := c.s
	begin := func() bool { return tx == nil || c.begin(c.inv, tx) }
	switch d.Kind {
	case timecond.KindExtension:
		c.ringExtension(c.inv, tx, snap, d.Target, nil)
	case timecond.KindRingGroup:
		g, members, ok := snap.RingGroup(d.Target)
		if !ok {
			c.addTrace("Time condition: ring group " + d.Target + " does not exist")
			c.tcReject(tx, sip.StatusNotFound, "ring group "+d.Target+" does not exist")
			return
		}
		c.ringGroup(c.inv, tx, snap, g, members)
	case timecond.KindVoicemail:
		e, ok := snap.Extension(d.Target)
		if !ok || !c.voicemailConfigured(e) {
			c.addTrace("Time condition: no voicemail box for extension " + d.Target)
			c.tcReject(tx, sip.StatusTemporarilyUnavailable, "voicemail box unavailable")
			return
		}
		if !begin() {
			return
		}
		c.mu.Lock()
		c.vmExt = d.Target
		c.mu.Unlock()
		c.startVoicemail(vmLeave, "time-condition")
	case timecond.KindAnnouncement:
		if d.ThenVoicemail != "" {
			e, ok := snap.Extension(d.ThenVoicemail)
			if ok && c.voicemailConfigured(e) {
				if !begin() {
					return
				}
				c.mu.Lock()
				c.vmExt, c.vmPre = d.ThenVoicemail, d.Target
				c.mu.Unlock()
				c.startVoicemail(vmLeave, "time-condition")
				return
			}
			c.addTrace("Time condition: no voicemail box for extension " + d.ThenVoicemail + "; the announcement ends the call")
		}
		if !begin() {
			return
		}
		c.announcementDestination(d.Target)
	case timecond.KindPaging:
		g, ok := snap.PagingGroup(d.Target)
		if !ok || tx == nil {
			c.addTrace("Time condition: paging group " + d.Target + " cannot take the call here")
			c.tcReject(tx, sip.StatusNotFound, "paging group unavailable")
			return
		}
		c.handlePage(tx, snap, g)
	case timecond.KindIVR:
		m, ok := snap.IVRMenu(d.Target)
		if !ok {
			c.addTrace("Time condition: IVR menu " + d.Target + " does not exist")
			c.tcReject(tx, sip.StatusNotFound, "IVR menu "+d.Target+" does not exist")
			return
		}
		c.startIVR(tx, snap, m)
	case timecond.KindExternal:
		c.forward(c.inv, tx, snap, d.Target, nil)
	default: // hangup, or a kind this node does not know
		c.addTrace("Time condition: hang up")
		if tx != nil {
			s.respond(tx, c.inv, statusDecline, "Decline")
			c.record(statusDecline, cdr.SideSystem, "time condition hangs up", ResultFailed)
			return
		}
		c.hangupGroup(statusDecline, "time condition hangs up")
	}
}

// playPreRoll plays the announcement that precedes a voicemail message
// (a time condition's "announcement, then voicemail").
func (c *call) playPreRoll(ctx context.Context, sess media.Session) {
	c.mu.Lock()
	name := c.vmPre
	c.vmPre = ""
	c.mu.Unlock()
	if name == "" || sess == nil {
		return
	}
	snap := c.s.deps.Snapshots.Current()
	objs := c.s.deps.Objects
	if snap == nil || objs == nil {
		return
	}
	obj, ok := snap.Announcement(name)
	if !ok {
		c.addTrace(fmt.Sprintf("Announcement %q missing: skipped", name))
		return
	}
	octx, cancel := context.WithTimeout(ctx, c.s.cfg.StateTimeout)
	raw, err := objs.GetAnnouncement(octx, obj)
	cancel()
	if err != nil {
		c.addTrace(fmt.Sprintf("Announcement %q unavailable: skipped", name))
		return
	}
	pcm, err := media.WAVToRate(raw, sess.SampleRate())
	if err != nil {
		c.addTrace(fmt.Sprintf("Announcement %q is not a WAV: skipped", name))
		return
	}
	_ = sess.Play(ctx, pcm)
	c.addTrace(fmt.Sprintf("Announcement %q played, then voicemail", name))
}

// handleTCOverride is the day/night feature code *28<n>: the digits after
// the code pick the time condition by its toggle number. The phone hears the
// confirmation tone of a 200, like the other mutating codes.
func (c *call) handleTCOverride(tx sip.ServerTransaction, fc snapshot.FeatureCode, arg string) {
	s := c.s
	mode := fc.Argument
	if mode == "" {
		mode = "toggle"
	}
	fail := func(code int, why string) {
		s.respond(tx, c.inv, code, statusText(code))
		c.record(code, cdr.SideSystem, why, ResultFailed)
	}
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		fail(sip.StatusServiceUnavailable, "no configuration")
		return
	}
	if _, ok := snap.TimeConditionByToggle(arg); !ok {
		c.s.m.TimeConditionTotal.WithLabelValues(TCMissing).Inc()
		fail(sip.StatusNotFound, "no time condition has toggle number "+arg)
		return
	}
	if s.deps.TimeConditions == nil {
		fail(sip.StatusServiceUnavailable, "configuration store unavailable")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	name, now, err := s.deps.TimeConditions.Toggle(ctx, "ext:"+c.callerNum, arg, mode, time.Now())
	if err != nil {
		s.log.Warn("time condition toggle failed", "component", "sip", "number", arg, "error", err)
		fail(sip.StatusServiceUnavailable, "configuration store unavailable")
		return
	}
	if !c.begin(c.inv, tx) {
		return
	}
	s.m.TimeConditionTotal.WithLabelValues(TCToggled).Inc()
	c.addTrace(fmt.Sprintf("Time condition %s: override set to %s by feature code %s%s", name, now, fc.Code, arg))
	s.log.Info("time condition override changed", "component", "sip", "time_condition", name, "override", now,
		"by", c.callerNum, "correlation_id", c.id, "call_id", c.callID)
	c.answerSelf(c.inv.Body())
}

// --- BLF on the toggle code -------------------------------------------------

// tcToggleCode reports whether user is a toggle feature code plus a toggle
// number that names a time condition, e.g. "*281".
func tcToggleCode(snap *snapshot.Snapshot, user string) (snapshot.TimeCondition, bool) {
	fc, arg, ok := matchFeatureCode(snap, user)
	if !ok || fc.Action != ActionTCOverride {
		return snapshot.TimeCondition{}, false
	}
	return snap.TimeConditionByToggle(arg)
}

// tcLampState is the dialog state shown on the toggle key: lit (confirmed)
// while a manual override is in force, off (terminated) while the schedule
// decides.
func tcLampState(t snapshot.TimeCondition) string {
	if t.Override == timecond.OverrideOpen || t.Override == timecond.OverrideClosed {
		return StateOnCall
	}
	return StateIdle
}

// tcSnapshotChanged notifies the BLF subscribers of toggle codes whose
// override changed between two snapshots (a change from the UI, the API or
// another node).
func (s *Server) tcSnapshotChanged(old, cur *snapshot.Snapshot) {
	for _, t := range cur.TimeConditions() {
		if t.ToggleNumber == "" {
			continue
		}
		before, ok := old.TimeConditionByToggle(t.ToggleNumber)
		if ok && tcLampState(before) == tcLampState(t) {
			continue
		}
		for _, fc := range cur.Codes() {
			if fc.Action != ActionTCOverride {
				continue
			}
			code := fc.Code + t.ToggleNumber
			state := tcLampState(t)
			s.goBG(func() {
				defer contain(s.log, "time condition presence")
				s.notifyExtension(code, state)
			})
		}
	}
}
