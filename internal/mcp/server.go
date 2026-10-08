package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/version"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxBody is the largest /mcp request body read (spec S-13).
const maxBody = 1 << 20

// server is the /mcp handler: transport, bearer authentication, the step-up
// check, and one SDK server per visible tool set.
type server struct {
	api         http.Handler
	lookup      auth.Lookup
	tools       map[string]*tool
	order       []string
	ops         map[string]apispec.Operation
	resource    string
	metadataURL string
	cop         *http.CrossOriginProtection
	metrics     *Metrics
	log         *slog.Logger
	opts        *sdk.StreamableHTTPOptions

	mu       sync.Mutex
	handlers map[scopeKey]http.Handler
}

// scopeKey is what decides a caller's tool set: the scopes Has answers.
type scopeKey struct{ read, write, admin, secrets bool }

func keyOf(ss auth.Scopes) scopeKey {
	return scopeKey{ss.Has(auth.ScopeRead), ss.Has(auth.ScopeWrite), ss.Has(auth.ScopeAdmin), ss.Has(auth.ScopeSecrets)}
}

func (k scopeKey) scopes() auth.Scopes {
	var ss auth.Scopes
	for s, on := range map[auth.Scope]bool{auth.ScopeRead: k.read, auth.ScopeWrite: k.write, auth.ScopeAdmin: k.admin, auth.ScopeSecrets: k.secrets} {
		if on {
			ss = append(ss, s)
		}
	}
	return ss
}

