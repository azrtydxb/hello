// Package ldapdir is Hello's read-only LDAP directory for phones (spec
// pbx-gaps S-24 and S-25): a phone binds with its own DN and searches the
// address books it is delivered. The wire codec is asn1-ber; the protocol
// handling, filter evaluation and limits are here.
package ldapdir

import (
	"errors"
	"fmt"

	ber "github.com/go-asn1-ber/asn1-ber"
)

func init() {
	// Nothing a phone sends comes near a megabyte; a bigger message is cut
	// off while it is read. The codec applies the limit before it
	// allocates.
	ber.MaxPacketLengthBytes = 1 << 20
}

// LDAP protocolOp application tags (RFC 4511).
const (
	opBindRequest     = 0
	opBindResponse    = 1
	opUnbindRequest   = 2
	opSearchRequest   = 3
	opSearchEntry     = 4
	opSearchDone      = 5
	opModifyRequest   = 6
	opModifyResponse  = 7
	opAddRequest      = 8
	opAddResponse     = 9
	opDelRequest      = 10
	opDelResponse     = 11
	opModDNRequest    = 12
	opModDNResponse   = 13
	opCompareRequest  = 14
	opCompareResponse = 15
	opAbandonRequest  = 16
	opExtRequest      = 23
	opExtResponse     = 24
)

// Result codes (RFC 4511 section 4.1.9).
const (
	ResultSuccess                = 0
	ResultOperationsError        = 1
	ResultProtocolError          = 2
	ResultTimeLimitExceeded      = 3
	ResultSizeLimitExceeded      = 4
	ResultAuthMethodNotSupported = 7
	ResultAdminLimitExceeded     = 11
	ResultUnavailableCriticalExt = 12
	ResultNoSuchObject           = 32
	ResultInvalidCredentials     = 49
	ResultInsufficientAccess     = 50
	ResultBusy                   = 51
	ResultUnavailable            = 52
	ResultUnwillingToPerform     = 53
	ResultOther                  = 80
)

// Object identifiers.
const (
	oidStartTLS              = "1.3.6.1.4.1.1466.20037"
	oidNoticeOfDisconnection = "1.3.6.1.4.1.1466.20036"
	oidPagedResults          = "1.2.840.113556.1.4.319"
)

// Search scopes.
const (
	scopeBase    = 0
	scopeSingle  = 1
	scopeSubtree = 2
)

// Filter choices (context-specific tags).
const (
	filterAnd            = 0
	filterOr             = 1
	filterNot            = 2
	filterEquality       = 3
	filterSubstrings     = 4
	filterGreaterOrEqual = 5
	filterLessOrEqual    = 6
	filterPresent        = 7
	filterApprox         = 8
	filterExtensible     = 9
)

// Substring choices.
const (
	substringInitial = 0
	substringAny     = 1
	substringFinal   = 2
)

const (
	extRequestNameTag  = 0
	extResponseNameTag = 10
	bindAuthSimple     = 0
	controlsTag        = 0
	bindVersion        = 3
	maxMessageChildren = 3
)

var errProtocol = errors.New("ldap: protocol error")

// message is one LDAPMessage: its id, the protocolOp and any controls.
type message struct {
	id       int64
	op       *ber.Packet
	controls []control
}

// control is one request control.
type control struct {
	oid      string
	critical bool
	value    []byte
}

// parseMessage reads the envelope of one LDAPMessage.
func parseMessage(p *ber.Packet) (message, error) {
	if p.ClassType != ber.ClassUniversal || p.Tag != ber.TagSequence || len(p.Children) < 2 || len(p.Children) > maxMessageChildren {
		return message{}, errProtocol
	}
	id, ok := intOf(p.Children[0])
	if !ok || id < 0 {
		return message{}, errProtocol
	}
	m := message{id: id, op: p.Children[1]}
	if m.op.ClassType != ber.ClassApplication {
		return message{}, errProtocol
	}
	if len(p.Children) == 3 {
		c := p.Children[2]
		if c.ClassType != ber.ClassContext || c.Tag != controlsTag {
			return message{}, errProtocol
		}
		for _, cp := range c.Children {
			ct, err := parseControl(cp)
			if err != nil {
				return message{}, err
			}
			m.controls = append(m.controls, ct)
		}
	}
	return m, nil
}

