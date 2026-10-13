package routing

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Internal-number dialing (trunk plan Task 1): the per-trunk internal
// dialing policy, the "internal" inbound destination, the caller-ID ladder
// for trunk-to-extension calls, and the fixed trace texts of contract 6.

// maxDialingPatterns bounds a trunk's internal dialing patterns (spec S-6:
// at most 100 per trunk).
const maxDialingPatterns = 100

// Fixed trace texts (contract 6): the policy verdict, the internal skip,
// the loop guard and the caller-ID ladder speak these words, so the route
// tester, hello-sip and the console render — and match — the same ones.
const (
	// Loop guard (spec S-3): a trunk-sourced call may not leave through
	// its own trunk, and leaves through outbound routing only once.
	loopSkipReason = "call came from it"
	loopReentry    = "Loop prevented: the call has already been routed outbound once"

	// Caller-ID ladder (spec S-4), when the trunk has no default either.
	cidTrunkNameSuffix = "(trunk name)"
)

// dialPattern is one compiled internal dialing pattern: an exact number or
// an X pattern compiled to an anchored regex (one [0-9] class per X), or a
// Go regex kept as given (the author anchors it).
type dialPattern struct {
	raw string
	re  *regexp.Regexp
}

// matches reports whether the pattern matches did, tried as given and then
// with a leading "+" toggled, as the inbound DID match does.
func (p dialPattern) matches(did string) bool {
	if p.re.MatchString(did) {
		return true
	}
	alt := "+" + did
	if strings.HasPrefix(did, "+") {
		alt = did[1:]
	}
	return alt != did && p.re.MatchString(alt)
}

// isXPattern reports whether p is an exact number or an X pattern: an
// optional leading "+" and then only digits, X, * and #. Anything else is a
// Go regex.
func isXPattern(p string) bool {
	p = strings.TrimPrefix(p, "+")
	if p == "" {
		return false
	}
	for i := 0; i < len(p); i++ {
		if !isNumberChar(p[i]) && p[i] != 'X' {
			return false
		}
	}
	return true
}

