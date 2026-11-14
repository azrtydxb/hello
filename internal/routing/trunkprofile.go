package routing

import "slices"

// Trunk profiles (docs/teams.md). A profile is a set of defaults a new trunk
// takes for the fields it leaves unset and a few limits the API enforces when
// the trunk is written. hello-sip never reads it: a trunk of any profile is
// carried by the same code.
const (
	// ProfileGeneric is every trunk that is not a peer SBC.
	ProfileGeneric = "generic"
	// ProfileSBCPeer is a trunk to a session border controller that Hello
	// peers with by IP, for example the certified SBC in front of Microsoft
	// Teams Direct Routing.
	ProfileSBCPeer = "sbc-peer"
)

// ValidProfile reports whether p is a profile name ("" is generic).
func ValidProfile(p string) bool {
	return p == "" || p == ProfileGeneric || p == ProfileSBCPeer
}

// SBCPeerCodecs is what an SBC peer is offered, wideband first: G.722 and
// G.711, the codecs Microsoft Teams supports on a Direct Routing trunk. Opus
// is not offered to it.
var SBCPeerCodecs = []string{"G722", "PCMU", "PCMA"}

// SBCPeerLimits are the limits of the sbc-peer profile in words, for the UI
// and the API.
var SBCPeerLimits = []string{
	"Mode ip: the SBC is recognised by its address, not registered.",
	"At least one source CIDR (the SBC's signalling addresses) and at least one destination.",
	"Codecs only G.722, PCMU and PCMA.",
	"SRTP is not required: the leg to the SBC is SIP over UDP on the LAN or DMZ, and the SBC encrypts towards Microsoft.",
}

// SBCPeerProblems checks a trunk of the sbc-peer profile and returns what is
// wrong with it (field path and message); nil means it satisfies the profile.
func SBCPeerProblems(mode string, sourceCIDRs []string, destinations int, codecs []string, srtp string) []FieldError {
	var out []FieldError
	if mode != "ip" {
		out = append(out, FieldError{Path: "mode", Message: `an SBC peer is IP-authenticated: mode must be "ip"`})
	}
	if len(sourceCIDRs) == 0 {
		out = append(out, FieldError{Path: "sourceCidrs", Message: "an SBC peer needs the SBC's signalling addresses as source CIDRs; calls from anywhere else are refused"})
	}
	if destinations == 0 {
		out = append(out, FieldError{Path: "destinations", Message: "an SBC peer needs the SBC as a destination"})
	}
	for _, c := range codecs {
		if !slices.Contains(SBCPeerCodecs, c) {
			out = append(out, FieldError{Path: "codecs", Message: "an SBC peer is offered only G722, PCMU and PCMA, not " + c})
		}
	}
	if srtp == "required" {
		out = append(out, FieldError{Path: "srtp", Message: "an SBC peer cannot require SRTP: the leg to the SBC is SIP over UDP"})
	}
	return out
}
