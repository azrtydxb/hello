// ICE-lite for the anchored media path (phone-feature-parity S-16, RFC 8445
// section 3.3). Hello is always the lite, controlled side: it never gathers
// or checks anything. It publishes its relay address as the one host
// candidate, answers the peer's STUN binding requests (authenticated with the
// short-term credentials of the SDP exchange) and takes the address a valid
// check came from as the address media may come from and go to.
package media

import (
	"crypto/rand"
	"net"
	"strconv"
	"strings"
	"sync"

	"github.com/pion/stun/v3"
)

// ICECandidate is one a=candidate line (RFC 8839): Hello keeps what a peer
// offered for the call trace and tests; as the lite side it never checks them.
type ICECandidate struct {
	Foundation string
	Component  int
	Transport  string
	Priority   uint32
	IP         string
	Port       int
	Type       string
}

// ParseICECandidate parses the value of an a=candidate line (with or without
// the "candidate:" prefix).
func ParseICECandidate(v string) (ICECandidate, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "candidate:")
	f := strings.Fields(v)
	if len(f) < 8 || f[6] != "typ" {
		return ICECandidate{}, false
	}
	comp, err1 := strconv.Atoi(f[1])
	prio, err2 := strconv.ParseUint(f[3], 10, 32)
	port, err3 := strconv.Atoi(f[5])
	if err1 != nil || err2 != nil || err3 != nil || port < 0 || port > 65535 {
		return ICECandidate{}, false
	}
	return ICECandidate{Foundation: f[0], Component: comp, Transport: strings.ToLower(f[2]),
		Priority: uint32(prio), IP: f[4], Port: port, Type: f[7]}, true
}

// ICEInfo is the ICE part of an SDP: credentials, whether the sender is lite
// and its candidates.
type ICEInfo struct {
	Ufrag      string
	Pwd        string
	Lite       bool
	Candidates []ICECandidate
}

// usable reports whether the credentials are present and within RFC 8839's
// limits (ufrag 4 to 256 characters, pwd 22 to 256).
func (i *ICEInfo) usable() bool {
	return i != nil && len(i.Ufrag) >= 4 && len(i.Ufrag) <= 256 && len(i.Pwd) >= 22 && len(i.Pwd) <= 256
}

const iceChars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randomICEString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = iceChars[int(b[i])%len(iceChars)]
	}
	return string(b)
}

// hostPriority is the RFC 8445 priority of the one host candidate:
// type preference 126, local preference 65535, component 1.
const hostPriority = 126<<24 | 65535<<8 | (256 - 1)

// iceLite is one endpoint's ICE-lite state.
type iceLite struct {
	mu          sync.Mutex
	ufrag, pwd  string
	remoteUfrag string
}

func newICELite() *iceLite {
	return &iceLite{ufrag: randomICEString(8), pwd: randomICEString(24)}
}

// local returns the credentials Hello puts in its SDP.
func (i *iceLite) local() (ufrag, pwd string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.ufrag, i.pwd
}

// setRemote records the peer's ufrag; it reports whether it changed (an ICE
// restart, which starts the address verification over).
func (i *iceLite) setRemote(ufrag string) bool {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.remoteUfrag == ufrag {
		return false
	}
	i.remoteUfrag = ufrag
	return true
}

// iceResult is what a datagram meant to the ICE agent.
type iceResult int

const (
	iceIgnored  iceResult = iota // not a binding request, or not for this endpoint
	iceBadCreds                  // a binding request that did not authenticate
	iceChecked                   // an authenticated check, answered
	iceConflict                  // an authenticated check from a peer that is also controlled
)

// handle processes a STUN datagram from the peer. It returns the response to
// send (nil when none) and, for an authenticated check, whether it carried
// USE-CANDIDATE. Only binding requests whose USERNAME names this endpoint's
// ufrag and whose MESSAGE-INTEGRITY verifies with its password are answered;
// everything else is dropped silently so the port is no oracle.
func (i *iceLite) handle(data []byte, from *net.UDPAddr) (resp []byte, res iceResult, nominate bool) {
	m := &stun.Message{Raw: append([]byte(nil), data...)}
	if err := m.Decode(); err != nil || m.Type != stun.BindingRequest {
		return nil, iceIgnored, false
	}
	var u stun.Username
	if err := u.GetFrom(m); err != nil {
		return nil, iceBadCreds, false
	}
	i.mu.Lock()
	ufrag, pwd, remote := i.ufrag, i.pwd, i.remoteUfrag
	i.mu.Unlock()
	user := string(u)
	want := ufrag + ":"
	if !strings.HasPrefix(user, want) || (remote != "" && user != want+remote) {
		return nil, iceBadCreds, false
	}
	integrity := stun.NewShortTermIntegrity(pwd)
	if err := integrity.Check(m); err != nil {
		return nil, iceBadCreds, false
	}
	if m.Contains(stun.AttrFingerprint) {
		if err := stun.Fingerprint.Check(m); err != nil {
			return nil, iceBadCreds, false
		}
	}
	if m.Contains(stun.AttrICEControlled) {
		// The peer thinks it is controlled too; the lite side is always
		// controlled, so the peer must switch (RFC 8445 section 7.3.1.1).
		out, err := stun.Build(stun.NewTransactionIDSetter(m.TransactionID),
			stun.NewType(stun.MethodBinding, stun.ClassErrorResponse),
			stun.ErrorCodeAttribute{Code: stun.CodeRoleConflict, Reason: []byte("Role Conflict")},
			integrity, stun.Fingerprint)
		if err != nil {
			return nil, iceIgnored, false
		}
		return out.Raw, iceConflict, false
	}
	out, err := stun.Build(stun.NewTransactionIDSetter(m.TransactionID), stun.BindingSuccess,
		&stun.XORMappedAddress{IP: from.IP, Port: from.Port}, integrity, stun.Fingerprint)
	if err != nil {
		return nil, iceIgnored, false
	}
	return out.Raw, iceChecked, m.Contains(stun.AttrUseCandidate)
}

// isSTUN reports whether a datagram is STUN (RFC 7983: first byte 0 to 3, and
// the magic cookie).
func isSTUN(b []byte) bool {
	return len(b) >= 20 && b[0] < 4 && b[4] == 0x21 && b[5] == 0x12 && b[6] == 0xa4 && b[7] == 0x42
}

// isDTLS reports whether a datagram is a DTLS record (RFC 7983: first byte 20
// to 63).
func isDTLS(b []byte) bool { return len(b) >= 13 && b[0] >= 20 && b[0] <= 63 }
