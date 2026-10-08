// Package assistant is the in-product AI assistant (spec ai-agent S-6 to
// S-9): chat sessions and messages, the read-only tools the model may call,
// replayed through Hello's own API as the user with scope read only, and
// the message task whose structured answer may carry a proposal. Nothing
// here changes configuration: a proposal is validated and stored for a
// human to apply.
package assistant

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/ai"
	"github.com/azrtydxb/hello/internal/ai/assistant/chat"
	"github.com/azrtydxb/hello/internal/ai/proposal"
	"github.com/azrtydxb/hello/internal/apispec"
	"github.com/prometheus/client_golang/prometheus"
)

// Bounds of spec S-6 and S-8.
const (
	// MaxMessageLen is the most characters a user message may have.
	MaxMessageLen = 4000
	// MaxTitleLen is the most characters a session title may have.
	MaxTitleLen = 200
	// HistoryWindow is how many of a session's messages, the new one
	// included, reach the model; older ones are dropped, never summarised.
	HistoryWindow = 20
	// MaxToolCalls is the most tool calls one message may make.
	MaxToolCalls = 16
	// DefaultMaxSteps is HELLO_AI_MAX_STEPS's default.
	DefaultMaxSteps = 8
	// TaskKind is the ai_tasks.kind of a message task.
	TaskKind = "message"
	// autoTitleLen is how much of the first message names a session.
	autoTitleLen = 60
)

// The chat types, here under the assistant's name.
type (
	Session   = chat.Session
	ToolCall  = chat.ToolCall
	Message   = chat.Message
	Task      = chat.Task
	Detail    = chat.Detail
	Finding   = chat.Finding
	StartFunc = chat.StartFunc
	Store     = chat.Store
	User      = chat.User
)

// Roles and errors of package chat.
const (
	RoleUser      = chat.RoleUser
	RoleAssistant = chat.RoleAssistant
)

// Errors of package chat.
var (
	ErrNotFound    = chat.ErrNotFound
	ErrTaskRunning = chat.ErrTaskRunning
	ErrInvalid     = chat.ErrInvalid
)

// GenerateFunc is ai.Generate bound to the service:
// func(ctx, c) { return ai.Generate(ctx, svc, c) }.
type GenerateFunc func(ctx context.Context, call ai.Call[Answer]) (Answer, ai.Usage, error)

// Tasks starts interactive tasks (*ai.Tasks implements it).
type Tasks interface {
	Start(ctx context.Context, kind string, userID int64, sessionID string, fn ai.TaskFunc) (taskID string, err error)
}

// Proposals validates and stores the proposals answers carry.
type Proposals struct {
	Validator proposal.Validator
	Store     proposal.Store
}

// Config wires an Assistant.
type Config struct {
	Store    Store
	Tasks    Tasks
	Generate GenerateFunc
	// DataBlock is ai.DataBlock: every tool result and the history reach
	// the model only through it.
	DataBlock func(v any) string
	// API is Hello's API handler the tools replay through; Spec its
	// OpenAPI document.
	API  http.Handler
	Spec *apispec.Spec
	// Proposals is nil until proposals are wired; an answer's proposal is
	// then refused, never stored unvalidated.
	Proposals *Proposals
	// MaxSteps is HELLO_AI_MAX_STEPS (0 means DefaultMaxSteps).
	MaxSteps int
	Log      *slog.Logger
	// Registerer receives hello_ai_tool_calls_total; nil keeps it
	// unregistered.
	Registerer prometheus.Registerer
	// ToolCalls, when set, counts each tool call in the service's own
	// hello_ai_tool_calls_total (ai.Metrics.ToolCall) and replaces
	// Registerer: one process registers that name once.
	ToolCalls func(operation, result string)
}

// Assistant is the chat assistant.
type Assistant struct {
	cfg       Config
	tools     []toolDef
	toolCalls *prometheus.CounterVec
}

