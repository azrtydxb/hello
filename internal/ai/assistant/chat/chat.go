// Package chat holds the assistant's sessions and messages as the store
// and the HTTP layer see them (spec ai-agent S-8). It imports nothing of
// Hello's so internal/store and internal/api can use it without depending
// on the assistant's model and tool code.
package chat

import (
	"context"
	"encoding/json"
	"errors"
	"time"
)

// Errors the HTTP layer maps: ErrNotFound to 404 and ErrTaskRunning to 409
// task_running.
var (
	ErrNotFound    = errors.New("chat: not found")
	ErrTaskRunning = errors.New("chat: a task is running for this session")
)

// Session is one conversation; it belongs to its owner.
type Session struct {
	ID           string    `json:"id"`
	Owner        int64     `json:"owner"`
	Title        string    `json:"title"`
	CreatedAt    time.Time `json:"createdAt"`
	LastActiveAt time.Time `json:"lastActiveAt"`
}

// ToolCall is the stored summary of one tool call: never its result.
type ToolCall struct {
	OperationID string          `json:"operationId"`
	Arguments   json.RawMessage `json:"arguments,omitempty"`
	// Status is the HTTP status the replay answered; 0 when it did not.
	Status    int  `json:"status"`
	Truncated bool `json:"truncated"`
}

// Message roles.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one stored message.
type Message struct {
	ID         int64      `json:"id"`
	SessionID  string     `json:"-"`
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"toolCalls"`
	Citations  []int      `json:"citations"`
	ProposalID *string    `json:"proposalId"`
	TaskID     *string    `json:"taskId"`
	CreatedAt  time.Time  `json:"createdAt"`
}

// Task is a session's task as getAISession shows it.
type Task struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Status       string          `json:"status"`
	SessionID    *string         `json:"sessionId"`
	ErrorCode    *string         `json:"errorCode"`
	ErrorMessage *string         `json:"errorMessage"`
	Result       json.RawMessage `json:"result"`
	CreatedAt    time.Time       `json:"createdAt"`
	StartedAt    *time.Time      `json:"startedAt"`
	FinishedAt   *time.Time      `json:"finishedAt"`
}

// Detail is a session with its messages, oldest first, and its running
// task (nil when none).
type Detail struct {
	Session  Session   `json:"session"`
	Messages []Message `json:"messages"`
	Task     *Task     `json:"task"`
}

// Finding is the summary of an open finding the model sees as data.
type Finding struct {
	ID       string    `json:"id"`
	Type     string    `json:"type"`
	Subject  string    `json:"subject"`
	Severity string    `json:"severity"`
	Status   string    `json:"status"`
	Title    string    `json:"title"`
	LastSeen time.Time `json:"lastSeen"`
}

// StartFunc starts the task answering a just-stored user message; history
// is the session's last HistoryWindow messages, that one last.
type StartFunc func(ctx context.Context, messageID int64, history []Message) (taskID string, err error)

// Store keeps sessions and messages (internal/store implements it).
type Store interface {
	CreateAISession(ctx context.Context, owner int64, title string) (Session, error)
	// ListAISessions lists owner's sessions, or every session when owner is
	// 0, most recently active first.
	ListAISessions(ctx context.Context, owner int64) ([]Session, error)
	GetAISession(ctx context.Context, id string) (Session, error)
	RenameAISession(ctx context.Context, id, title string) (Session, error)
	// DeleteAISession deletes a session with its messages; ErrTaskRunning
	// while a task is queued or running for it.
	DeleteAISession(ctx context.Context, id string) error
	AIMessages(ctx context.Context, sessionID string) ([]Message, error)
	// ActiveAITask is the session's queued or running task, nil when none.
	ActiveAITask(ctx context.Context, sessionID string) (*Task, error)
	// PostAIMessage, serialised per session, refuses with ErrTaskRunning
	// while a task is queued or running for it, stores the user message,
	// names an untitled session title, marks it active, and calls start
	// (which records the task) before releasing the session; the message
	// is linked to the task.
	PostAIMessage(ctx context.Context, sessionID, content, title string, window int, start StartFunc) (messageID int64, taskID string, err error)
	// AddAIMessage stores an assistant message and marks the session active.
	AddAIMessage(ctx context.Context, m Message) (int64, error)
	// OpenAIFindings lists open and acknowledged findings, most severe and
	// recent first, at most limit.
	OpenAIFindings(ctx context.Context, limit int) ([]Finding, error)
}

// ErrInvalid is a request the HTTP layer answers 400.
var ErrInvalid = errors.New("invalid")

// User is who calls: the session owner check uses ID, Admin sees every
// session.
type User struct {
	ID    int64
	Admin bool
}
