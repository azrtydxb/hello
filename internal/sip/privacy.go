// Anonymous outbound calls (RFC 3323 / RFC 3325, phone-feature-parity S-19).
//
// A call is anonymous toward a trunk when any of these holds: the trunk's
// anonymous policy is "always", the calling extension has anonymous outbound
// on, or the device asked for privacy (a Privacy header carrying id, user or
// header, or a From of "anonymous", as the Grandstream "Send Anonymous"
// setting sends). The trunk leg then carries an anonymous From, the number
// the trunk asserts (its asserted identity, else the call's caller ID) in
// P-Asserted-Identity or P-Preferred-Identity, and Privacy: id. The device's
// privacy request is kept (its values are merged with id) and is never
// stripped; the device's own identity headers are never forwarded, as Hello
// asserts the identity, not the phone.
package sip

import (
	"strings"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/emiago/sipgo/sip"
)

const (
	anonymousUser   = "anonymous"
	anonymousDomain = "anonymous.invalid"
)

// privacyTokens are the values of a request's Privacy headers, lower case.
func privacyTokens(req *sip.Request) []string {
	if req == nil {
		return nil
	}
	var out []string
	for _, h := range req.GetHeaders("Privacy") {
		for _, t := range strings.FieldsFunc(h.Value(), func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
			out = append(out, strings.ToLower(t))
		}
	}
	return out
}

// requestsPrivacy reports whether a request asks for anonymity: a Privacy
// header naming id, user or header (none cancels the request), or an
// anonymous From.
func requestsPrivacy(req *sip.Request) bool {
	if req == nil {
		return false
	}
	asked := false
	for _, t := range privacyTokens(req) {
		switch t {
		case "none":
			return false
		case "id", "user", "header":
			asked = true
		}
	}
	if from := req.From(); from != nil && strings.EqualFold(from.Address.User, anonymousUser) {
		asked = true
	}
	return asked
}

// outboundPrivacy is the Privacy header value for a trunk leg: the device's
// request with id added.
func outboundPrivacy(req *sip.Request) string {
	out := []string{"id"}
	for _, t := range privacyTokens(req) {
		if t == "none" || t == "id" {
			continue
		}
		dup := false
		for _, o := range out {
			dup = dup || o == t
		}
		if !dup {
			out = append(out, t)
		}
	}
	return strings.Join(out, ";")
}

// anonymousReason says why this trunk leg is anonymous, or "" when it is not.
func (l *leg) anonymousReason() string {
	c := l.c
	if l.trunk == nil {
		return ""
	}
	switch {
	case l.trunk.AnonymousPolicy == routing.AnonymousAlways:
		return "trunk policy"
	case c.callerDevice != "" && c.extensionAnonymous():
		return "extension setting"
	case requestsPrivacy(c.inv):
		return "caller privacy request"
	}
	return ""
}

// extensionAnonymous reports the calling extension's anonymous outbound flag.
func (c *call) extensionAnonymous() bool {
	snap := c.s.deps.Snapshots.Current()
	if snap == nil {
		return false
	}
	e, ok := snap.Extension(c.callerNum)
	return ok && e.AnonymousOutbound
}

// applyAnonymous turns a trunk leg's INVITE into an anonymous one when
// anonymousReason says so; from is the INVITE's From header.
func (l *leg) applyAnonymous(req *sip.Request, from *sip.FromHeader) {
	why := l.anonymousReason()
	if why == "" {
		return
	}
	t := l.trunk
	from.DisplayName = "Anonymous"
	from.Address = sip.Uri{Scheme: "sip", User: anonymousUser, Host: anonymousDomain}
	// The device's own identity headers are Hello's to assert, not the phone's.
	req.RemoveHeader("P-Asserted-Identity")
	req.RemoveHeader("P-Preferred-Identity")
	id := t.AssertedIdentity
	if id == "" {
		id = l.callerID
	}
	asserted := "none"
	if id != "" {
		name := "P-Asserted-Identity"
		if t.IdentityHeader == routing.IdentityPPI {
			name = "P-Preferred-Identity"
		}
		req.AppendHeader(sip.NewHeader(name, "<sip:"+id+"@"+trunkDomain(*t)+">"))
		asserted = name
	}
	req.AppendHeader(sip.NewHeader("Privacy", outboundPrivacy(l.c.inv)))
	l.c.addTrace("Anonymous call to trunk " + t.Name + " (" + why + "), identity in " + asserted)
}
