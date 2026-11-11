// Package secmedia holds the SRTP policy vocabulary shared by the control
// plane (validation, storage, provisioning) and hello-sip (negotiation). It
// has no dependencies so every service can import it.
package secmedia

import "strings"

// Policy is a device's or trunk's media-encryption policy (phone-feature-
// parity S-15). It only ever applies to a leg that runs over TLS: SDES keys
// travel in the SDP, so offering them over an unprotected signalling leg
// would hand the key to anyone on the path.
type Policy string

const (
	// Off never offers or accepts crypto: the leg is plain RTP.
	Off Policy = "off"
	// Optional offers SDES crypto on a TLS leg and accepts it when the far
	// end answers with it; a far end without it gets plain RTP.
	Optional Policy = "optional"
	// Required offers SRTP only. A far end that does not do SRTP, or a leg
	// that is not on TLS, refuses the call (488 to the offerer).
	Required Policy = "required"
)

// Parse maps a stored or submitted value to a Policy; the empty string is
// the default (Optional for a device, Off for a trunk: see Default).
func Parse(s string) (Policy, bool) {
	switch p := Policy(strings.ToLower(strings.TrimSpace(s))); p {
	case Off, Optional, Required:
		return p, true
	}
	return "", false
}

// Valid reports whether s is one of the three policies.
func Valid(s string) bool { _, ok := Parse(s); return ok }

// Normalize returns the policy for a stored value, def for anything unknown
// (a row written before the column existed).
func Normalize(s string, def Policy) Policy {
	if p, ok := Parse(s); ok {
		return p
	}
	return def
}

// Values lists the policies in order of strength.
func Values() []string { return []string{string(Off), string(Optional), string(Required)} }
