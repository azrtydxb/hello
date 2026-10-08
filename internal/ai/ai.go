// Package ai is the in-product AI agent's bounded model service (spec
// ai-agent). This file holds the contract the streams build on in parallel
// (plan ai-agent, Shared contracts 5): the types, the error codes and the
// interfaces. The implementations live in the files the plan names:
// provider.go (the only place a go-ai-sdk model is built), limits.go,
// usage.go, generate.go (Generate, DataBlock), tasks.go, heartbeat.go,
// scheduler.go, prune.go, metrics.go and status.go.
//
// Their signatures are fixed:
//
//	func New(cfg config.AIAgent, st Store, vk valkey.Client, reg prometheus.Registerer, log *slog.Logger) (*Service, error)
//	func Generate[T any](ctx context.Context, svc *Service, call Call[T]) (T, Usage, error)
//	func DataBlock(v any) string
//	func (*Tasks) Start(ctx context.Context, kind string, userID int64, sessionID string, fn TaskFunc) (taskID string, err error)
//	func (*Scheduler) Register(a Agent)
package ai

import (
	"context"
	"errors"
	"time"

	aisdk "github.com/azrtydxb/go-ai-sdk/ai"
)

// Error codes of an AI call or task (spec S-4, S-15, S-16, S-1).
const (
	CodeDisabled          = "ai_disabled"
	CodeBusy              = "ai_busy"
	CodeBudgetExhausted   = "ai_budget_exhausted"
	CodeInvalidOutput     = "invalid_output"
	CodeTimeout           = "timeout"
	CodeProviderError     = "provider_error"
	CodeEndpointNotPublic = "endpoint_not_private"
	CodeToolsUnsupported  = "tools_unsupported"
	CodeInstanceStopped   = "instance_stopped"
)

// Error is an AI failure with a stable code; its message never contains a
// prompt, a response or the API key.
type Error struct {
	Code    string
	Message string
	// Err is the cause, for logs and errors.Is; it is never shown to users.
	Err error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Unwrap returns the cause.
func (e *Error) Unwrap() error { return e.Err }

// Is reports whether target is an *Error with the same code, so
// errors.Is(err, &ai.Error{Code: ai.CodeBusy}) matches by code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// CodeOf returns the code of the first *Error in err's chain, or "".
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return ""
}

// Usage is the tokens one call, or a day, used. Reasoning tokens are
// counted and never stored or shown as text.
type Usage struct {
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64
	Calls           int64
}

// Total is the tokens counted against the budget.
func (u Usage) Total() int64 { return u.InputTokens + u.OutputTokens + u.ReasoningTokens }

// Call is one model call (spec S-4). T is the typed answer: its JSON schema
// is the model's Output, and Validate (when set) checks it; an error from
// either is sent back to the model, at most HELLO_AI_VALIDATION_ATTEMPTS
// times, then the call fails with CodeInvalidOutput.
type Call[T any] struct {
	// Feature names the call in the system prompt ("hello-feature:
	// <feature>"), metrics and logs: assistant, aiops_explain.
	Feature string
	// Background calls stop at the background share of the budget and never
	// wait for a slot; interactive calls wait at most 5 s.
	Background bool
	// System is the feature's instructions; Generate prepends the feature
	// line and the untrusted-data notice.
	System string
	// Data is untrusted content (SIP traffic, CDRs, configuration); it is
	// sent only through DataBlock.
	Data any
	// Prompt is the user turn, trusted text only.
	Prompt string
	// Tools are the read-only tools the model may call, at most MaxSteps
	// steps (default HELLO_AI_MAX_STEPS) in all.
	Tools    []aisdk.Tool
	MaxSteps int
	// Validate checks the decoded answer; nil accepts any that decodes.
	Validate func(context.Context, T) error
}

// Outcome is how an agent run ended (spec S-17), as stored in
// ai_agent_runs.outcome.
type Outcome string

// The outcomes.
const (
	OutcomeOK            Outcome = "ok"
	OutcomeNoChange      Outcome = "no_change"
	OutcomeSkippedBudget Outcome = "skipped_budget"
	OutcomeSkippedLocked Outcome = "skipped_locked"
	OutcomeFailed        Outcome = "failed"
)

// Agent is a background agent the scheduler runs (spec S-17). An Interval
// of 0 disables it.
type Agent interface {
	Name() string
	Interval() time.Duration
	// Run does one pass. The scheduler holds the agent's advisory lock and
	// records the outcome; Run returns OutcomeFailed with its error, never
	// panics for bad data.
	Run(ctx context.Context) (Outcome, error)
}

// TaskStatus is an ai_tasks.status (spec S-15).
type TaskStatus string

// The task statuses.
const (
	TaskQueued    TaskStatus = "queued"
	TaskRunning   TaskStatus = "running"
	TaskSucceeded TaskStatus = "succeeded"
	TaskFailed    TaskStatus = "failed"
)

// TaskFunc is the work of an interactive task; its result is stored with
// the task as JSON.
type TaskFunc func(ctx context.Context, taskID string) (result any, err error)
