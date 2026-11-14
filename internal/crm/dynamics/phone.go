package dynamics

import "strings"

// Dataverse stores a phone number as the text somebody typed and has no
// normalised phone column, so "contains the digits" finds only numbers stored
// without separators. phoneVariants lists the fragments a stored number is
// likely to contain: the last eight digits run together (the national and
// the international spelling of a line share them, "011..." against "+32 11
// ..."), and the last digits grouped the way people write them (pairs,
// 2-3-3, 3-3-2, 4-4, the last six in pairs, the last seven as 3-4) with a
// space, dash, dot or slash between the groups. Each is queried with
// contains(); phoneMatches then confirms the digits, so a short fragment
// that matches a different number does no harm.
func phoneVariants(digits string) []string {
	tail := lastDigits(digits, matchDigits)
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(tail)
	for _, sep := range []string{" ", "-", ".", "/"} {
		for _, g := range [][]int{{2, 2, 2, 2}, {2, 3, 3}, {3, 3, 2}, {4, 4}, {2, 2, 2}, {3, 4}} {
			add(grouped(tail, g, sep))
		}
	}
	return out
}

// matchDigits is how many trailing digits identify a line.
const matchDigits = 8

// grouped writes the last sum(sizes) digits of d in groups of the given
// sizes (first to last); "" when d is shorter than that.
func grouped(d string, sizes []int, sep string) string {
	n := 0
	for _, s := range sizes {
		n += s
	}
	if len(d) < n {
		return ""
	}
	d = d[len(d)-n:]
	parts := make([]string, 0, len(sizes))
	for _, s := range sizes {
		parts = append(parts, d[:s])
		d = d[s:]
	}
	return strings.Join(parts, sep)
}

func lastDigits(d string, n int) string {
	if len(d) > n {
		return d[len(d)-n:]
	}
	return d
}

// onlyDigits drops everything but 0-9.
func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// phoneMatches reports whether a stored phone text is the number whose digits
// are digits: both end in the same last eight digits (all of them, for a
// shorter number).
func phoneMatches(stored, digits string) bool {
	s := onlyDigits(stored)
	need := min(matchDigits, len(digits))
	if need == 0 || len(s) < need {
		return false
	}
	return lastDigits(s, need) == lastDigits(digits, need)
}
