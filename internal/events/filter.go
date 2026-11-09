package events

import (
	"fmt"
	"strings"
)

// Filter selects events for one reader.
type Filter struct {
	// Types are exact types or a prefix pattern ending in ".*" (call.*);
	// empty is every type.
	Types []string
	// Extension, when set, keeps only events about that extension.
	Extension string
	// Allowed limits the reader to these extension numbers; nil is every
	// extension. An event with no extension is only for an unrestricted
	// reader.
	Allowed []string
}

// ParseTypes validates a comma-separated type list: each element is a
// registered type or a prefix pattern such as call.*.
func ParseTypes(list string) ([]string, error) {
	if strings.TrimSpace(list) == "" {
		return nil, nil
	}
	var out []string
	for t := range strings.SplitSeq(list, ",") {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if p, ok := strings.CutSuffix(t, ".*"); ok {
			if p == "" || strings.ContainsAny(p, "*%_ ") {
				return nil, fmt.Errorf("invalid event type pattern %q", t)
			}
		} else if !KnownType(t) {
			return nil, fmt.Errorf("unknown event type %q", t)
		}
		out = append(out, t)
	}
	return out, nil
}

// split separates exact types from prefixes ("call." for call.*).
func (f Filter) split() (exact, prefixes []string) {
	for _, t := range f.Types {
		if p, ok := strings.CutSuffix(t, "*"); ok {
			prefixes = append(prefixes, p)
		} else {
			exact = append(exact, t)
		}
	}
	return exact, prefixes
}

// Restricted reports whether the reader may see only some extensions.
func (f Filter) Restricted() bool { return f.Allowed != nil }

// Matches reports whether e passes the filter.
func (f Filter) Matches(e Event) bool {
	if f.Extension != "" && e.Data.Extension != f.Extension {
		return false
	}
	if f.Allowed != nil && !contains(f.Allowed, e.Data.Extension) {
		return false
	}
	if len(f.Types) == 0 {
		return true
	}
	exact, prefixes := f.split()
	if contains(exact, e.Type) {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(e.Type, p) {
			return true
		}
	}
	return false
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