// New checks the tool list against the document and returns the assistant.
func New(cfg Config) (*Assistant, error) {
	if cfg.Store == nil || cfg.Tasks == nil || cfg.Generate == nil || cfg.DataBlock == nil || cfg.API == nil || cfg.Spec == nil {
		return nil, errors.New("assistant: store, tasks, generate, data block, API and spec are required")
	}
	if cfg.MaxSteps <= 0 {
		cfg.MaxSteps = DefaultMaxSteps
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	defs, err := toolDefs(cfg.Spec)
	if err != nil {
		return nil, err
	}
	calls := prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_ai_tool_calls_total",
		Help: "Assistant tool calls by operation and result (ok, truncated, error, limit).",
	}, []string{"operation", "result"})
	if cfg.Registerer != nil && cfg.ToolCalls == nil {
		if err := cfg.Registerer.Register(calls); err != nil {
			return nil, err
		}
	}
	return &Assistant{cfg: cfg, tools: defs, toolCalls: calls}, nil
}

// ListSessions lists the user's sessions, or every session for an admin.
func (a *Assistant) ListSessions(ctx context.Context, u User) ([]Session, error) {
	owner := u.ID
	if u.Admin {
		owner = 0
	}
	return a.cfg.Store.ListAISessions(ctx, owner)
}

// CreateSession starts a session owned by u.
func (a *Assistant) CreateSession(ctx context.Context, u User, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if utf8.RuneCountInString(title) > MaxTitleLen {
		return Session{}, fmt.Errorf("%w: title is longer than %d characters", ErrInvalid, MaxTitleLen)
	}
	return a.cfg.Store.CreateAISession(ctx, u.ID, title)
}

// session loads a session u may see: their own, or any for an admin.
func (a *Assistant) session(ctx context.Context, u User, id string) (Session, error) {
	s, err := a.cfg.Store.GetAISession(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if s.Owner != u.ID && !u.Admin {
		return Session{}, ErrNotFound
	}
	return s, nil
}

// GetSession returns a session with its messages and running task.
func (a *Assistant) GetSession(ctx context.Context, u User, id string) (Detail, error) {
	s, err := a.session(ctx, u, id)
	if err != nil {
		return Detail{}, err
	}
	msgs, err := a.cfg.Store.AIMessages(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	t, err := a.cfg.Store.ActiveAITask(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if msgs == nil {
		msgs = []Message{}
	}
	return Detail{Session: s, Messages: msgs, Task: t}, nil
}

// RenameSession sets a session's title.
func (a *Assistant) RenameSession(ctx context.Context, u User, id, title string) (Session, error) {
	title = strings.TrimSpace(title)
	if title == "" || utf8.RuneCountInString(title) > MaxTitleLen {
		return Session{}, fmt.Errorf("%w: title must have 1 to %d characters", ErrInvalid, MaxTitleLen)
	}
	if _, err := a.session(ctx, u, id); err != nil {
		return Session{}, err
	}
	return a.cfg.Store.RenameAISession(ctx, id, title)
}

// DeleteSession deletes a session and its messages.
func (a *Assistant) DeleteSession(ctx context.Context, u User, id string) error {
	if _, err := a.session(ctx, u, id); err != nil {
		return err
	}
	return a.cfg.Store.DeleteAISession(ctx, id)
}

// PostMessage stores the user's question and starts the task answering it.
// Only the session's owner may post, admin or not: the task reads as them.
func (a *Assistant) PostMessage(ctx context.Context, u User, sessionID, content string) (messageID int64, taskID string, err error) {
	content = strings.TrimSpace(content)
	if content == "" || utf8.RuneCountInString(content) > MaxMessageLen {
		return 0, "", fmt.Errorf("%w: content must have 1 to %d characters", ErrInvalid, MaxMessageLen)
	}
	s, err := a.cfg.Store.GetAISession(ctx, sessionID)
	if err != nil {
		return 0, "", err
	}
	if s.Owner != u.ID {
		return 0, "", ErrNotFound
	}
	start := func(ctx context.Context, msgID int64, history []Message) (string, error) {
		return a.cfg.Tasks.Start(ctx, TaskKind, u.ID, sessionID, func(ctx context.Context, taskID string) (any, error) {
			return a.answer(ctx, u.ID, sessionID, taskID, history)
		})
	}
	return a.cfg.Store.PostAIMessage(ctx, sessionID, content, autoTitle(content), HistoryWindow, start)
}

// autoTitle is a session's title from its first message: the first line,
// cut at autoTitleLen characters.
func autoTitle(content string) string {
	line, _, _ := strings.Cut(content, "\n")
	line = strings.TrimSpace(line)
	if utf8.RuneCountInString(line) <= autoTitleLen {
		return line
	}
	r := []rune(line)
	return strings.TrimSpace(string(r[:autoTitleLen])) + "…"
}
