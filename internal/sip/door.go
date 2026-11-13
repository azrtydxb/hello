package sip

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/media"
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

// doorOpenKeys is what the answering user dials during a door call to open
// the door (spec S-47).
const doorOpenKeys = "*50"

// Outcomes of an open-door request, as SIP-style codes the API maps.
const (
	doorOpenOK       = 0
	doorOpenConflict = 409
)

// doorStation finds the door station on the call and the relay leg that
// reaches it: the caller when the station called, the callee when a user
// dialled the station's number. ok is false for a call without a station.
func (c *call) doorStation(snap *snapshot.Snapshot) (ext, doorLeg string, ok bool) {
	c.mu.Lock()
	caller := c.doorCaller
	c.mu.Unlock()
	switch {
	case caller:
		return c.callerNum, legCaller, true
	case snap != nil && snap.IsDoor(c.dialled):
		return c.dialled, legCallee, true
	}
	return "", "", false
}

// answerer is the extension on the far side of the door station: the one
// whose device answered a station's call, or the user who dialled it.
func (c *call) answerer(doorLeg string) string {
	if doorLeg == legCallee {
		return c.callerNum
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.winner != nil {
		return c.winner.binding.Extension
	}
	return ""
}

// doorOpenPressed handles the digits the users dialled: when they end in *50
// on a connected door call, from the user's side, the door station's stored
// code goes to the door leg. It reports whether the buffer was the door-open
// code, so the caller drops it. On any other call it does nothing: the digits
// were already relayed as ordinary DTMF.
func (c *call) doorOpenPressed(all []byte, fromCaller bool) bool {
	if !bytes.HasSuffix(all, []byte(doorOpenKeys)) {
		return false
	}
	snap := c.s.deps.Snapshots.Current()
	_, doorLeg, ok := c.doorStation(snap)
	if !ok {
		return false
	}
	if fromCaller == (doorLeg == legCaller) {
		return true // the station's own keys never open its door
	}
	if code, why := c.openDoor(snap, "key"); code != doorOpenOK {
		c.addTrace("Door open refused: " + why)
	}
	return true
}

// openDoorBy is the API's request: the extension ext asks to open the door of
// this call. Only the extension on the far side of the door station may.
func (c *call) openDoorBy(ext string) (int, string) {
	snap := c.s.deps.Snapshots.Current()
	_, doorLeg, ok := c.doorStation(snap)
	if !ok {
		return doorOpenConflict, "the call has no door station"
	}
	if ext == "" || c.answerer(doorLeg) != ext {
		return 403, "only the user who answered the door call may open the door"
	}
	return c.openDoor(snap, "api")
}

// openDoor sends the station's door-open code to its leg as RFC 2833 events
// and, when the station is set to, ends the call a few seconds later. The
// digits are never written to the trace, a log or an error. via says what
// asked (key or api).
func (c *call) openDoor(snap *snapshot.Snapshot, via string) (int, string) {
	ext, doorLeg, ok := c.doorStation(snap)
	if !ok {
		return doorOpenConflict, "the call has no door station"
	}
	c.mu.Lock()
	connected, busy := c.connected, c.doorOpening
	if connected && !busy {
		c.doorOpening = true
	}
	c.mu.Unlock()
	switch {
	case !connected:
		return doorOpenConflict, "the call is not answered"
	case busy:
		return doorOpenConflict, "the door is already being opened"
	}
	code, hangup, ok := snap.DoorOpen(ext)
	relay := c.anchorRelay()
	if !ok || relay == nil {
		c.mu.Lock()
		c.doorOpening = false
		c.mu.Unlock()
		if !ok {
			return doorOpenConflict, "the door station has no door-open code"
		}
		return doorOpenConflict, "the call's media is not anchored"
	}
	c.addTrace("Door open pressed (" + via + ")")
	c.s.goBG(func() {
		defer contain(c.s.log, "door open")
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err := relay.SendDTMF(ctx, doorLeg, code)
		c.mu.Lock()
		c.doorOpening = false
		c.mu.Unlock()
		switch {
		case errors.Is(err, media.ErrNoTelephoneEvent):
			c.addTrace("Door open failed: the door station negotiated no telephone-event")
			return
		case err != nil:
			c.addTrace("Door open failed: the code could not be sent")
			return
		}
		c.addTrace("Door open code sent")
		if hangup {
			time.AfterFunc(c.s.cfg.DoorHangup, func() {
				defer contain(c.s.log, "door open hangup")
				c.endBoth("door opened")
			})
		}
	})
	return doorOpenOK, ""
}
