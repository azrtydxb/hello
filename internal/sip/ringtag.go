package sip

import (
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/ringtag"
	"github.com/emiago/sipgo/sip"
)

// Ring tags (spec phone-feature-parity S-39 to S-41). Every INVITE Hello
// sends to an internal device carries one Alert-Info header naming the tag
// the call rings with; the provisioning maps the header to a tone per vendor.
// The choice reads the snapshot only.

// addRingTag chooses the ring tag of a leg to an internal device, records it
// in the trace and the CDR, and adds the canonical Alert-Info (S-41). The
// INVITE is built from scratch, so no received Alert-Info can reach the
// device. A trunk or SIP URI leg gets none. A page or intercom leg keeps the
// auto-answer header of its own (phonebooks-ptt P-16), which takes
// precedence; the tag is still chosen and recorded.
func (l *leg) addRingTag(req *sip.Request) {
	if l.trunk != nil || l.uri != nil {
		return
	}
	c, s := l.c, l.c.s
	snap := c.snapOrFail()
	if snap == nil {
		return
	}
	tbl := snap.RingTags()

	c.mu.Lock()
	in := ringtag.Input{
		Kind: c.kind, CallerNumber: c.callerNum, DID: c.dialled, CallerKind: ringtag.CallerExtension,
		At: time.Now(),
	}
	if c.doorCaller {
		in.CallerKind = ringtag.CallerDoor
	}
	if c.direction == cdr.DirectionInbound {
		in.FromTrunk, in.Trunk, in.CallerKind = true, c.trunkName, ringtag.CallerTrunk
	}
	group, auto := c.groupName, c.autoAnswer
	c.mu.Unlock()
	if group != "" {
		in.GroupTag = ringtag.RingGroup
		if g, _, ok := snap.RingGroup(group); ok && g.RingTag != "" {
			in.GroupTag = g.RingTag
		}
	}
	ch := tbl.Choose(in)

	c.mu.Lock()
	changed := c.ringTag != ch.Tag || c.ringWhy != ch.Why
	if changed {
		c.ringTag, c.ringWhy = ch.Tag, ch.Why
		c.trace.Add(fmt.Sprintf("Ring tag %s (%s)", ch.Tag, ch.Why))
	}
	c.mu.Unlock()
	if changed {
		s.m.RingTags.WithLabelValues(ch.Tag).Inc()
	}
	if l.pl != nil || auto {
		return
	}
	req.RemoveHeader("Alert-Info") // the INVITE is built from scratch; this keeps it true
	req.AppendHeader(sip.NewHeader("Alert-Info", tbl.Header(ch.Tag, l.binding.UserAgent)))
}
