// Package aifake is test support for the AI packages (plan ai-agent,
// Constraints): an in-memory ai.Store, a Service on go-ai-sdk's aitest mock
// model, and builders for scripted model responses.
package aifake

import (
	"context"
	"encoding/json"
	"log/slog"
	"maps"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/go-ai-sdk/ai/aitest"
	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/config"
	"github.com/prometheus/client_golang/prometheus"
)

// Config is the configuration of a test service: the spec's defaults with
// an endpoint and model set.
func Config() config.AIAgent {
	return config.AIAgent{
		Provider: "openai", BaseURL: "http://127.0.0.1:1/v1", Model: "test-model", StructuredOutput: "json_schema",
		ValidationAttempts: 3, MaxSteps: 8, MaxConcurrency: 2, RequestsPerMinute: 30, Timeout: 30 * time.Second,
		DailyTokenBudget: 2_000_000, BackgroundBudgetPercent: 80, AgentStartDelay: 2 * time.Minute,
		AIOpsInterval: time.Minute, ExplainMinInterval: 10 * time.Minute,
	}
}

// Service returns an enabled service on a mock model that answers with
// responses in order, an in-memory store and a fresh registry.
func Service(t testing.TB, cfg config.AIAgent, responses ...*provider.Response) (*ai.Service, *aitest.MockModel, *Store) {
	t.Helper()
	m := &aitest.MockModel{Responses: responses, Caps: provider.Capabilities{NativeJSON: true}}
	st := NewStore()
	s, err := ai.NewWith(cfg, st, nil, prometheus.NewRegistry(), slog.New(slog.DiscardHandler), ai.Options{Replica: "test", Model: m})
	if err != nil {
		t.Fatal(err)
	}
	return s, m, st
}

// Text is a model response whose text is s, finished with stop.
func Text(s string) *provider.Response {
	return &provider.Response{Content: []provider.ContentPart{provider.TextPart{Text: s}}, FinishReason: provider.FinishStop,
		Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}
}

// JSON is a response whose text is v as JSON.
func JSON(v any) *provider.Response {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return Text(string(b))
}

// ToolCall is a response calling tool name with args.
func ToolCall(id, name string, args any) *provider.Response {
	b, err := json.Marshal(args)
	if err != nil {
		panic(err)
	}
	return &provider.Response{Content: []provider.ContentPart{provider.ToolCallPart{ID: id, Name: name, Args: b}},
		FinishReason: provider.FinishToolCalls, Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, TotalTokens: 15}}
}

// Store is an in-memory ai.Store. Its lock is process-local.
type Store struct {
	mu       sync.Mutex
	Usage    map[string]ai.Usage
	Tasks    map[string]ai.Task
	Runs     []ai.AgentRun
	Requests map[string]time.Time
	locks    map[string]bool
	Pruned   int
	Counts   ai.Counts
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{Usage: map[string]ai.Usage{}, Tasks: map[string]ai.Task{}, Requests: map[string]time.Time{}, locks: map[string]bool{}}
}

var _ ai.Store = (*Store)(nil)

func key(day time.Time) string { return day.UTC().Format(time.DateOnly) }

// AddAIUsage implements ai.Store.
func (s *Store) AddAIUsage(_ context.Context, day time.Time, u ai.Usage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := s.Usage[key(day)]
	s.Usage[key(day)] = ai.Usage{InputTokens: o.InputTokens + u.InputTokens, OutputTokens: o.OutputTokens + u.OutputTokens,
		ReasoningTokens: o.ReasoningTokens + u.ReasoningTokens, Calls: o.Calls + u.Calls}
	return nil
}

// AIUsage implements ai.Store.
func (s *Store) AIUsage(_ context.Context, day time.Time) (ai.Usage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Usage[key(day)], nil
}

// SetUsage sets day's usage.
func (s *Store) SetUsage(day time.Time, u ai.Usage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Usage[key(day)] = u
}

// CreateAITask implements ai.Store.
func (s *Store) CreateAITask(_ context.Context, t ai.Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.SessionID != "" {
		for _, o := range s.Tasks {
			if o.SessionID == t.SessionID && (o.Status == ai.TaskQueued || o.Status == ai.TaskRunning) {
				return ai.ErrTaskRunning
			}
		}
	}
	t.Status = ai.TaskQueued
	s.Tasks[t.ID] = t
	return nil
}

// StartAITask implements ai.Store.
func (s *Store) StartAITask(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.Tasks[id]; ok && t.Status == ai.TaskQueued {
		t.Status, t.StartedAt = ai.TaskRunning, &at
		s.Tasks[id] = t
	}
	return nil
}