func parseControl(p *ber.Packet) (control, error) {
	if len(p.Children) < 1 || len(p.Children) > 3 {
		return control{}, errProtocol
	}
	c := control{oid: string(p.Children[0].Data.Bytes())}
	for _, ch := range p.Children[1:] {
		switch ch.Tag {
		case ber.TagBoolean:
			c.critical = len(ch.Data.Bytes()) == 1 && ch.Data.Bytes()[0] != 0
		case ber.TagOctetString:
			c.value = ch.Data.Bytes()
		default:
			return control{}, errProtocol
		}
	}
	return c, nil
}

// intOf reads an INTEGER or ENUMERATED (or an application-tagged integer
// such as an abandon id) from p's content.
func intOf(p *ber.Packet) (int64, bool) {
	b := p.Data.Bytes()
	if len(b) == 0 || len(b) > 8 {
		return 0, false
	}
	v, err := ber.ParseInt64(b)
	return v, err == nil
}

func str(p *ber.Packet) string { return string(p.Data.Bytes()) }

func octets(v string, desc string) *ber.Packet {
	return ber.NewString(ber.ClassUniversal, ber.TypePrimitive, ber.TagOctetString, v, desc)
}

// result builds an LDAPResult-shaped response op with the application tag
// tag.
func result(tag ber.Tag, code int, diag string) *ber.Packet {
	op := ber.Encode(ber.ClassApplication, ber.TypeConstructed, tag, nil, "result")
	op.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagEnumerated, int64(code), "resultCode"))
	op.AppendChild(octets("", "matchedDN"))
	op.AppendChild(octets(diag, "diagnosticMessage"))
	return op
}

// envelope wraps op (and an optional control) in an LDAPMessage.
func envelope(id int64, op *ber.Packet, ctl *ber.Packet) []byte {
	p := ber.Encode(ber.ClassUniversal, ber.TypeConstructed, ber.TagSequence, nil, "LDAPMessage")
	p.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, id, "messageID"))
	p.AppendChild(op)
	if ctl != nil {
		cs := ber.Encode(ber.ClassContext, ber.TypeConstructed, controlsTag, nil, "controls")
		cs.AppendChild(ctl)
		p.AppendChild(cs)
	}
	return p.Bytes()
}

// pagedResponseControl is the paged results control of a search result.
func pagedResponseControl(total int, cookie []byte) *ber.Packet {
	val := ber.NewSequence("realSearchControlValue")
	val.AppendChild(ber.NewInteger(ber.ClassUniversal, ber.TypePrimitive, ber.TagInteger, int64(total), "size"))
	val.AppendChild(octets(string(cookie), "cookie"))
	c := ber.NewSequence("control")
	c.AppendChild(octets(oidPagedResults, "controlType"))
	c.AppendChild(octets(string(val.Bytes()), "controlValue"))
	return c
}

// parsePaged reads the paged results request value.
func parsePaged(v []byte) (size int64, cookie []byte, err error) {
	p, err := ber.DecodePacketErr(v)
	if err != nil || len(p.Children) != 2 {
		return 0, nil, errProtocol
	}
	size, ok := intOf(p.Children[0])
	if !ok || size < 0 {
		return 0, nil, errProtocol
	}
	return size, p.Children[1].Data.Bytes(), nil
}

func (m message) opName() string {
	switch m.op.Tag {
	case opBindRequest:
		return "bind"
	case opSearchRequest:
		return "search"
	case opUnbindRequest:
		return "unbind"
	case opExtRequest:
		return "extended"
	case opAbandonRequest:
		return "abandon"
	default:
		return fmt.Sprintf("op%d", m.op.Tag)
	}
}
