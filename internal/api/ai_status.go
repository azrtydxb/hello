package api

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/config"
)

// aiAlwaysOn are the /api/v1/ai/ routes that answer while the in-product
// agent is off: its status (which says why) and phase 1's access settings.
var aiAlwaysOn = map[string]bool{"/api/v1/ai/status": true, "/api/v1/ai/settings": true}

// aiGuard makes every in-product AI operation but the always-on ones answer
// 503 ai_disabled while the agent is off (spec S-1).
func (s *server) aiGuard(pattern string, h http.HandlerFunc) http.Handler {
	if !strings.HasPrefix(pattern, "/api/v1/ai/") || aiAlwaysOn[pattern] {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.aiOn() {
			writeError(w, http.StatusServiceUnavailable, ai.CodeDisabled, "the AI agent is off; GET /api/v1/ai/status says why")
			return
		}
		h(w, r)
	})
}

func (s *server) aiOn() bool {
	if s.AIAgent == nil {
		return false
	}
	on, _ := s.AIAgent.Enabled()
	return on
}

// agentError answers an AI agent failure with its status and code: 503 ai_disabled,
// 429 ai_busy or ai_budget_exhausted, 409 task_running, 404 for a missing
// row, 502 for the model's failures (with the coded message, which holds no
// prompt or response).
func (s *server) agentError(w http.ResponseWriter, what string, err error) {
	var e *ai.Error
	switch {
	case errors.Is(err, ai.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "not found")
	case !errors.As(err, &e):
		s.internal(w, what, err)
	case e.Code == ai.CodeDisabled:
		writeError(w, http.StatusServiceUnavailable, e.Code, e.Error())
	case e.Code == ai.CodeBusy || e.Code == ai.CodeBudgetExhausted:
		writeError(w, http.StatusTooManyRequests, e.Code, e.Error())
	case e.Code == ai.CodeTaskRunning:
		writeError(w, http.StatusConflict, e.Code, e.Error())
	default:
		writeError(w, http.StatusBadGateway, e.Code, e.Error())
	}
}

type aiAgentRunJSON struct {
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Outcome    *string    `json:"outcome"`
	Tokens     int64      `json:"tokens"`
	Error      *string    `json:"error"`
}

type aiAgentJSON struct {
	Name            string          `json:"name"`
	IntervalSeconds int64           `json:"intervalSeconds"`
	LastRun         *aiAgentRunJSON `json:"lastRun"`
	NextDueAt       *time.Time      `json:"nextDueAt"`
	RunRequested    bool            `json:"runRequested"`
}

func nonEmpty(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

func agentsJSON(in []ai.AgentInfo) []aiAgentJSON {
	out := make([]aiAgentJSON, 0, len(in))
	for _, a := range in {
		j := aiAgentJSON{Name: a.Name, IntervalSeconds: int64(a.Interval / time.Second), NextDueAt: a.NextDueAt, RunRequested: a.RunRequested}
		if r := a.LastRun; r != nil {
			j.LastRun = &aiAgentRunJSON{StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, Outcome: nonEmpty(string(r.Outcome)),
				Tokens: r.Tokens, Error: nonEmpty(r.Error)}
		}
		out = append(out, j)
	}
	return out
}

// getAIStatus answers 200 whether or not the agent is on (spec S-23); it
// never carries the endpoint's path or the API key.
func (s *server) getAIStatus(w http.ResponseWriter, r *http.Request) {
	st := ai.Status{Reason: config.AIReasonNotConfigured, Counts: ai.Counts{HealthScore: 10}}
	if s.AIAgent != nil {
		var err error
		if st, err = s.AIAgent.Status(r.Context()); err != nil {
			s.internal(w, "ai status", err)
			return
		}
		if !st.Enabled {
			st.Counts.HealthScore = 10
		}
	}
	errs := make([]map[string]any, 0, len(st.RecentErrors))
	for _, e := range st.RecentErrors {
		errs = append(errs, map[string]any{"code": e.Code, "at": e.At})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled": st.Enabled, "reason": nonEmpty(st.Reason), "provider": st.Provider, "model": st.Model,
		"endpointHost": st.EndpointHost, "endpointPrivate": st.EndpointPrivate, "structuredOutput": st.StructuredOutput,
		"tokensToday": st.TokensToday, "dailyTokenBudget": st.DailyTokenBudget, "backgroundTokenLimit": st.BackgroundTokenLimit,
		"concurrencyInUse": st.ConcurrencyInUse, "maxConcurrency": st.MaxConcurrency,
		"agents": agentsJSON(st.Agents), "recentErrors": errs,
		"openFindings": map[string]int{"info": st.Counts.Info, "warning": st.Counts.Warning, "critical": st.Counts.Critical},
		"healthScore":  st.Counts.HealthScore, "openProposals": st.Counts.OpenProposals,
	})
}

// listAIAgents lists the background agents with their last run.
func (s *server) listAIAgents(w http.ResponseWriter, r *http.Request) {
	as, err := s.AIAgent.Scheduler.Agents(r.Context())
	if err != nil {
		s.internal(w, "list ai agents", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": agentsJSON(as)})
}

// runAIAgent records a run-now request that the next scheduler tick on any
// replica runs once (spec S-17).
func (s *server) runAIAgent(w http.ResponseWriter, r *http.Request) {
	a, _ := auth.ActorFrom(r.Context())
	name := r.PathValue("name")
	at, err := s.AIAgent.Scheduler.RequestRun(r.Context(), name, a.UserID)
	if errors.Is(err, ai.ErrUnknownAgent) {
		writeError(w, http.StatusNotFound, "not_found", "no such agent")
		return
	}
	if err != nil {
		s.internal(w, "request ai agent run", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"agent": name, "requestedAt": at})
}

// getAITask returns a task to its requester or an admin, 404 to anyone
// else (spec S-15).
func (s *server) getAITask(w http.ResponseWriter, r *http.Request) {
	a, _ := auth.ActorFrom(r.Context())
	t, err := s.AIAgent.Tasks.Get(r.Context(), r.PathValue("id"), a.UserID, a.Role == auth.RoleAdmin)
	if err != nil {
		s.agentError(w, "get ai task", err)
		return
	}
	var result any
	if len(t.Result) > 0 {
		result = t.Result
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": t.ID, "kind": t.Kind, "status": t.Status, "sessionId": nonEmpty(t.SessionID),
		"errorCode": nonEmpty(t.ErrorCode), "errorMessage": nonEmpty(t.ErrorMessage), "result": result,
		"createdAt": t.CreatedAt, "startedAt": t.StartedAt, "finishedAt": t.FinishedAt,
	})
}
