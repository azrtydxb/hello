package routing

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// maxRegexLen bounds every regex in the configuration (spec constraint: a
// route cannot be a denial-of-service vector). Counted in characters.
const maxRegexLen = 500

// Transform semantics, applied in this order:
//
//  1. Strip removes the first Strip characters of the number (a leading "+"
//     counts as one). Stripping more characters than the number has leaves
//     it empty.
//  2. Prefix is prepended.
//  3. If Regex is set, its leftmost match in the result is replaced by
//     Template expanded against that match; text outside the match is
//     kept, so an anchored regex (^...$) replaces the whole number. If the
//     regex does not match, this step is a no-op (the decision trace says
//     so) — it is not an error.
//
// Template syntax: literal characters may only be "+", digits, "*" and "#";
// group references must be written ${N} (N a group number without leading
// zeros, 0 being the whole match) or ${name} (a named group). A bare "$"
// — including Go's $1 and $name short forms, whose boundaries are
// ambiguous ("$1x" means group "1x") — is rejected.
//
// The result must match ^\+?[0-9*#]+$; anything else is an error.

// compiledTransform is a validated Transform with its regex compiled once.
type compiledTransform struct {
	strip    int
	prefix   string
	re       *regexp.Regexp
	template string
}

func (ct *compiledTransform) identity() bool {
	return ct.strip == 0 && ct.prefix == "" && ct.re == nil
}

// compileTransform validates tr, appending a FieldError under path for each
// problem. ok is false when any problem was found.
func compileTransform(tr Transform, path string, errs *[]FieldError) (ct compiledTransform, ok bool) {
	n := len(*errs)
	if tr.Strip < 0 {
		addErr(errs, path+".strip", "must not be negative")
	}
	if !validPrefix(tr.Prefix) {
		addErr(errs, path+".prefix", "may contain only +, digits, * and #, with + only as the first character")
	}
	switch {
	case tr.Regex == "" && tr.Template != "":
		addErr(errs, path+".template", "a template requires a regex")
	case tr.Regex != "":
		re, msg := compileRegex(tr.Regex)
		if re == nil {
			addErr(errs, path+".regex", msg)
			break
		}
		if tr.Template == "" {
			addErr(errs, path+".template", "is required when a regex is set")
			break
		}
		if msg := checkTemplate(tr.Template, re); msg != "" {
			addErr(errs, path+".template", msg)
			break
		}
		ct.re = re
	}
	ct.strip, ct.prefix, ct.template = tr.Strip, tr.Prefix, tr.Template
	return ct, len(*errs) == n
}

// compileRegex compiles a configuration regex (RE2 via Go's regexp) after
// the length check, returning nil and a message on failure.
func compileRegex(expr string) (*regexp.Regexp, string) {
	if utf8.RuneCountInString(expr) > maxRegexLen {
		return nil, fmt.Sprintf("is longer than %d characters", maxRegexLen)
	}
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, "does not compile: " + err.Error()
	}
	return re, ""
}

// checkTemplate returns "" when tmpl uses only the accepted syntax and
// references only groups re defines, else a message naming the problem.
func checkTemplate(tmpl string, re *regexp.Regexp) string {
	for i := 0; i < len(tmpl); {
		c := tmpl[i]
		if c != '$' {
			if !isNumberChar(c) && c != '+' {
				return fmt.Sprintf("literal %s is not allowed: only +, digits, * and # and ${N} or ${name} references", strconv.Quote(string(rune(c))))
			}
			i++
			continue
		}
		if i+1 >= len(tmpl) || tmpl[i+1] != '{' {
			return "a bare $ is not allowed: write ${1} or ${name}"
		}
		end := strings.IndexByte(tmpl[i+2:], '}')
		if end < 0 {
			return "unterminated ${ reference"
		}
		if msg := checkGroupRef(tmpl[i+2:i+2+end], re); msg != "" {
			return msg
		}
		i += end + 3
	}
	return ""
}

