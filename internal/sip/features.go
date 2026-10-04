// Feature codes (S-11) and the collaborators they need. Every database
// effect of a feature code is queued to a sink and applied off the SIP
// transaction path, like a CDR; the snapshot is the only thing the request
// goroutine reads.
package sip

import (
	"strings"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// Feature-code actions (contract 4).
const (
	ActionForwardAlways    = "forward_always"
	ActionForwardBusy      = "forward_busy"
	ActionForwardNoAnswer  = "forward_no_answer"
	ActionDNDOn            = "dnd_on"
	ActionDNDOff           = "dnd_off"
	ActionVoicemail        = "voicemail"
	ActionBlindTransfer    = "blind_transfer"
	ActionAttendedTransfer = "attended_transfer"
)

// ExtUpdate is one queued extension-feature change from a feature code.
// Pointer fields are optional: nil leaves the column alone.
type ExtUpdate struct {
	Extension       string
	DND             *bool
	ForwardAlways   *string
	ForwardBusy     *string
	ForwardNoAnswer *string
}

// SettingsSink takes extension-feature changes and applies them later: the
// implementation (hello-sip's config writer, wired in cmd) writes
// PostgreSQL, bumps the configuration revision and NOTIFYs, off the
// transaction path. It reports whether the change was accepted.
type SettingsSink interface {
	Apply(update ExtUpdate) bool
}

// matchFeatureCode finds the feature code a dialled string starts with, the
// rest being the argument (a phone dialling *72599 invokes *72 on 599).
// The longest match wins, so a code that is another's prefix still works.
func matchFeatureCode(snap *snapshot.Snapshot, dialled string) (snapshot.FeatureCode, string, bool) {
	if dialled == "" {
		return snapshot.FeatureCode{}, "", false
	}
	for _, c := range snap.Codes() {
		if strings.HasPrefix(dialled, c.Code) {
			return c, strings.TrimPrefix(dialled, c.Code), true
		}
	}
	return snapshot.FeatureCode{}, "", false
}

// handleFeatureInvite carries out a feature code dialled as a call (INVITE
// to the code, or the code plus its argument). Mutating codes queue their
// change to the settings sink and answer 200 (the phone's confirmation),
// keeping the dialog until the phone hangs up. ok=false means the dialled
// number is not a feature code.
func (c *call) handleFeatureInvite(tx sip.ServerTransaction, fc snapshot.FeatureCode, arg string) bool {
	s := c.s
	switch fc.Action {
	case ActionBlindTransfer, ActionAttendedTransfer:
		// *2 and ## work only inside a call (in-dialog DTMF, handleInfo).
		s.respond(tx, c.inv, sip.StatusForbidden, "Forbidden")
		c.record(sip.StatusForbidden, cdr.SideSystem, "transfer code dialled as a call", ResultFailed)
		return true
	case ActionVoicemail:
		if !c.begin(c.inv, tx) {
			return true
		}
		c.voicemailOwnBox(fc.Argument)
		return true
	}
	upd, ok := featureUpdate(fc, arg, c.callerNum)
	if !ok {
		s.respond(tx, c.inv, sip.StatusBadRequest, "Bad Extension")
		c.record(sip.StatusBadRequest, cdr.SideSystem, "feature code needs its argument", ResultFailed)
		return true
	}
	if s.deps.Settings == nil || !s.deps.Settings.Apply(upd) {
		s.respond(tx, c.inv, sip.StatusServiceUnavailable, "Service Unavailable")
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "configuration store unavailable", ResultUnavailable)
		return true
	}
	c.announceDNDChange(upd)
	if !c.begin(c.inv, tx) {
		return true
	}
	c.addTrace("Feature code " + fc.Code + " (" + fc.Action + ") applied")
	// Answered through the transaction (like the voicemail answer): the
	// phone hears the confirmation tone of a 200 and hangs up.
	c.answerSelf(c.inv.Body())
	return true
}

// featureUpdate builds the queued change for a mutating feature code. The
// code row's argument column is the mode: "set" takes the forwarding target
// from arg (dialled after the code, or collected by DTMF), "clear" removes
// the forwarding. ok=false when a set has no target to set.
func featureUpdate(fc snapshot.FeatureCode, arg, ext string) (ExtUpdate, bool) {
	upd := ExtUpdate{Extension: ext}
	target := func() *string {
		t := arg
		return &t
	}
	switch fc.Action {
	case ActionDNDOn:
		on := true
		upd.DND = &on
	case ActionDNDOff:
		off := false
		upd.DND = &off
	case ActionForwardAlways:
		if fc.Argument == "clear" {
			empty := ""
			upd.ForwardAlways = &empty
			return upd, true
		}
		if arg == "" {
			return upd, false
		}
		upd.ForwardAlways = target()
	case ActionForwardBusy:
		if fc.Argument == "clear" {
			empty := ""
			upd.ForwardBusy = &empty
			return upd, true
		}
		if arg == "" {
			return upd, false
		}
		upd.ForwardBusy = target()
	case ActionForwardNoAnswer:
		if fc.Argument == "clear" {
			empty := ""
			upd.ForwardNoAnswer = &empty
			return upd, true
		}
		if arg == "" {
			return upd, false
		}
		upd.ForwardNoAnswer = target()
	default:
		return upd, false
	}
	return upd, true
}

