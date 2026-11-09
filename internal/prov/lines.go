package prov

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
//   - Grandstream, from the vendor's own configuration templates
//     (wp8x6_config_1.0.3.39, wp810_822_825_config_1.0.11.83,
//     wp820_config_1.0.7.90, wp856_config_1.0.3.16): the WP8x6 three accounts
//     (the template labels account 3 "GRP2602/2603/2604 only", an artefact of
//     a template shared with that family: its account sharing and default
//     account choices list Account 3, and the phone-feature-parity spec gives
//     the WP8x6 three; to confirm on a handset), the WP810, WP820, WP822 and
//     WP825 two, the WP856 six. Every other Grandstream model keeps one.
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
			return 2
		case FamilyWP856:
			return 6
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
// of lines 2 and up for the vendor and model: every vendor with a built-in
// template that has a model taking more than one line. The Grandstream WP
// templates write them from the account blocks of Grandstream's own
// templates (account 2 from P401, account 3 from P501, accounts 4 to 6 of the
// WP856 from P601, P1701 and P1801). A Grandstream model that takes one line
// has no account block to write.
func SupportsLineKeys(v Vendor, model string) bool {
	switch v {
	case Yealink, Poly, Snom, Fanvil, Grandstream:
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
	// Password is the group's opened account sharing password (P22523); masked
	// in previews. Empty writes nothing, so a phone keeps what it has.
	Password string
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
