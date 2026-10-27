package sip

import (
	"fmt"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// Door stations (spec phone-feature-parity S-45 and S-46). A door extension
// registers like any extension, but its calls are confined to its targets: a
// compromised door unit must not be able to dial out on a trunk.

// doorCall serves an INVITE from a door station. The call has kind door, so
// it rings with the doorbell tag (S-40 rule 1) and its CDR says door. Only
// the station's own targets and the Door ring groups are reachable; anything
// else, a feature code, page, queue or outside number included, is 403. The
// destination goes straight to the ring group or extension and never through
// routing, so no route can send it out.
func (c *call) doorCall(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot) {
	c.mu.Lock()
	c.doorCaller, c.kind = true, cdr.KindDoor
	c.mu.Unlock()
	if !snap.DoorMayCall(c.callerNum, c.dialled) {
		c.addTrace(fmt.Sprintf("Door station %s may not call %s", c.callerNum, c.dialled))
		c.s.respond(tx, req, sip.StatusForbidden, statusText(sip.StatusForbidden))
		c.record(sip.StatusForbidden, cdr.SideSystem, "door station may not call "+c.dialled, ResultFailed)
		return
	}
	c.addTrace(fmt.Sprintf("Door station %s calls %s", c.callerNum, c.dialled))
	c.ringTarget(req, tx, snap, c.dialled)
}

// noteDoorGroup gives a call that reaches a Door ring group the kind door.
func (c *call) noteDoorGroup(g snapshot.RingGroup) {
	if !g.IsDoor {
		return
	}
	c.mu.Lock()
	if c.kind == "" || c.kind == cdr.KindCall {
		c.kind = cdr.KindDoor
	}
	c.mu.Unlock()
}

// doorBlocksTrunk reports whether the call came from a door station and so
// may not use a trunk; an emergency route is never blocked (S-45).
func (c *call) doorBlocksTrunk(emergency bool) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.doorCaller && !emergency
}
