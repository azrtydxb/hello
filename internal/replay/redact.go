package replay

import "strings"

// Withheld replaces every x-hello-secret value in a replayed result.
const Withheld = "[withheld: shown only in the Hello console]"

// Redact replaces, in the decoded JSON value v, every value found at one of
// paths (Operation.Secrets: "/secret", "/items/*/url") with Withheld. It
// walks the decoded structure, never the text, so a secret nested in a list
// is found by its schema path and not by its field name. A "*" segment
// matches every element of an array or every member of an object.
func Redact(v any, paths []string) {
	for _, p := range paths {
		segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
		redactAt(v, segs)
	}
}

func redactAt(v any, segs []string) {
	if len(segs) == 0 {
		return
	}
	seg, rest := Unescape(segs[0]), segs[1:]
	switch n := v.(type) {
	case map[string]any:
		if seg == "*" {
			for k, child := range n {
				if len(rest) == 0 {
					n[k] = Withheld
					continue
				}
				redactAt(child, rest)
			}
			return
		}
		child, ok := n[seg]
		if !ok {
			return
		}
		if len(rest) == 0 {
			n[seg] = Withheld
			return
		}
		redactAt(child, rest)
	case []any:
		if seg != "*" {
			return
		}
		for i, child := range n {
			if len(rest) == 0 {
				n[i] = Withheld
				continue
			}
			redactAt(child, rest)
		}
	}
}

// Unescape decodes a JSON pointer segment (~1 is "/", ~0 is "~").
func Unescape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}
