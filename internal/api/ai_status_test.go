package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/aifake"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/config"
)

// aiUsers is a lookup with three session users: 7 operator, 8 operator,
// 9 admin, presented by the cookies "u7", "u8" and "u9".
func aiUsers() roleLookup {
	l := roleLookup{actors: map[string]*auth.Actor{}}
	for id, role := range map[int64]auth.Role{7: auth.RoleOperator, 8: auth.RoleOperator, 9: auth.RoleAdmin} {
		name := "u" + string(rune('0'+id))
		l.actors[string(auth.HashToken(name))] = &auth.Actor{UserID: id, Username: name, Role: role, Kind: auth.KindSession,
			Scopes: append(append(auth.Scopes{}, auth.AllScopes...), auth.ScopeSession)}
	}
	return l
}

func aiDo(t *testing.T, h http.Handler, user, method, path string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: user})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec.Code, body
}

func errCode(body map[string]any) string {
	e, _ := body["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

// TestAIEnablement (API half of spec S-1, S-23) fails if, with the agent
// unwired or off, an AI agent operation other than getAIStatus answers
// anything but 503 ai_disabled, getAIStatus is not 200 with the reason, or
// the status of an enabled agent carries the endpoint's path or the key.
func TestAIEnablement(t *testing.T) {
	off, err := ai.New(config.AIAgent{BaseURL: "http://10.0.0.1/v1"}, aifake.NewStore(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name   string
		svc    *ai.Service
		reason string
	}{{"unwired", nil, config.AIReasonNotConfigured}, {"incomplete", off, config.AIReasonIncompleteSetup}} {
		h := Handler(Config{Store: aiUsers(), AIAgent: c.svc})
		n := 0
		for _, rt := range Routes() {
			if !strings.HasPrefix(rt.Pattern, "/api/v1/ai/") || rt.Pattern == "/api/v1/ai/status" || rt.Pattern == "/api/v1/ai/settings" {
				continue
			}
			n++
			path := strings.NewReplacer("{id}", ai.NewID(), "{name}", "aiops").Replace(rt.Pattern)
			if code, body := aiDo(t, h, "u9", rt.Method, path); code != http.StatusServiceUnavailable || errCode(body) != ai.CodeDisabled {
				t.Errorf("%s: %s %s = %d %v, want 503 ai_disabled", c.name, rt.Method, rt.Pattern, code, body)
			}
		}
		if n < 17 {
			t.Errorf("only %d AI agent operations checked", n)
		}
		code, body := aiDo(t, h, "u7", "GET", "/api/v1/ai/status")
		if code != http.StatusOK || body["enabled"] != false || body["reason"] != c.reason {
			t.Errorf("%s: status = %d %v", c.name, code, body)
		}
	}

	cfg := aifake.Config()
	cfg.BaseURL, cfg.APIKey = "http://10.1.2.3:8000/secret-path/v1", "sk-NEVER-SHOWN-123"
	svc, _, st := aifake.Service(t, cfg)
	st.Counts = ai.Counts{Warning: 1, Critical: 1, HealthScore: 6, OpenProposals: 2}
	h := Handler(Config{Store: aiUsers(), AIAgent: svc})
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/api/v1/ai/status", nil)
	r.AddCookie(&http.Cookie{Name: auth.SessionCookie, Value: "u7"})
	h.ServeHTTP(rec, r)
	raw := rec.Body.String()
	if rec.Code != http.StatusOK || strings.Contains(raw, "sk-NEVER") || strings.Contains(raw, "secret-path") ||
		!strings.Contains(raw, `"endpointHost":"10.1.2.3:8000"`) || !strings.Contains(raw, `"healthScore":6`) || !strings.Contains(raw, `"enabled":true`) {
		t.Errorf("enabled status = %d %s", rec.Code, raw)
	}
}

type apiAgent struct{}

func (apiAgent) Name() string                            { return "aiops" }
func (apiAgent) Interval() time.Duration                 { return time.Minute }
func (apiAgent) Run(context.Context) (ai.Outcome, error) { return ai.OutcomeOK, nil }

// TestAIAgentsAndTasksAPI (spec S-15, S-17, S-23) fails if the agents list,
// run-now (202, 404 for an unknown agent) or getAITask ownership (404 to
// another non-admin user) misbehave.
func TestAIAgentsAndTasksAPI(t *testing.T) {
	svc, _, _ := aifake.Service(t, aifake.Config())
	svc.Scheduler.Register(apiAgent{})
	h := Handler(Config{Store: aiUsers(), AIAgent: svc})

	code, body := aiDo(t, h, "u8", "POST", "/api/v1/ai/agents/aiops/run")
	if code != http.StatusAccepted || body["agent"] != "aiops" {
		t.Errorf("run now = %d %v", code, body)
	}
	if code, _ := aiDo(t, h, "u8", "POST", "/api/v1/ai/agents/nope/run"); code != http.StatusNotFound {
		t.Errorf("run unknown = %d", code)
	}
	code, body = aiDo(t, h, "u7", "GET", "/api/v1/ai/agents")
	items, _ := body["items"].([]any)
	if code != http.StatusOK || len(items) != 1 || items[0].(map[string]any)["runRequested"] != true {
		t.Errorf("agents = %d %v", code, body)
	}

	done := make(chan struct{})
	id, err := svc.Tasks.Start(context.Background(), "message", 7, "", func(context.Context, string) (any, error) {
		defer close(done)
		return map[string]any{"messageId": 1}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	<-done
	time.Sleep(20 * time.Millisecond)
	if code, body := aiDo(t, h, "u7", "GET", "/api/v1/ai/tasks/"+id); code != http.StatusOK || body["id"] != id {
		t.Errorf("own task = %d %v", code, body)
	}
	if code, _ := aiDo(t, h, "u8", "GET", "/api/v1/ai/tasks/"+id); code != http.StatusNotFound {
		t.Errorf("another user's task = %d, want 404", code)
	}
	if code, _ := aiDo(t, h, "u9", "GET", "/api/v1/ai/tasks/"+id); code != http.StatusOK {
		t.Errorf("admin reading a task = %d", code)
	}
	if code, _ := aiDo(t, h, "u7", "GET", "/api/v1/ai/tasks/"+ai.NewID()); code != http.StatusNotFound {
		t.Errorf("missing task = %d", code)
	}
}
