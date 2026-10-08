package mcp

import (
	"strings"

	"github.com/azrtydxb/hello/internal/replay"
)

// Withheld replaces every x-hello-secret value in an MCP result (spec S-15).
const Withheld = replay.Withheld

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
				next, _ = props[replay.Unescape(seg)].(map[string]any)
				if next != nil && i == len(segs)-1 {
					props[replay.Unescape(seg)] = map[string]any{
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
