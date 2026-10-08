package auth

import (
	"encoding/hex"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestAgentIdentity (auth half) fails if a header or cookie can set the
// agent identity, if the agent holds more than read, if a deleted user is
// not refused with 401, if a demotion does not apply to the next request,
// or if the actor lacks the assistant's via.
func TestAgentIdentity(t *testing.T) {
	users := map[int64]Actor{1: {UserID: 1, Username: "ann", Role: RoleAdmin}}
	l := fakeLookup{users: users, tokens: map[string]Actor{}, sessions: map[string]Actor{}}
	var seen Actor
	h := Middleware(l, Options{Cookies: true}, slog.New(slog.DiscardHandler))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = ActorFrom(r.Context())
		w.WriteHeader(http.StatusTeapot)
	}))
	serve := func(req *http.Request) int {
		seen = Actor{}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec.Code
	}
	agentReq := func(id int64) *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
		return req.WithContext(WithAgent(req.Context(), Agent{UserID: id, TaskID: "t1"}))
	}

	t.Run("no header sets it", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
		for _, k := range []string{"X-Hello-Agent", "X-Hello-Agent-User", "Agent", "X-AI-Assistant", "Via", "X-Hello-Replay"} {
			req.Header.Set(k, "1")
		}
		if code := serve(req); code != http.StatusUnauthorized {
			t.Fatalf("request with agent-looking headers = %d, want 401", code)
		}
	})
	t.Run("read only, as the user, via the assistant", func(t *testing.T) {
		if code := serve(agentReq(1)); code != http.StatusTeapot {
			t.Fatalf("agent request = %d", code)
		}
		if seen.Kind != KindAgent || seen.UserID != 1 || seen.Username != "ann" || seen.Via != ViaAssistant {
			t.Errorf("actor = %+v", seen)
		}
		if len(seen.Scopes) != 1 || !seen.Scopes.Has(ScopeRead) || seen.Scopes.Has(ScopeWrite) || seen.Scopes.Has(ScopeSession) {
			t.Errorf("agent scopes = %v, want read only", seen.Scopes)
		}
	})
	t.Run("demotion applies", func(t *testing.T) {
		users[1] = Actor{UserID: 1, Username: "ann", Role: RoleViewer}
		serve(agentReq(1))
		if seen.Role != RoleViewer {
			t.Errorf("role after demotion = %q", seen.Role)
		}
	})
	t.Run("deleted user", func(t *testing.T) {
		if code := serve(agentReq(2)); code != http.StatusUnauthorized {
			t.Fatalf("agent for a deleted user = %d, want 401", code)
		}
	})
	t.Run("credentials win over the marker", func(t *testing.T) {
		req := agentReq(1)
		req.Header.Set("Authorization", "Bearer nope")
		if code := serve(req); code != http.StatusUnauthorized || seen.Kind == KindAgent {
			t.Fatalf("bad bearer with an agent marker = %d, %+v", code, seen)
		}
	})
	t.Run("via from the context", func(t *testing.T) {
		l.tokens[hexHash("tok")] = Actor{UserID: 1, Username: "ann", Role: RoleOperator, Kind: KindPersonalToken, Scopes: Scopes{ScopeWrite}}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/x", nil)
		req.Header.Set("Authorization", "Bearer tok")
		serve(req.WithContext(WithVia(req.Context(), "ai-proposal:7")))
		if seen.Via != "ai-proposal:7" || seen.Kind != KindPersonalToken {
			t.Errorf("actor = %+v", seen)
		}
	})
}

func hexHash(tok string) string { return hex.EncodeToString(HashToken(tok)) }