// compileDialPattern compiles one pattern. Exact and X patterns become an
// anchored regex with one [0-9] class per X; anything else is compiled as a
// Go regex, as given. nil and a message on failure.
func compileDialPattern(p string) (*regexp.Regexp, string) {
	if p == "" {
		return nil, "is empty"
	}
	if utf8.RuneCountInString(p) > maxRegexLen {
		return nil, fmt.Sprintf("is longer than %d characters", maxRegexLen)
	}
	if !isXPattern(p) {
		return compileRegex(p)
	}
	var b strings.Builder
	b.WriteString("^")
	for _, r := range p {
		if r == 'X' {
			b.WriteString("[0-9]")
		} else {
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	b.WriteString("$")
	re, err := regexp.Compile(b.String())
	if err != nil {
		return nil, "does not compile: " + err.Error()
	}
	return re, ""
}

// compileDialing validates a trunk's internal dialing policy, appending a
// FieldError under path for each problem, and returns the compiled patterns
// (nil unless mode is "patterns").
func compileDialing(d Dialing, path string, errs *[]FieldError) []dialPattern {
	mode := d.Mode
	switch mode {
	case "", "off", "patterns", "all":
	default:
		addErr(errs, path+".mode", `must be "off", "patterns" or "all"`)
	}
	if len(d.Patterns) > maxDialingPatterns {
		addErr(errs, path+".patterns", fmt.Sprintf("at most %d patterns are allowed", maxDialingPatterns))
	}
	if len(d.Patterns) > 0 && mode != "patterns" {
		addErr(errs, path+".patterns", `patterns are only allowed when mode is "patterns"`)
	}
	if mode == "patterns" && len(d.Patterns) == 0 {
		addErr(errs, path+".patterns", `at least one pattern is required when mode is "patterns"`)
	}
	if mode != "patterns" {
		return nil
	}
	pats := make([]dialPattern, 0, len(d.Patterns))
	for j, p := range d.Patterns {
		re, msg := compileDialPattern(p)
		if re == nil {
			// The trunk compiles misconfigured and its routes skip (the
			// snapshot marks it so); Compile itself returns no Table.
			addErr(errs, fmt.Sprintf("%s.patterns[%d]", path, j), msg)
			continue
		}
		pats = append(pats, dialPattern{raw: p, re: re})
	}
	return pats
}

// dialingAllows reports whether tr may dial the internal number did (the
// normalised DID), and which pattern allowed it: the first matching pattern
// in list order, "all" for mode "all", "" when it may not (contract 3).
func (t *Table) dialingAllows(tr *Trunk, did string) (ok bool, pattern string) {
	switch tr.InternalDialing.Mode {
	case "all":
		return true, "all"
	case "patterns":
		for _, p := range t.dial[tr.ID] {
			if p.matches(did) {
				return true, p.raw
			}
		}
	}
	return false, ""
}

// dialingVerdict is the traced policy verdict (spec S-2), checked once per
// call after DID normalisation and before the route walk.
func dialingVerdict(tr *Trunk, did string, ok bool, pattern string) string {
	switch {
	case !ok && (tr.InternalDialing.Mode == "" || tr.InternalDialing.Mode == "off"):
		return "Internal dialing off on trunk " + tr.Name
	case ok && pattern == "all":
		return fmt.Sprintf("Trunk %s may dial %s: all internal numbers", tr.Name, show(did))
	case ok:
		return fmt.Sprintf("Trunk %s may dial %s: pattern %s", tr.Name, show(did), pattern)
	default:
		return fmt.Sprintf("Trunk %s may not dial %s: no pattern matches", tr.Name, show(did))
	}
}

// dialingSkip is why an inbound route with the "internal" destination does
// not take a call the policy denies (spec S-2's traced skip).
func dialingSkip(tr *Trunk, did string) string {
	if tr.InternalDialing.Mode == "" || tr.InternalDialing.Mode == "off" {
		return "internal dialing off on trunk " + tr.Name
	}
	return fmt.Sprintf("internal dialing to %s not allowed on trunk %s", show(did), tr.Name)
}

// extensionFor looks did up in the extension namespace, as the phone path
// does: the map holds enabled extensions only. A leading "+" is ignored, as
// the inbound DID match ignores it.
func (t *Table) extensionFor(did string) (string, bool) {
	if _, ok := t.extensions[did]; ok {
		return did, true
	}
	if bare := strings.TrimPrefix(did, "+"); bare != did {
		if _, ok := t.extensions[bare]; ok {
			return bare, true
		}
	}
	return "", false
}

// internalCallerID applies the caller-ID ladder (spec S-4) for a call
// ringing an internal extension: the caller ID as received, else the
// trunk's default caller ID, else the trunk name in angle brackets.
func internalCallerID(tr *Trace, src *Trunk, received string) string {
	switch {
	case received != "":
		tr.Add(fmt.Sprintf("Caller ID %s as received", show(received)))
		return received
	case src.DefaultCallerID != "":
		tr.Add(fmt.Sprintf("Caller ID %s (default caller ID of trunk %s)", show(src.DefaultCallerID), src.Name))
		return src.DefaultCallerID
	default:
		tr.Add(fmt.Sprintf("Caller ID <%s> %s", src.Name, cidTrunkNameSuffix))
		return "<" + src.Name + ">"
	}
}

// normaliseCallerID applies the route's transform to the chosen caller ID;
// a regex miss or a failed rewrite keeps the chosen value, traced.
func normaliseCallerID(tr *Trace, ct *compiledTransform, cid string) string {
	if ct.identity() {
		return cid
	}
	out, missed, err := ct.apply(cid)
	if missed {
		tr.Add(fmt.Sprintf("Caller ID transform regex %s did not match %s; regex step skipped", ct.re.String(), show(stripPrefix(ct, cid))))
	}
	if err != nil {
		tr.Add(fmt.Sprintf("Caller ID rewrite %s failed: %s; presenting the chosen value", show(cid), err))
		return cid
	}
	tr.Add(fmt.Sprintf("Caller ID rewrite %s -> %s", show(cid), out))
	return out
}
