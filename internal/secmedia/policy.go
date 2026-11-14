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

// Key exchange methods for a device's SRTP (phone-feature-parity S-16): SDES
// keys in the SDP (S-15, TLS legs only), or a DTLS-SRTP handshake on the media
// path (also only on a TLS leg, so the fingerprint in the SDP is protected).
const (
	KeyExSDES = "sdes"
	KeyExDTLS = "dtls"
)

// ParseKeyEx maps a stored or submitted key exchange to its value.
func ParseKeyEx(s string) (string, bool) {
	switch v := strings.ToLower(strings.TrimSpace(s)); v {
	case KeyExSDES, KeyExDTLS:
		return v, true
	}
	return "", false
}

// NormalizeKeyEx returns the stored key exchange, SDES for anything unknown.
func NormalizeKeyEx(s string) string {
	if v, ok := ParseKeyEx(s); ok {
		return v
	}
	return KeyExSDES
}
