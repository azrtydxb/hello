package api

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// scopeStore resolves the tokens TestScopeEnforcement makes; every other
// store method panics (stubStore), which the test reads as "admitted".
type scopeStore struct {
	stubStore
	tokens map[string]auth.Actor
}

func (s scopeStore) TokenActor(_ context.Context, h []byte) (auth.Actor, error) {
	if a, ok := s.tokens[hex.EncodeToString(h)]; ok {
		return a, nil
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

func (s scopeStore) SessionActor(_ context.Context, h []byte) (auth.Actor, error) {
	if hex.EncodeToString(h) == hex.EncodeToString(auth.HashToken("sess")) {
		return auth.Actor{UserID: 1, Username: "alice", Role: auth.RoleAdmin}, nil
	}
	return auth.Actor{}, auth.ErrNoCredentials
}

// below is the strongest scope set that does not grant s.
func below(s auth.Scope) auth.Scopes {
	switch s {
	case auth.ScopeRead:
		return auth.Scopes{}
	case auth.ScopeWrite:
		return auth.Scopes{auth.ScopeRead, auth.ScopeSecrets}
	case auth.ScopeAdmin:
		return auth.Scopes{auth.ScopeWrite, auth.ScopeSecrets}
	case auth.ScopeSecrets:
		return auth.Scopes{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin}
	}
	return slices.Clone(auth.AllScopes) // session: every scope a token can hold
}

var concreteParams = strings.NewReplacer("{id}", "1", "{vendor}", "snom", "{secretId}", "1",
	"{ip}", "192.0.2.1", "{name}", "hello-setup")

// TestScopeEnforcement fails, for any route, if a token whose scopes are
// just below the route's is admitted, if the route's own scope (or a
// higher one) is refused, if the 403 lacks the insufficient_scope
// challenge, if a bearer token can call a session operation, or if a
// session or legacy token is refused anything else.
func TestScopeEnforcement(t *testing.T) {
	const meta = "https://hello.example/.well-known/oauth-protected-resource/api/v1"
	st := scopeStore{tokens: map[string]auth.Actor{}}
	token := func(name string, kind auth.Kind, scopes auth.Scopes) string {
		st.tokens[hex.EncodeToString(auth.HashToken(name))] = auth.Actor{
			UserID: 1, Username: "alice", Role: auth.RoleAdmin, Kind: kind, Scopes: scopes,
			ClientID: "c", Audience: []string{"https://hello.example/api/v1"},
		}
		return name
	}
	for _, s := range []auth.Scope{auth.ScopeRead, auth.ScopeWrite, auth.ScopeAdmin, auth.ScopeSecrets, auth.ScopeSession} {
		token("below-"+string(s), auth.KindOAuth, below(s))
		token("exact-"+string(s), auth.KindOAuth, auth.Scopes{s})
	}
	token("admin", auth.KindOAuth, auth.Scopes{auth.ScopeAdmin})
	legacy := token("legacy", auth.KindLegacyToken, nil)
	h := Handler(Config{Store: st, AI: fakeAI{}})

	// call reports the status, or 0 when the handler ran into the stub
	// store (the request was admitted).
	call := func(method, path string, set func(*http.Request)) (code int, rec *httptest.ResponseRecorder) {
		req := httptest.NewRequest(method, concreteParams.Replace(path), strings.NewReader("{}"))
		set(req)
		rec = httptest.NewRecorder()
		defer func() {
			if recover() != nil {
				code = 0
			}
		}()
		h.ServeHTTP(rec, req)
		return rec.Code, rec
	}
	bearer := func(tok string) func(*http.Request) {
		return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+tok) }
	}
	refused := func(code int) bool { return code == http.StatusUnauthorized || code == http.StatusForbidden }

	n := 0
	for _, rt := range Routes() {
		if rt.Public {
			continue
		}
		n++
		op := rt.Method + " " + rt.Pattern
		code, rec := call(rt.Method, rt.Pattern, bearer("below-"+string(rt.Scope)))
		if code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "insufficient_scope") {
			t.Errorf("%s: a token below %s = %d %s, want 403 insufficient_scope", op, rt.Scope, code, rec.Body)
		} else if ch := rec.Header().Get("WWW-Authenticate"); !strings.Contains(ch, `error="insufficient_scope"`) ||
			!strings.Contains(ch, `scope="`+string(rt.Scope)+`"`) || !strings.Contains(ch, `resource_metadata="`+meta+`"`) {
			t.Errorf("%s: challenge %q", op, ch)
		}
		session := rt.Scope == auth.ScopeSession
		if code, rec := call(rt.Method, rt.Pattern, bearer("exact-"+string(rt.Scope))); refused(code) != session {
			t.Errorf("%s: a token with exactly %s = %d %s", op, rt.Scope, code, rec.Body)
		}
		if rt.Scope == auth.ScopeRead || rt.Scope == auth.ScopeWrite {
			if code, rec := call(rt.Method, rt.Pattern, bearer("admin")); refused(code) {
				t.Errorf("%s: an admin-scoped token = %d %s", op, code, rec.Body)
			}
		}
		if code, rec := call(rt.Method, rt.Pattern, bearer(legacy)); refused(code) != session {
			t.Errorf("%s: a legacy token = %d %s", op, code, rec.Body)
		}
		if code, rec := call(rt.Method, rt.Pattern, func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "sess"})
		}); refused(code) {
			t.Errorf("%s: a session = %d %s", op, code, rec.Body)
		}
	}
	if n < 100 {
		t.Fatalf("only %d protected routes", n)
	}
}

// fakeAI gives the middleware the API resource and its metadata URL;
// every other method panics (read as "admitted").
type fakeAI struct{ AIAccess }

func (fakeAI) Resources() (string, string) {
	return "https://hello.example/api/v1", "https://hello.example/mcp"
}
func (fakeAI) MetadataURL(r string) string {
	return "https://hello.example/.well-known/oauth-protected-resource" + strings.TrimPrefix(r, "https://hello.example")
}
