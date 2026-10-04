// Media anchoring decision (plan contract 2) and the call-side anchoring
// integration: a call anchors when a feature needs the anchor, and stays
// anchored to its end once anchored (no churn, spec S-1).
package sip

import (
	"strings"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// AnchorReason is why a call's media anchors (or not). The zero value is
// direct media; every other value is a trigger.
type AnchorReason string

// The anchoring triggers (contract 2). Precedence follows the contract's
// order: NAT before recording before announcement before voicemail; the
// force switch answers only when nothing else did.
const (
	AnchorNone         AnchorReason = ""
	AnchorNAT          AnchorReason = "nat"
	AnchorRecording    AnchorReason = "recording"
	AnchorAnnouncement AnchorReason = "announcement"
	AnchorVoicemail    AnchorReason = "voicemail"
	AnchorForced       AnchorReason = "forced"
)

// EndpointInfo is one side's addressing as the anchoring decision sees it.
type EndpointInfo struct {
	// ContactHost/ContactPort name what the endpoint claims (Contact
	// header, or the binding's ContactURI); SourceHost/SourcePort are
	// where its packets really come from.
	ContactHost string
	ContactPort int
	SourceHost  string
	SourcePort  int
	// NATKnown is a Phase 3 NAT flag: the registration path already knows
	// the endpoint is behind NAT.
	NATKnown bool
	// Extension is the extension number on this side (empty for a trunk or
	// URI destination); it selects record_default.
	Extension string
}

// nat reports the endpoint behind NAT: contact host:port differs from the
// packet source (spec S-1a), or the registration says so.
func (e EndpointInfo) nat() bool {
	if e.ContactHost == "" || e.SourceHost == "" {
		return false
	}
	if !strings.EqualFold(e.ContactHost, e.SourceHost) {
		return true
	}
	return e.ContactPort != 0 && e.SourcePort != 0 && e.ContactPort != e.SourcePort
}

// decideAnchor reports why a call anchors, or AnchorNone for direct media
// (contract 2). snap may be nil. Exported for tests.
func decideAnchor(req *sip.Request, snap *snapshot.Snapshot, from, to EndpointInfo, force bool) AnchorReason {
	if from.nat() || to.nat() {
		return AnchorNAT
	}
	if req != nil && snap != nil {
		if recordingWanted(snap, from, to) {
			return AnchorRecording
		}
		if announcementWanted(snap, req) {
			return AnchorAnnouncement
		}
		if voicemailWanted(snap, req) {
			return AnchorVoicemail
		}
	}
	if force {
		return AnchorForced
	}
	return AnchorNone
}

// recordingWanted reports whether the call is recorded from the start:
// either endpoint's record_default (spec S-4; a direct call with
// record_default anchors at setup, so recording always works).
func recordingWanted(snap *snapshot.Snapshot, from, to EndpointInfo) bool {
	for _, num := range []string{from.Extension, to.Extension} {
		if e, ok := snap.Extension(num); ok && e.RecordDefault {
			return true
		}
	}
	return false
}

// announcementWanted reports whether the call's route plays an
// announcement: an announcement feature code dialled as a call (its
// argument names the announcement), or a ring group's announcement
// failure destination.
func announcementWanted(snap *snapshot.Snapshot, req *sip.Request) bool {
	dialled := req.Recipient.User
	if fc, ok := snap.FeatureCode(dialled); ok && fc.Action == ActionAnnouncement {
		return true
	}
	if _, _, ok := snap.RingGroup(dialled); ok {
		if g, _, _ := snap.RingGroup(dialled); g.FailureKind == "announcement" {
			return true
		}
	}
	return false
}

// voicemailWanted reports whether the dialled extension hands the call to
// voicemail without ringing (DND with a box, or nothing registered with a
// box): the anchor is needed from the start (spec S-1c).
func voicemailWanted(snap *snapshot.Snapshot, req *sip.Request) bool {
	e, ok := snap.Extension(req.Recipient.User)
	if !ok {
		return false
	}
	if e.DND && e.VoicemailEnabled && e.VoicemailBoxID > 0 {
		return true
	}
	return false
}