// FinishAITask implements ai.Store.
func (s *Store) FinishAITask(_ context.Context, id string, status ai.TaskStatus, code, message string, result json.RawMessage, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.Tasks[id]; ok && (t.Status == ai.TaskQueued || t.Status == ai.TaskRunning) {
		t.Status, t.ErrorCode, t.ErrorMessage, t.Result, t.FinishedAt = status, code, message, result, &at
		s.Tasks[id] = t
	}
	return nil
}

// AITask implements ai.Store.
func (s *Store) AITask(_ context.Context, id string) (ai.Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.Tasks[id]
	if !ok {
		return ai.Task{}, ai.ErrNotFound
	}
	return t, nil
}

// AITaskReplicas implements ai.Store.
func (s *Store) AITaskReplicas(_ context.Context, before time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	set := map[string]bool{}
	for _, t := range s.Tasks {
		if (t.Status == ai.TaskQueued || t.Status == ai.TaskRunning) && t.CreatedAt.Before(before) {
			set[t.Replica] = true
		}
	}
	out := sortedKeys(set)
	return out, nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range maps.Keys(m) {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// FailAITasks implements ai.Store.
func (s *Store) FailAITasks(_ context.Context, replica, code, message string, at time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, t := range s.Tasks {
		if t.Replica == replica && (t.Status == ai.TaskQueued || t.Status == ai.TaskRunning) {
			t.Status, t.ErrorCode, t.ErrorMessage, t.FinishedAt = ai.TaskFailed, code, message, &at
			s.Tasks[id] = t
			n++
		}
	}
	return n, nil
}

// TryAILock implements ai.Store.
func (s *Store) TryAILock(_ context.Context, k string) (func(), bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks[k] {
		return nil, false, nil
	}
	s.locks[k] = true
	return func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.locks, k)
	}, true, nil
}

// LastAgentRun implements ai.Store.
func (s *Store) LastAgentRun(_ context.Context, agent string) (*ai.AgentRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.Runs) - 1; i >= 0; i-- {
		if s.Runs[i].Agent == agent {
			r := s.Runs[i]
			return &r, nil
		}
	}
	return nil, nil
}

// AgentRunRequested implements ai.Store.
func (s *Store) AgentRunRequested(_ context.Context, agent string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Requests[agent]
	return ok, nil
}

// RequestAgentRun implements ai.Store.
func (s *Store) RequestAgentRun(_ context.Context, agent string, _ int64, at time.Time) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.Requests[agent]; ok {
		return old, nil
	}
	s.Requests[agent] = at
	return at, nil
}

// TakeAgentRequest implements ai.Store.
func (s *Store) TakeAgentRequest(_ context.Context, agent string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.Requests[agent]
	delete(s.Requests, agent)
	return ok, nil
}

// StartAgentRun implements ai.Store.
func (s *Store) StartAgentRun(_ context.Context, agent, replica string, at time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := int64(len(s.Runs) + 1)
	s.Runs = append(s.Runs, ai.AgentRun{ID: id, Agent: agent, Replica: replica, StartedAt: at})
	return id, nil
}

// FinishAgentRun implements ai.Store.
func (s *Store) FinishAgentRun(_ context.Context, id int64, r ai.AgentRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	o := &s.Runs[id-1]
	o.FinishedAt, o.Outcome, o.Tokens, o.Detail, o.Error = r.FinishedAt, r.Outcome, r.Tokens, r.Detail, r.Error
	return nil
}

// CloseOrphanRuns implements ai.Store.
func (s *Store) CloseOrphanRuns(_ context.Context, agent string, at time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for i := range s.Runs {
		if r := &s.Runs[i]; r.Agent == agent && r.FinishedAt == nil {
			r.FinishedAt, r.Outcome, r.Error = &at, ai.OutcomeFailed, "instance_stopped"
			n++
		}
	}
	return n, nil
}

// PruneAI implements ai.Store; it only counts calls.
func (s *Store) PruneAI(context.Context, time.Time) (map[string]int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Pruned++
	return map[string]int64{}, nil
}

// AICounts implements ai.Store.
func (s *Store) AICounts(context.Context) (ai.Counts, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Counts, nil
}

// Task returns a task by id, for assertions.
func (s *Store) Task(id string) ai.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Tasks[id]
}

// AgentRuns returns a copy of the run rows.
func (s *Store) AgentRuns() []ai.AgentRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ai.AgentRun(nil), s.Runs...)
}
