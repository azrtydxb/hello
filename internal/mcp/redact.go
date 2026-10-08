package mcp

import "strings"

// Withheld replaces every x-hello-secret value in an MCP result (spec S-15).
const Withheld = "[withheld: shown only in the Hello console]"

// redact replaces, in the decoded JSON value v, every value found at one of
// paths (Operation.Secrets: "/secret", "/items/*/url") with Withheld. It
// walks the decoded structure, never the text, so a secret nested in a list
// is found by its schema path and not by its field name. A "*" segment
// matches every element of an array or every member of an object.
func redact(v any, paths []string) {
	for _, p := range paths {
		segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
		redactAt(v, segs)
	}
}

func redactAt(v any, segs []string) {
	if len(segs) == 0 {
		return
	}
	seg, rest := unescape(segs[0]), segs[1:]
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

// unescape decodes a JSON pointer segment (~1 is "/", ~0 is "~").
func unescape(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "~1", "/"), "~0", "~")
}

// withholdSchema rewrites the output schema s so every secret path is typed
// as a string (the withheld note), as the redacted result is.
func withholdSchema(s map[string]any, paths []string) {
	for _, p := range paths {
		node := s
		segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
		for i, seg := range segs {
			var next map[string]any
			if seg == "*" {
				next, _ = node["items"].(map[string]any)
				if next == nil {
					next, _ = node["additionalProperties"].(map[string]any)
				}
			} else {
				props, _ := node["properties"].(map[string]any)
				next, _ = props[unescape(seg)].(map[string]any)
				if next != nil && i == len(segs)-1 {
					props[unescape(seg)] = map[string]any{
						"type":        "string",
						"description": "Withheld from MCP: " + Withheld,
					}
					next = nil
				}
			}
			if next == nil {
				break
			}
			node = next
		}
	}
}
