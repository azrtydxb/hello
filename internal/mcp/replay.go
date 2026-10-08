package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/replay"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// textCap is the largest text content a tool returns (spec S-15).
const textCap = 64 << 10

// caller is the authenticated /mcp request a tool call or resource read
// replays as: its Authorization header verbatim and its actor.
type caller struct {
	authz      string
	actor      auth.Actor
	remoteAddr string
}

type callerKey struct{}

func withCaller(ctx context.Context, c caller) context.Context {
	return context.WithValue(ctx, callerKey{}, c)
}

func callerFrom(ctx context.Context) (caller, bool) {
	c, ok := ctx.Value(callerKey{}).(caller)
	return c, ok
}

// replay sends one request to the API handler in-process (spec S-15):
// only the caller's Authorization header is copied.
func (s *server) replay(ctx context.Context, c caller, method, path string, query url.Values, body []byte) (replay.Result, error) {
	return replay.Do(ctx, s.api, replay.Request{
		Method: method, Path: path, Query: query, Body: body,
		Header:     http.Header{"Authorization": {c.authz}},
		ClientID:   c.actor.ClientID,
		RemoteAddr: c.remoteAddr,
	})
}

// request turns tool arguments into the method, path, query and body of
// op: path parameters escaped into the path, query parameters encoded, and
// "body" as the JSON request body.
func request(op apispec.Operation, raw json.RawMessage) (path string, query url.Values, body []byte, err error) {
	args := map[string]any{}
	if len(raw) > 0 && string(raw) != "null" {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&args); err != nil {
			return "", nil, nil, fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	known := map[string]bool{}
	path = op.Path
	query = url.Values{}
	for _, p := range op.Params {
		known[p.Name] = true
		v, ok := args[p.Name]
		switch p.In {
		case "path":
			if !ok || v == nil {
				return "", nil, nil, fmt.Errorf("argument %q is required", p.Name)
			}
			path = strings.ReplaceAll(path, "{"+p.Name+"}", url.PathEscape(scalar(v)))
		case "query":
			if !ok || v == nil {
				continue
			}
			if list, isList := v.([]any); isList {
				for _, e := range list {
					query.Add(p.Name, scalar(e))
				}
				continue
			}
			query.Set(p.Name, scalar(v))
		}
	}
	if b, ok := args["body"]; ok && op.Body != nil && op.Body.JSON != nil {
		known["body"] = true
		if body, err = json.Marshal(b); err != nil {
			return "", nil, nil, err
		}
	}
	for k := range maps.Keys(args) {
		if !known[k] {
			return "", nil, nil, fmt.Errorf("unknown argument %q", k)
		}
	}
	return path, query, body, nil
}

// scalar formats a decoded JSON scalar for a path or query parameter.
func scalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	case bool:
		if x {
			return "true"
		}
		return "false"
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// toolResult maps a replayed response to a tool result: a 2xx JSON object
// as structuredContent and text, an error as isError with the API's code,
// message and fields. Every x-hello-secret value is withheld first.
func toolResult(res replay.Result, secrets []string) *sdk.CallToolResult {
	switch res.Fail {
	case "timeout":
		return toolError("timeout", "the operation did not finish within 30 s", nil)
	case "internal":
		return toolError("internal", "internal error", nil)
	case "too_large":
		return toolError("too_large", "the response is too large; narrow the request or page with limit/before", nil)
	}
	if res.Status >= 300 {
		return apiError(res)
	}
	if len(bytes.TrimSpace(res.Body)) == 0 {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("OK (HTTP %d, no content)", res.Status)}}}
	}
	if !isJSON(res.Header) {
		if len(secrets) > 0 {
			return toolError("internal", "a response with secrets was not JSON", nil)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: capText(string(res.Body))}}}
	}
	v, err := decode(res.Body)
	if err != nil {
		return toolError("internal", "the API answered malformed JSON", nil)
	}
	replay.Redact(v, secrets)
	text, _ := json.Marshal(v)
	out := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: capText(string(text))}}}
	if obj, ok := v.(map[string]any); ok {
		out.StructuredContent = obj
	}
	return out
}

// apiError is the isError result of a non-2xx response.
func apiError(res replay.Result) *sdk.CallToolResult {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Fields  any    `json:"fields"`
		} `json:"error"`
	}
	if json.Unmarshal(res.Body, &env) != nil || env.Error.Code == "" {
		return toolError(fmt.Sprintf("http_%d", res.Status), http.StatusText(res.Status), nil)
	}
	return toolError(env.Error.Code, env.Error.Message, env.Error.Fields)
}

// toolError is an isError result carrying code, message and fields as JSON
// text.
func toolError(code, msg string, fields any) *sdk.CallToolResult {
	e := map[string]any{"code": code, "message": msg}
	if fields != nil {
		e["fields"] = fields
	}
	b, _ := json.Marshal(map[string]any{"error": e})
	return &sdk.CallToolResult{IsError: true, Content: []sdk.Content{&sdk.TextContent{Text: string(b)}}}
}

func isJSON(h http.Header) bool {
	ct := h.Get("Content-Type")
	return strings.HasPrefix(ct, "application/json") || strings.Contains(ct, "+json")
}

func decode(b []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	return v, nil
}

// capText cuts text over 64 KiB at a rune boundary with a note on paging.
func capText(s string) string {
	if len(s) <= textCap {
		return s
	}
	cut := textCap
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + fmt.Sprintf("\n[truncated: the result is %d bytes; page with the operation's limit and before parameters, or narrow the request]", len(s))
}
