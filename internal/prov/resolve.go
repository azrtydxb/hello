package prov

import (
	"path"
	"strings"
)

// Resolve picks the template for a phone (spec S-8): the explicit
// override, else among the templates of the phone's vendor whose model glob
// matches its model the highest priority, then the more specific glob
// (more literal characters), then the lower ID. False when none matches.
func Resolve(phone Phone, override *Template, all []Template) (Template, bool) {
	if override != nil {
		return *override, true
	}
	var best Template
	found := false
	for _, t := range all {
		if t.Vendor != phone.Vendor || !GlobMatch(t.ModelGlob, phone.Model) {
			continue
		}
		if !found || better(t, best) {
			best, found = t, true
		}
	}
	return best, found
}

func better(a, b Template) bool {
	if a.Priority != b.Priority {
		return a.Priority > b.Priority
	}
	if sa, sb := globSpecificity(a.ModelGlob), globSpecificity(b.ModelGlob); sa != sb {
		return sa > sb
	}
	return a.ID < b.ID
}

// GlobMatch matches a model against a model glob (path.Match syntax),
// ignoring case. A malformed glob matches nothing.
func GlobMatch(glob, model string) bool {
	ok, err := path.Match(strings.ToUpper(glob), strings.ToUpper(model))
	return err == nil && ok
}

// globSpecificity counts a glob's literal characters.
func globSpecificity(glob string) int {
	n := 0
	inClass := false
	for i := 0; i < len(glob); i++ {
		switch c := glob[i]; {
		case inClass:
			if c == ']' {
				inClass = false
			}
		case c == '[':
			inClass = true
		case c == '*' || c == '?':
		case c == '\\':
			i++
			n++
		default:
			n++
		}
	}
	return n
}
