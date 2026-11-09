package prov

import "regexp"

var wp822RE = regexp.MustCompile(`^WP822`)

// Multiple lines per phone and account sharing (phone-feature-parity S-10,
// S-11).
//
// A phone's line 1 is the device phones.device_id binds; lines 2 and up are
// further devices (phone_lines), each of its own extension, so a phone with
// two lines registers two SIP accounts. Several phones may each hold a line
// of the same extension (a shared line appearance): every phone binds its
// own device of that extension, and hello-sip forks a call to all of them.

// maxLinesCap bounds the lines of a phone whose vendor has account blocks
// Hello writes (Yealink, Poly, Snom, Fanvil): a bound a model's own limit
// can only lower. The vendors' desk phones ship with up to this many
// accounts in their entry and mid range; a model with more is simply held to
// this number until the table learns it.
const maxLinesCap = 6

// MaxLines is the number of lines (SIP accounts) the model takes, 1 when it
// takes no more than the first.
//
//   - Grandstream WP8x6 has three accounts and the WP822 two (phone-feature-
//     parity S-10); every other Grandstream model keeps one, because no
//     published template Hello holds gives its further account keys.
//   - Yealink and Snom models in the key table (keycaps.go) take as many
//     lines as they have line keys, up to maxLinesCap; a model outside the
//     table, and Poly and Fanvil, take maxLinesCap (their line count is not
//     in a table Hello holds, and a phone ignores an account it has no room
//     for).
//   - Generic has no built-in template and keeps one.
func MaxLines(v Vendor, model string) int {
	switch v {
	case Grandstream:
		switch GrandstreamFamily(model) {
		case FamilyWP8x6:
			return 3
		case FamilyWP8xx:
			if wp822RE.MatchString(normModel(model)) {
				return 2
			}
		}
		return 1
	case Yealink, Snom:
		m := keyModelName(v, model)
		var km keyModel
		var ok bool
		if v == Yealink {
			km, ok = yealinkKeys[yealinkFamilyRE.FindString(m)]
		} else {
			km, ok = snomKeys[snomFamilyRE.FindString(m)]
		}
		if ok {
			return max(1, min(km.keys, maxLinesCap))
		}
		return maxLinesCap
	case Poly, Fanvil:
		return maxLinesCap
	}
	return 1
}

// SupportsLineKeys reports whether the built-in template writes the account
// of lines 2 and up for the vendor and model. It is false for Grandstream:
// the account block of a further account (account 2 from P401, account 3
// from P501) is named by the spec, but no template in hand gives the keys of
// its server, user, authentication and name settings, and Hello writes only
// keys a vendor document or an existing template confirms. A Grandstream
// phone may still hold the lines in Hello; they reach the phone when the
// keys are confirmed in the lab (decisions, "Multiple lines").
func SupportsLineKeys(v Vendor, model string) bool {
	switch v {
	case Yealink, Poly, Snom, Fanvil:
		return MaxLines(v, model) > 1
	}
	return false
}

// SharingInfo is the account-sharing role of a Grandstream WP8x6 whose phone
// group enabled account sharing (S-11): the host holds the shared line, a
// guest shares it. It is nil in the render data of any other phone.
type SharingInfo struct {
	Host  bool
	Guest bool
}

// Sharing roles of a phone in a phone group with account sharing.
const (
	SharingHost  = "host"
	SharingGuest = "guest"
)

// ValidSharingRole reports whether r is a sharing role.
func ValidSharingRole(r string) bool { return r == SharingHost || r == SharingGuest }

// NewSharing returns the render data for a phone with the given role in a
// group that enabled account sharing; nil for a model without the setting
// (only the WP8x6 has it, P22521 and P22522) or a group that did not enable
// it.
func NewSharing(v Vendor, model string, groupEnabled bool, role string) *SharingInfo {
	if !groupEnabled || v != Grandstream || GrandstreamFamily(model) != FamilyWP8x6 {
		return nil
	}
	return &SharingInfo{Host: role == SharingHost, Guest: role == SharingGuest}
}