func checkGroupRef(ref string, re *regexp.Regexp) string {
	if ref == "" {
		return "empty ${} reference"
	}
	allDigits := true
	for i := 0; i < len(ref); i++ {
		c := ref[i]
		switch {
		case c >= '0' && c <= '9':
		case c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
			allDigits = false
		default:
			return fmt.Sprintf("reference ${%s} is not a group number or name", ref)
		}
	}
	if !allDigits {
		if re.SubexpIndex(ref) < 0 {
			return fmt.Sprintf("references group ${%s}, which the regex does not define", ref)
		}
		return ""
	}
	if len(ref) > 1 && ref[0] == '0' {
		return fmt.Sprintf("reference ${%s} has a leading zero", ref)
	}
	n, err := strconv.Atoi(ref)
	if err != nil || n > re.NumSubexp() {
		return fmt.Sprintf("references group ${%s}, but the regex defines only %d", ref, re.NumSubexp())
	}
	return ""
}

// errInvalidNumber is wrapped by every "result is not a number" error.
var errInvalidNumber = errors.New("not a valid number (only +, digits, * and #)")

// apply runs the transform. regexMissed reports that a regex was set but
// did not match (the regex step was skipped). The result is always checked
// against ^\+?[0-9*#]+$, even for the identity transform.
func (ct *compiledTransform) apply(number string) (out string, regexMissed bool, err error) {
	s := number
	if ct.strip > 0 {
		if ct.strip >= len(s) {
			s = ""
		} else {
			s = s[ct.strip:]
		}
	}
	s = ct.prefix + s
	if ct.re != nil {
		if m := ct.re.FindStringSubmatchIndex(s); m == nil {
			regexMissed = true
		} else {
			b := make([]byte, 0, len(s)+len(ct.template))
			b = append(b, s[:m[0]]...)
			b = ct.re.ExpandString(b, ct.template, s, m)
			b = append(b, s[m[1]:]...)
			s = string(b)
		}
	}
	if !validNumber(s) {
		return s, regexMissed, fmt.Errorf("result %s is %w", show(s), errInvalidNumber)
	}
	return s, regexMissed, nil
}

// ApplyTransform validates tr exactly as Compile does and applies it to
// number. It returns the first validation problem as an error, or an error
// when the result is not ^\+?[0-9*#]+$. A regex that does not match leaves
// the number as strip and prefix made it.
func ApplyTransform(tr Transform, number string) (string, error) {
	var errs []FieldError
	ct, ok := compileTransform(tr, "transform", &errs)
	if !ok {
		return "", fmt.Errorf("%s: %s", errs[0].Path, errs[0].Message)
	}
	out, _, err := ct.apply(number)
	if err != nil {
		return "", err
	}
	return out, nil
}

func isNumberChar(c byte) bool { return (c >= '0' && c <= '9') || c == '*' || c == '#' }

// validNumber reports whether s matches ^\+?[0-9*#]+$.
func validNumber(s string) bool {
	s = strings.TrimPrefix(s, "+")
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if !isNumberChar(s[i]) {
			return false
		}
	}
	return true
}

// validPrefix reports whether s matches ^\+?[0-9*#]*$.
func validPrefix(s string) bool { return s == "" || s == "+" || validNumber(s) }

// show renders a caller-supplied value for a trace: a valid number as is,
// anything else quoted (so control characters cannot forge trace lines)
// and truncated.
func show(s string) string {
	if validNumber(s) && len(s) <= 64 {
		return s
	}
	return quote(s)
}

// quote is strconv.Quote with the input truncated to 64 bytes.
func quote(s string) string {
	const limit = 64
	if len(s) > limit {
		return strconv.Quote(s[:limit]) + "…"
	}
	return strconv.Quote(s)
}

func addErr(errs *[]FieldError, path, msg string) {
	*errs = append(*errs, FieldError{Path: path, Message: msg})
}
