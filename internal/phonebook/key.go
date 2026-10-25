package phonebook

import "strings"

// Dialing is the numbering context a lookup key is built in: the prefix a
// dialled international number starts with and the country code a number
// with the trunk prefix 0 belongs to (HELLO_DIAL_INTL_PREFIX and
// HELLO_DIAL_COUNTRY_CODE).
type Dialing struct {
	// IntlPrefix becomes "+" when a number starts with it; empty means "00".
	IntlPrefix string
	// CountryCode (digits, no "+") is put in front of a number with the
	// trunk prefix 0 to make it E.164; empty leaves such a number national.
	CountryCode string
}

// suffixLen is how many trailing digits the fallback match compares.
const suffixLen = 9

// minKeyDigits is the shortest number that can name a caller unless it is
// an extension: shorter ones are service and feature numbers.
const minKeyDigits = 4

// Key reduces number to its lookup key (spec P-8) with the default
// numbering context and no known extensions.
func Key(number string) (string, bool) { return Dialing{}.Key(number, nil) }

// Key reduces a stored or dialled number to its lookup key. isExtension
// reports whether digits are an extension number (nil: none is); a 2 to 10
// digit number that is one keeps its form. ok is false when the number
// cannot name anybody: anonymous, a feature code, a short code, or anything
// with characters other than digits and separators.
func (d Dialing) Key(number string, isExtension func(string) bool) (key string, ok bool) {
	n := userPart(strings.TrimSpace(number))
	if n == "" || n[0] == '*' || strings.EqualFold(n, "anonymous") {
		return "", false
	}
	var b strings.Builder
	b.Grow(len(n))
	for i := 0; i < len(n); i++ {
		switch c := n[i]; {
		case c >= '0' && c <= '9':
			b.WriteByte(c)
		case c == '+' && b.Len() == 0:
			b.WriteByte(c)
		case c == ' ' || c == '.' || c == '-' || c == '(' || c == ')':
		default:
			return "", false
		}
	}
	k := b.String()
	plus := strings.HasPrefix(k, "+")
	digits := strings.TrimPrefix(k, "+")
	if digits == "" {
		return "", false
	}
	if !plus && len(digits) >= 2 && len(digits) <= 10 && isExtension != nil && isExtension(digits) {
		return digits, true
	}
	if !plus {
		prefix := d.IntlPrefix
		if prefix == "" {
			prefix = "00"
		}
		switch {
		case strings.HasPrefix(digits, prefix) && len(digits) > len(prefix):
			plus, digits = true, digits[len(prefix):]
		case digits[0] == '0' && len(digits) > 1 && d.CountryCode != "":
			plus, digits = true, d.CountryCode+digits[1:]
		}
	}
	if len(digits) < minKeyDigits {
		return "", false
	}
	if plus {
		return "+" + digits, true
	}
	return digits, true
}

// Suffix is the fallback key: the last nine digits of key, when it has at
// least nine (spec P-8). A caller is named by it only when exactly one
// entry has that suffix.
func Suffix(key string) (string, bool) {
	digits := strings.TrimPrefix(key, "+")
	if len(digits) < suffixLen {
		return "", false
	}
	return digits[len(digits)-suffixLen:], true
}

// userPart is the number inside a SIP, SIPS or tel URI (parameters and
// host dropped), or s unchanged when it is no URI.
func userPart(s string) string {
	for _, scheme := range [...]string{"sips:", "sip:", "tel:"} {
		if len(s) >= len(scheme) && strings.EqualFold(s[:len(scheme)], scheme) {
			s = s[len(scheme):]
			if i := strings.IndexByte(s, '@'); i >= 0 {
				s = s[:i]
			}
			if i := strings.IndexAny(s, ";?"); i >= 0 {
				s = s[:i]
			}
			return strings.ReplaceAll(strings.ReplaceAll(s, "%2B", "+"), "%2b", "+")
		}
	}
	return s
}