// rpc is the part of a JSON-RPC message the transport inspects.
type rpc struct {
	Method string `json:"method"`
	Params struct {
		Name string `json:"name"`
	} `json:"params"`
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sw := &statusWriter{ResponseWriter: w}
	method := "other"
	defer func() { s.metrics.Requests.WithLabelValues(methodLabel(method), resultLabel(sw.code())).Inc() }()

	if _, nested := auth.ReplayFrom(r.Context()); nested {
		auth.WriteError(sw, http.StatusForbidden, "forbidden", "a replayed request cannot reach /mcp")
		return
	}
	if err := s.cop.Check(r); err != nil {
		auth.WriteError(sw, http.StatusForbidden, "forbidden_origin", "cross-origin request refused")
		return
	}
	a, authz, ok := s.authenticate(sw, r)
	if !ok {
		return
	}
	if r.Method == http.MethodPost {
		body, err := io.ReadAll(http.MaxBytesReader(sw, r.Body, maxBody))
		if err != nil {
			var tooBig *http.MaxBytesError
			if errors.As(err, &tooBig) {
				auth.WriteError(sw, http.StatusRequestEntityTooLarge, "too_large", "request body over 1 MiB")
				return
			}
			auth.WriteError(sw, http.StatusBadRequest, "bad_request", "unreadable request body")
			return
		}
		msgs := parseRPC(body)
		if len(msgs) > 0 {
			method = msgs[0].Method
		}
		for _, m := range msgs {
			if need, deny := s.stepUp(a.Scopes, m); deny {
				s.challenge(sw, http.StatusForbidden, fmt.Sprintf(`error="insufficient_scope", scope="%s"`, need), "insufficient_scope", "this call needs scope "+string(need))
				return
			}
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
		r.ContentLength = int64(len(body))
	}
	ctx := withCaller(r.Context(), caller{authz: authz, actor: a, remoteAddr: r.RemoteAddr})
	s.handlerFor(a.Scopes).ServeHTTP(sw, r.WithContext(ctx))
}

// authenticate admits a bearer credential only (spec S-13): a session
// cookie is never consulted, and an OAuth access token must be bound to
// the MCP resource.
func (s *server) authenticate(w http.ResponseWriter, r *http.Request) (auth.Actor, string, bool) {
	h := r.Header.Get("Authorization")
	tok, ok := strings.CutPrefix(h, "Bearer ")
	if !ok || tok == "" {
		s.challenge(w, http.StatusUnauthorized, `scope="read"`, "unauthorized", "authentication required")
		return auth.Actor{}, "", false
	}
	a, err := s.lookup.TokenActor(r.Context(), auth.HashToken(tok))
	switch {
	case errors.Is(err, auth.ErrNoCredentials):
		s.challenge(w, http.StatusUnauthorized, `error="invalid_token", scope="read"`, "unauthorized", "authentication required")
		return auth.Actor{}, "", false
	case err != nil:
		s.log.Error("mcp: authenticate request", "error", err)
		auth.WriteError(w, http.StatusServiceUnavailable, "unavailable", "credential store unavailable")
		return auth.Actor{}, "", false
	}
	if a.Kind == "" {
		a.Kind = auth.KindLegacyToken
	}
	if a.Kind == auth.KindLegacyToken && a.Scopes == nil {
		a.Scopes = slices.Clone(auth.AllScopes)
	}
	if a.Kind == auth.KindSession || (a.Kind == auth.KindOAuth && !inAudience(a.Audience, s.resource)) {
		s.challenge(w, http.StatusUnauthorized, `error="invalid_token", scope="read"`, "invalid_token", "token not valid for this resource")
		return auth.Actor{}, "", false
	}
	return a, h, true
}

// inAudience compares resource identifiers after trimming a trailing slash
// and lower-casing scheme and host.
func inAudience(aud []string, resource string) bool {
	norm := func(s string) string {
		s = strings.TrimSuffix(s, "/")
		if i := strings.Index(s, "://"); i >= 0 {
			rest := s[i+3:]
			j := strings.IndexByte(rest, '/')
			if j < 0 {
				j = len(rest)
			}
			s = strings.ToLower(s[:i+3]+rest[:j]) + rest[j:]
		}
		return s
	}
	want := norm(resource)
	return slices.ContainsFunc(aud, func(a string) bool { return norm(a) == want })
}

// stepUp reports the scope a message needs that the caller lacks: a known
// tool beyond the caller's scopes, or a resource read without read.
func (s *server) stepUp(scopes auth.Scopes, m rpc) (auth.Scope, bool) {
	switch m.Method {
	case "tools/call":
		if t, ok := s.tools[m.Params.Name]; ok && !allowed(scopes, t.op.Scope) {
			return t.op.Scope, true
		}
	case "resources/read", "resources/list", "resources/templates/list":
		if !scopes.Has(auth.ScopeRead) {
			return auth.ScopeRead, true
		}
	}
	return "", false
}

// challenge answers status with the WWW-Authenticate challenge of spec S-7.
func (s *server) challenge(w http.ResponseWriter, status int, params, code, msg string) {
	v := "Bearer " + params
	if s.metadataURL != "" {
		v += fmt.Sprintf(`, resource_metadata="%s"`, s.metadataURL)
	}
	w.Header().Set("WWW-Authenticate", v)
	auth.WriteError(w, status, code, msg)
}

// parseRPC decodes a JSON-RPC message or batch; anything else is left for
// the SDK to reject.
func parseRPC(body []byte) []rpc {
	body = bytes.TrimSpace(body)
	if len(body) > 0 && body[0] == '[' {
		var ms []rpc
		_ = json.Unmarshal(body, &ms)
		return ms
	}
	var m rpc
	if json.Unmarshal(body, &m) != nil {
		return nil
	}
	return []rpc{m}
}

// handlerFor returns the SDK handler serving the tools scopes allow,
// building it once per tool set.
func (s *server) handlerFor(scopes auth.Scopes) http.Handler {
	k := keyOf(scopes)
	s.mu.Lock()
	defer s.mu.Unlock()
	if h, ok := s.handlers[k]; ok {
		return h
	}
	srv := s.sdkServer(k.scopes())
	h := sdk.NewStreamableHTTPHandler(func(*http.Request) *sdk.Server { return srv }, s.opts)
	s.handlers[k] = h
	return h
}

// instructions name the skills and resources (spec S-13).
const instructions = `Hello is a PBX. Its tools are the Hello API's operations, named by operationId; each needs the scope its description names. ` +
	`Show-once secrets (device SIP passwords, provisioning URLs and tokens, API secrets) are withheld: the administrator reads them in the Hello console. ` +
	`Resources: hello://calls, hello://registrations, hello://cdrs/recent, hello://trunks/status, hello://cluster, and the templates hello://cdrs/{id}, hello://extensions/{id}, hello://diagnostics/devices/{id}. ` +
	`Skills: hello-setup (extensions, devices, voicemail, phones), hello-routing (trunks, routes, ring groups, feature codes), hello-troubleshoot (failed calls, registration, trunks, cluster); download them from GET /api/v1/skills.`

// sdkServer is an SDK server with the tools scopes allow, the resources
// when they allow read, and the prompts.
func (s *server) sdkServer(scopes auth.Scopes) *sdk.Server {
	srv := sdk.NewServer(&sdk.Implementation{Name: "hello", Title: "Hello PBX", Version: version.Version},
		&sdk.ServerOptions{Instructions: instructions})
	for _, id := range s.order {
		t := s.tools[id]
		if !allowed(scopes, t.op.Scope) {
			continue
		}
		srv.AddTool(t.tool, s.toolHandler(t))
	}
	if scopes.Has(auth.ScopeRead) {
		s.addResources(srv)
	}
	addPrompts(srv)
	return srv
}

// toolHandler replays a call of t as the caller and logs and counts it
// (spec S-15, S-20): tool, actor, client, status and duration, never the
// arguments or the result.
func (s *server) toolHandler(t *tool) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		start := time.Now()
		c, ok := callerFrom(ctx)
		if !ok {
			return nil, errors.New("mcp: tool call without an authenticated caller")
		}
		status, res := 0, (*sdk.CallToolResult)(nil)
		path, query, body, err := request(t.op, req.Params.Arguments)
		if err != nil {
			status, res = http.StatusBadRequest, toolError("bad_request", err.Error(), nil)
		} else {
			r, rerr := s.replay(ctx, c, t.op.Method, path, query, body)
			if rerr != nil {
				return nil, rerr
			}
			status, res = r.status, toolResult(r, t.op.Secrets)
			if r.fail != "" {
				status = 0
			}
		}
		d := time.Since(start)
		result := "ok"
		if res.IsError {
			result = "error"
		}
		s.metrics.ToolCalls.WithLabelValues(t.op.ID, result).Inc()
		s.metrics.ToolSeconds.Observe(d.Seconds())
		s.log.Info("mcp tool call", "tool", t.op.ID, "actor", c.actor.String(), "client", c.actor.ClientID,
			"status", status, "duration", d)
		return res, nil
	}
}

// statusWriter records the status written through it.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(p)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func (w *statusWriter) code() int {
	if w.status == 0 {
		return http.StatusOK
	}
	return w.status
}