// handleInfo relays nothing: an INFO with a DTMF digit (application/
// dtmf-relay) feeds the call's feature-code collection; anything else is
// answered 200 and ignored.
func (s *Server) handleInfo(req *sip.Request, tx sip.ServerTransaction) {
	ref, ok := s.matchDialog(req)
	if !ok {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	if h := req.ContentType(); h != nil && strings.Contains(h.Value(), "dtmf-relay") {
		if d, ok := dtmfDigit(req.Body()); ok {
			s.dispatchDigit(ref.c, ref.leg == nil, d)
		}
	}
	s.respond(tx, req, sip.StatusOK, "OK")
}

// handleMessage answers an inbound MESSAGE 200; the MWI MESSAGEs are
// outbound only.
func (s *Server) handleMessage(req *sip.Request, tx sip.ServerTransaction) {
	s.respond(tx, req, sip.StatusOK, "OK")
}

// dtmfDigit reads a digit from a dtmf-relay body ("Signal=2", "Signal: #").
func dtmfDigit(body []byte) (byte, bool) {
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(strings.ToLower(line), "signal") {
			continue
		}
		rest := strings.TrimLeft(strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(line), "signal"), ":"), " =")
		if rest != "" {
			return rest[0], true
		}
	}
	return 0, false
}

// dispatchDigit collects the digit into the call's buffer and dispatches a
// feature code when the buffer completes one: a code that needs no argument
// fires at once, one that does (a forward set, a transfer) collects digits
// until '#'. The digits act for the extension of the phone that dialled
// them.
func (s *Server) dispatchDigit(c *call, fromCaller bool, d byte) {
	buf := s.bufferFor(c)
	all := buf.push([]byte{d})
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	if d == '#' {
		// The terminator dispatches the collected code and argument.
		buf.take()
		fc, arg, ok := matchFeatureCode(snap, string(all[:len(all)-1]))
		if ok {
			s.applyInDialogCode(c, fromCaller, fc, arg)
		}
		return
	}
	fc, arg, ok := matchFeatureCode(snap, string(all))
	if !ok {
		return
	}
	// A code whose action carries no argument fires immediately; otherwise
	// keep collecting (the trailing digits are the argument).
	switch fc.Action {
	case ActionDNDOn, ActionDNDOff, ActionVoicemail:
		buf.take()
	case ActionForwardAlways, ActionForwardBusy, ActionForwardNoAnswer:
		if fc.Argument == "clear" {
			buf.take()
			break
		}
		return // the target digits follow, ended by '#'
	case ActionBlindTransfer, ActionAttendedTransfer:
		return // the target digits follow, ended by '#'
	default:
		buf.take()
	}
	s.applyInDialogCode(c, fromCaller, fc, arg)
}

// bufferFor is the call's DTMF collection buffer, created on demand and
// dropped when the call ends.
func (s *Server) bufferFor(c *call) *digitBuffer {
	s.digitsMu.Lock()
	defer s.digitsMu.Unlock()
	if b, ok := s.digits[c]; ok {
		return b
	}
	b := &digitBuffer{}
	s.digits[c] = b
	return b
}

func (s *Server) dropBuffer(c *call) {
	s.digitsMu.Lock()
	delete(s.digits, c)
	s.digitsMu.Unlock()
}

// applyInDialogCode runs a completed in-dialog feature code: DND and
// forwarding queue to the settings sink; the transfer codes transfer the
// call (the side whose phone dialled the code is the transferee).
func (s *Server) applyInDialogCode(c *call, fromCaller bool, fc snapshot.FeatureCode, arg string) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	ext := c.dialled
	if fromCaller {
		ext = c.callerNum
	}
	switch fc.Action {
	case ActionBlindTransfer:
		if arg != "" {
			// The phone that dialled the code is the transferee: its side
			// gets the NOTIFYs (the caller's dialog when it dialled).
			if fromCaller {
				c.transferBlindVia(ext, arg, TransferBlind, nil)
			} else {
				c.transferBlind(ext, arg, TransferBlind)
			}
		}
		return
	case ActionAttendedTransfer:
		if arg != "" {
			// DTMF attended transfer without a second call: the transferee
			// legs drop and the two parties bridge, the same as a REFER
			// attended transfer whose second call was never answered.
			if fromCaller {
				c.transferBlindVia(ext, arg, TransferAttended, nil)
			} else {
				c.transferBlind(ext, arg, TransferAttended)
			}
		}
		return
	case ActionVoicemail:
		return // dialling *97 mid-call does nothing
	}
	upd, ok := featureUpdate(fc, arg, ext)
	if !ok || s.deps.Settings == nil || !s.deps.Settings.Apply(upd) {
		return
	}
	c.addTrace("Feature code " + fc.Code + " (" + fc.Action + ") applied in call")
}

// announceDNDChange publishes the new DND state for the extension's devices
// right away, so BLF lamps follow the phone's feature code before the next
// snapshot reload.
func (c *call) announceDNDChange(upd ExtUpdate) {
	if upd.DND == nil {
		return
	}
	snap := c.s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	state := StateIdle
	if *upd.DND {
		state = StateDND
	}
	c.s.publishDeviceState(c.s.devicesOf(snap, upd.Extension), upd.Extension, state)
}
