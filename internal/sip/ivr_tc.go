package sip

import (
	"context"

	"github.com/azrtydxb/hello/internal/snapshot"
)

// ivrTimeCondition decides the time condition an IVR exit names and leaves
// through the destination it picks. next and done follow ivrLeave's meaning.
// The menu is bounded by ivrMaxEntries, so the chain starts afresh each time.
func (c *call) ivrTimeCondition(ctx context.Context, snap *snapshot.Snapshot, m snapshot.IVRMenu, ex ivrExit) (next string, done bool) {
	res, err := c.tcDecide(snap, ex.dest.Target, nil)
	if err != nil {
		return "", false
	}
	d := res.Dest
	if d.Kind == snapshot.IVRAnnouncement && d.ThenVoicemail != "" {
		c.ivrAnnounce(ctx, snap, d.Target)
		return c.ivrVoicemailExit(ctx, snap, d.ThenVoicemail, "time-condition")
	}
	ex.dest = snapshot.IVRDest{Kind: d.Kind, Target: d.Target}
	return c.ivrLeave(ctx, snap, m, ex)
}
