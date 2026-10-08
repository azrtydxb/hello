package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// TestTokenLifecycle (credential half) fails if a new credential lacks its
// prefix or 32 random bytes, or its stored form is anything but the
// SHA-256 of the whole credential. (Expiry, revocation, legacy tokens and
// audit via are the store and API halves.)
func TestTokenLifecycle(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range []string{PrefixPersonal, PrefixAccess, PrefixRefresh, PrefixClientSecret} {
		plain, hash := NewPrefixed(p)
		rest, ok := strings.CutPrefix(plain, p)
		if !ok {
			t.Fatalf("%q lacks prefix %s", plain, p)
		}
		if raw, err := base64.RawURLEncoding.DecodeString(rest); err != nil || len(raw) != 32 {
			t.Fatalf("%q is not the prefix and 32 random bytes", plain)
		}
		if want := sha256.Sum256([]byte(plain)); !bytes.Equal(hash, want[:]) || bytes.Contains(hash, []byte(rest)) {
			t.Fatalf("stored form of %s is not sha256 of the credential", p)
		}
		if seen[plain] {
			t.Fatal("credential repeated")
		}
		seen[plain] = true
	}
}

// TestEnforcement fails if Require admits a role below the route's or a
// credential without the route's scope, if the 403 lacks the step-up
// challenge, if a token keeps a stored session scope, or if an OAuth token
// is admitted outside its audience (an MCP-bound token directly on the
// API, or inside another client's replay) or refused inside its own replay.
func TestEnforcement(t *testing.T) {
	const (
		apiRes = "https://hello.example/api/v1"
		mcpRes = "https://hello.example/mcp"
		meta   = "https://hello.example/.well-known/oauth-protected-resource/api/v1"
	)
	tok := func(a Actor) (string, Actor) { return "tok-" + a.Username, a }
	actors := map[string]Actor{}
	add := func(a Actor) string {
		k, v := tok(a)
		actors[hex.EncodeToString(HashToken(k))] = v
		return k
	}
	viewer := add(Actor{UserID: 1, Username: "viewer", Role: RoleViewer, Kind: KindPersonalToken, Scopes: Scopes{ScopeRead, ScopeWrite}})
	writer := add(Actor{UserID: 2, Username: "writer", Role: RoleOperator, Kind: KindPersonalToken, Scopes: Scopes{ScopeWrite}})
	sneaky := add(Actor{UserID: 3, Username: "sneaky", Role: RoleAdmin, Kind: KindPersonalToken, Scopes: Scopes{ScopeRead, ScopeSession}})
	apiOnly := add(Actor{UserID: 4, Username: "api", Role: RoleAdmin, Kind: KindOAuth, Scopes: Scopes{ScopeRead}, ClientID: "c1", Audience: []string{apiRes}})
	mcpOnly := add(Actor{UserID: 5, Username: "mcp", Role: RoleAdmin, Kind: KindOAuth, Scopes: Scopes{ScopeRead}, ClientID: "c1", Audience: []string{mcpRes}})
	service := add(Actor{Username: "", ServiceName: "ci", Role: RoleOperator, Kind: KindService, Scopes: Scopes{ScopeWrite}, ClientID: "hello_sa_x", Audience: []string{apiRes, mcpRes}})
	l := fakeLookup{tokens: actors, sessions: map[string]Actor{}}

	serve := func(o Options, token string, role Role, scope Scope, replay *Replay) *httptest.ResponseRecorder {
		h := Middleware(l, o, slog.New(slog.DiscardHandler))(Require(role, scope)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTeapot)
		})))
		req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		if replay != nil {
			req = req.WithContext(WithReplay(req.Context(), *replay))
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	o := Options{Resource: apiRes, MetadataURL: meta, Cookies: true}
	cases := []struct {
		name   string
		token  string
		role   Role
		scope  Scope
		replay *Replay
		want   int
		code   string
	}{
		{"viewer reads", viewer, RoleViewer, ScopeRead, nil, 418, ""},
		{"viewer below operator", viewer, RoleOperator, ScopeWrite, nil, 403, "forbidden_role"},
		{"write implies read", writer, RoleViewer, ScopeRead, nil, 418, ""},
		{"write lacks admin", writer, RoleViewer, ScopeAdmin, nil, 403, "insufficient_scope"},
		{"write lacks secrets", writer, RoleViewer, ScopeSecrets, nil, 403, "insufficient_scope"},
		{"stored session scope dropped", sneaky, RoleViewer, ScopeSession, nil, 403, "insufficient_scope"},
		{"api token on api", apiOnly, RoleViewer, ScopeRead, nil, 418, ""},
		{"mcp token on api", mcpOnly, RoleViewer, ScopeRead, nil, 401, "invalid_token"},
		{"mcp token in its replay", mcpOnly, RoleViewer, ScopeRead, &Replay{ClientID: "c1"}, 418, ""},
		{"mcp token in another replay", mcpOnly, RoleViewer, ScopeRead, &Replay{ClientID: "c2"}, 401, "invalid_token"},
		{"service account role", service, RoleOperator, ScopeWrite, nil, 418, ""},
		{"service account below admin", service, RoleAdmin, ScopeAdmin, nil, 403, "forbidden_role"},
	}
	for _, c := range cases {
		rec := serve(o, c.token, c.role, c.scope, c.replay)
		if rec.Code != c.want || (c.code != "" && !strings.Contains(rec.Body.String(), `"`+c.code+`"`)) {
			t.Errorf("%s: %d %s, want %d %s", c.name, rec.Code, rec.Body, c.want, c.code)
		}
		if c.code == "insufficient_scope" {
			ch := rec.Header().Get("WWW-Authenticate")
			if !strings.Contains(ch, `error="insufficient_scope"`) || !strings.Contains(ch, `scope="`+string(c.scope)+`"`) ||
				!strings.Contains(ch, `resource_metadata="`+meta+`"`) {
				t.Errorf("%s: challenge %q", c.name, ch)
			}
		}
		if c.code == "invalid_token" && !strings.Contains(rec.Header().Get("WWW-Authenticate"), `resource_metadata="`+meta+`"`) {
			t.Errorf("%s: 401 without resource_metadata: %q", c.name, rec.Header().Get("WWW-Authenticate"))
		}
	}
	// Without HELLO_PUBLIC_URL no OAuth token is admitted and 401s carry
	// no challenge, as before.
	if rec := serve(Options{Cookies: true}, apiOnly, RoleViewer, ScopeRead, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("OAuth token without a resource = %d", rec.Code)
	}
	if rec := serve(Options{Cookies: true}, "nope", RoleViewer, ScopeRead, nil); rec.Header().Get("WWW-Authenticate") != "" {
		t.Fatalf("challenge without metadata: %q", rec.Header().Get("WWW-Authenticate"))
	}
	if rec := serve(o, "nope", RoleViewer, ScopeRead, nil); !strings.Contains(rec.Header().Get("WWW-Authenticate"), `scope="read"`) {
		t.Fatalf("401 challenge = %q", rec.Header().Get("WWW-Authenticate"))
	}
	// Require outside Middleware refuses.
	rec := httptest.NewRecorder()
	Require(RoleViewer, ScopeRead)(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil).WithContext(context.Background()))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Require without an actor = %d", rec.Code)
	}
	if !slices.Contains(GrantableScopes(RoleAdmin), ScopeSecrets) || GrantableScopes(RoleViewer).Has(ScopeWrite) ||
		GrantableScopes(RoleOperator).Has(ScopeAdmin) || GrantableScopes(RoleOperator).Has(ScopeSecrets) {
		t.Fatal("GrantableScopes drifted from spec S-23")
	}
}
