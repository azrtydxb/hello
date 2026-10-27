package ldapdir

import (
	"errors"
	"strings"
	"unicode"

	ber "github.com/go-asn1-ber/asn1-ber"
)

// Filter limits (spec S-25).
const (
	maxFilterDepth    = 10
	maxFilterElements = 50
)

var errFilterTooComplex = errors.New("ldap: filter too complex")

type tri int8

const (
	triFalse tri = iota
	triTrue
	triUndefined
)

// filter is a parsed search filter.
type filter struct {
	kind    int
	subs    []*filter // and, or, not (one)
	attr    string    // lower case
	value   string    // normalised, for equality and approx
	initial string
	any     []string
	final   string
}

// parseFilter reads a filter and refuses one deeper than maxFilterDepth or
// with more than maxFilterElements elements. Nothing is evaluated before
// the whole filter has passed.
func parseFilter(p *ber.Packet) (*filter, error) {
	n := 0
	return parseFilterAt(p, 1, &n)
}

func parseFilterAt(p *ber.Packet, depth int, n *int) (*filter, error) {
	if depth > maxFilterDepth {
		return nil, errFilterTooComplex
	}
	*n++
	if *n > maxFilterElements {
		return nil, errFilterTooComplex
	}
	if p.ClassType != ber.ClassContext {
		return nil, errProtocol
	}
	f := &filter{kind: int(p.Tag)}
	switch f.kind {
	case filterAnd, filterOr:
		if len(p.Children) == 0 && f.kind == filterOr {
			return f, nil
		}
		for _, c := range p.Children {
			s, err := parseFilterAt(c, depth+1, n)
			if err != nil {
				return nil, err
			}
			f.subs = append(f.subs, s)
		}
	case filterNot:
		if len(p.Children) != 1 {
			return nil, errProtocol
		}
		s, err := parseFilterAt(p.Children[0], depth+1, n)
		if err != nil {
			return nil, err
		}
		f.subs = []*filter{s}
	case filterEquality, filterApprox, filterGreaterOrEqual, filterLessOrEqual:
		if len(p.Children) != 2 {
			return nil, errProtocol
		}
		f.attr = strings.ToLower(str(p.Children[0]))
		f.value = normValue(f.attr, str(p.Children[1]))
	case filterPresent:
		f.attr = strings.ToLower(str(p))
	case filterSubstrings:
		if len(p.Children) != 2 {
			return nil, errProtocol
		}
		f.attr = strings.ToLower(str(p.Children[0]))
		for _, c := range p.Children[1].Children {
			v := normValue(f.attr, str(c))
			switch c.Tag {
			case substringInitial:
				f.initial = v
			case substringAny:
				f.any = append(f.any, v)
			case substringFinal:
				f.final = v
			default:
				return nil, errProtocol
			}
		}
	case filterExtensible:
		// Matching rules are not supported: the filter is undefined.
	default:
		return nil, errProtocol
	}
	return f, nil
}

// normValue folds a value for caseIgnoreMatch; a telephone number is also
// stripped of the separators people type in it (telephoneNumberMatch).
func normValue(attr, v string) string {
	if phoneAttr(attr) {
		return strings.Map(func(r rune) rune {
			switch r {
			case ' ', '-', '(', ')', '.':
				return -1
			}
			return unicode.ToLower(r)
		}, v)
	}
	return strings.ToLower(strings.Join(strings.Fields(v), " "))
}

func phoneAttr(attr string) bool {
	return attr == "telephonenumber" || attr == "mobile" || attr == "homephone"
}

// eval evaluates f on e with the three-valued logic of RFC 4511 4.5.1.7.
func (f *filter) eval(e *entry) tri {
	switch f.kind {
	case filterAnd:
		out := triTrue
		for _, s := range f.subs {
			switch s.eval(e) {
			case triFalse:
				return triFalse
			case triUndefined:
				out = triUndefined
			}
		}
		return out
	case filterOr:
		out := triFalse
		for _, s := range f.subs {
			switch s.eval(e) {
			case triTrue:
				return triTrue
			case triUndefined:
				out = triUndefined
			}
		}
		return out
	case filterNot:
		switch f.subs[0].eval(e) {
		case triTrue:
			return triFalse
		case triFalse:
			return triTrue
		}
		return triUndefined
	case filterGreaterOrEqual, filterLessOrEqual, filterExtensible:
		return triUndefined
	}
	if !knownAttr(f.attr) {
		return triUndefined
	}
	vals := e.norm[f.attr]
	switch f.kind {
	case filterPresent:
		return boolTri(len(vals) > 0)
	case filterEquality, filterApprox:
		for _, v := range vals {
			if v == f.value {
				return triTrue
			}
		}
		return triFalse
	case filterSubstrings:
		for _, v := range vals {
			if f.substr(v) {
				return triTrue
			}
		}
		return triFalse
	}
	return triUndefined
}

func boolTri(b bool) tri {
	if b {
		return triTrue
	}
	return triFalse
}

// substr reports whether v matches the initial, any and final parts in
// order, without overlap.
func (f *filter) substr(v string) bool {
	if f.initial != "" {
		if !strings.HasPrefix(v, f.initial) {
			return false
		}
		v = v[len(f.initial):]
	}
	for _, a := range f.any {
		i := strings.Index(v, a)
		if i < 0 {
			return false
		}
		v = v[i+len(a):]
	}
	return f.final == "" || strings.HasSuffix(v, f.final)
}
