package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	// replayTimeout bounds one tool call (spec S-13).
	replayTimeout = 30 * time.Second
	// textCap is the largest text content a tool returns (spec S-15).
	textCap = 64 << 10
	// replayBodyCap bounds what a replayed response may buffer.
	replayBodyCap = 8 << 20
)

// errNestedReplay refuses a replay started from inside a replay.
var errNestedReplay = errors.New("a replayed request cannot start another replay")

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

// apiResult is a replayed response, or the failure that replaced it.
type apiResult struct {
	status int
	header http.Header
	body   []byte
	// fail is "timeout", "internal" or "too_large" when the handler did not
	// answer normally.
	fail string
}

// replay sends one request to the API handler in-process (spec S-15): only
// the caller's Authorization header is copied, and the replay marker rides
// in the context, where no external request can set it.
func (s *server) replay(ctx context.Context, c caller, method, path string, query url.Values, body []byte) (apiResult, error) {
	if _, nested := auth.ReplayFrom(ctx); nested {
		return apiResult{}, errNestedReplay
	}
	ctx, cancel := context.WithTimeout(auth.WithReplay(ctx, auth.Replay{ClientID: c.actor.ClientID}), replayTimeout)
	defer cancel()
	target := "http://hello-control" + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
	if err != nil {
		return apiResult{}, err
	}
	req.Header = http.Header{"Authorization": {c.authz}}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = c.remoteAddr
	rec := &recorder{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				s.log.Error("mcp replay panicked", "method", method, "path", path, "panic", fmt.Sprint(p))
				rec.fail("internal")
			}
		}()
		s.api.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		rec.fail("timeout")
	}
	return rec.result(), nil
}

// recorder buffers a replayed response; it is safe for a handler that keeps
// writing after a timeout has been answered.
type recorder struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	buf     bytes.Buffer
	failure string
	closed  bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = code
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.closed {
		return 0, http.ErrHandlerTimeout
	}
	if r.buf.Len()+len(p) > replayBodyCap {
		r.failure = "too_large"
		r.closed = true
		return 0, errors.New("mcp: replayed response too large")
	}
	return r.buf.Write(p)
}

func (r *recorder) fail(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure == "" {
		r.failure = reason
	}
	r.closed = true
}

func (r *recorder) result() apiResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return apiResult{status: status, header: r.header.Clone(), body: bytes.Clone(r.buf.Bytes()), fail: r.failure}
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
func toolResult(res apiResult, secrets []string) *sdk.CallToolResult {
	switch res.fail {
	case "timeout":
		return toolError("timeout", "the operation did not finish within 30 s", nil)
	case "internal":
		return toolError("internal", "internal error", nil)
	case "too_large":
		return toolError("too_large", "the response is too large; narrow the request or page with limit/before", nil)
	}
	if res.status >= 300 {
		return apiError(res)
	}
	if len(bytes.TrimSpace(res.body)) == 0 {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: fmt.Sprintf("OK (HTTP %d, no content)", res.status)}}}
	}
	if !isJSON(res.header) {
		if len(secrets) > 0 {
			return toolError("internal", "a response with secrets was not JSON", nil)
		}
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: capText(string(res.body))}}}
	}
	v, err := decode(res.body)
	if err != nil {
		return toolError("internal", "the API answered malformed JSON", nil)
	}
	redact(v, secrets)
	text, _ := json.Marshal(v)
	out := &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: capText(string(text))}}}
	if obj, ok := v.(map[string]any); ok {
		out.StructuredContent = obj
	}
	return out
}

// apiError is the isError result of a non-2xx response.
func apiError(res apiResult) *sdk.CallToolResult {
	var env struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Fields  any    `json:"fields"`
		} `json:"error"`
	}
	if json.Unmarshal(res.body, &env) != nil || env.Error.Code == "" {
		return toolError(fmt.Sprintf("http_%d", res.status), http.StatusText(res.status), nil)
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
